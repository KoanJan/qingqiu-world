package memory

import (
	"container/list"
	"context"
	"sort"
	"sync"
	"time"

	"qingqiu-world-server/internal/config"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/service/energy"
)

const (
	dailyRequestMinIntervalMS = 1000
	dailyRequestMaxIntervalMS = 60000
)

// dailyIndexTask names one resource group on one completed local date. Only
// agents, rather than tasks, enter the global ready queue.
type dailyIndexTask struct {
	date string
	key  dailyGroupKey
}

// dailyIndexResult returns the attempted shard to the scheduling loop, which
// alone updates queue membership and decides whether the agent can rejoin.
type dailyIndexResult struct {
	personID int64
	task     dailyIndexTask
	err      error
}

// dailyIndexScheduler keeps agents, rather than shards, in a fair ready queue.
// Its queue and task maps belong to runDailyMemoryIndex; workers send results
// through done and never mutate scheduler state directly.
type dailyIndexScheduler struct {
	state  dailyIndexState
	ready  *list.List                   // Idle agents in opportunity order.
	queued map[int64]*list.Element      // Prevents duplicate ready nodes.
	busy   map[int64]context.CancelFunc // Agents with one shard in progress.
	tasks  map[int64][]dailyIndexTask   // Missing shards found at discovery.
	done   chan dailyIndexResult
	gate   *dailyRequestGate
	now    func() time.Time
}

// newDailyIndexScheduler keeps the historical configuration name, but its
// interval now spaces shard opportunities rather than individual LLM calls.
func newDailyIndexScheduler() *dailyIndexScheduler {
	configuredMS := config.Get().DailyMemoryRequestIntervalMS
	interval := dailyRequestInterval(configuredMS)
	if configuredMS < dailyRequestMinIntervalMS || configuredMS > dailyRequestMaxIntervalMS {
		applogger.Warn("daily memory opportunity interval adjusted to supported range", "configured_ms", configuredMS, "effective_ms", interval.Milliseconds())
	}
	return &dailyIndexScheduler{
		state:  dailyIndexState{pending: make(map[int64]map[string]struct{})},
		ready:  list.New(),
		queued: make(map[int64]*list.Element),
		busy:   make(map[int64]context.CancelFunc),
		tasks:  make(map[int64][]dailyIndexTask),
		done:   make(chan dailyIndexResult),
		gate:   newDailyRequestGate(interval),
		now:    time.Now,
	}
}

// dailyRequestInterval clamps the deployment setting to its supported range.
func dailyRequestInterval(ms int) time.Duration {
	if ms < dailyRequestMinIntervalMS {
		ms = dailyRequestMinIntervalMS
	} else if ms > dailyRequestMaxIntervalMS {
		ms = dailyRequestMaxIntervalMS
	}
	return time.Duration(ms) * time.Millisecond
}

// runDailyMemoryIndex discovers missing shards at startup and every five
// minutes. Each gate permit removes one agent from the ready queue for one
// shard; completed workers may rejoin the tail while other shards keep running.
func runDailyMemoryIndex(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	scheduler := newDailyIndexScheduler()
	ticker := time.NewTicker(dailyMemoryRefreshInterval)
	defer ticker.Stop()
	defer scheduler.cancelWorkers()
	permit := make(chan error)
	waitingForPermit := false
	if err := scheduler.discover(ctx); err != nil && ctx.Err() == nil {
		applogger.Error("daily memory index discovery failed", "error", err)
	}
	for {
		if !waitingForPermit && scheduler.ready.Len() > 0 {
			waitingForPermit = true
			// The gate can sleep for several seconds. Wait outside this loop so
			// discovery and worker completions remain responsive meanwhile.
			go func() {
				err := scheduler.gate.wait(ctx)
				select {
				case permit <- err:
				case <-ctx.Done():
				}
			}()
		}
		select {
		case <-ctx.Done():
			return
		case err := <-permit:
			waitingForPermit = false
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				applogger.Error("daily memory opportunity wait failed", "error", err)
				return
			}
			scheduler.dispatch(ctx)
		case <-ticker.C:
			if err := scheduler.discover(ctx); err != nil && ctx.Err() == nil {
				applogger.Error("daily memory index discovery failed", "error", err)
			}
		case result := <-scheduler.done:
			scheduler.finish(result)
		}
	}
}

// dailyDateEligible excludes today and dates outside the rolling backfill window.
func dailyDateEligible(now time.Time, date string) bool {
	today := now.In(energy.Timezone())
	start := recentDailyBackfillStart(now).Format("2006-01-02")
	return date >= start && date < today.Format("2006-01-02")
}

// discover reconciles source groups with stored accounts. It preserves an
// unfinished date in memory until completion or expiry, and rescans the seven
// completed dates after restart instead of persisting a work queue.
func (s *dailyIndexScheduler) discover(ctx context.Context) error {
	if err := refreshDailyMemoryIndex(ctx, &s.state); err != nil {
		return err
	}
	now := s.now()
	cutoff := recentDailyBackfillStart(now).Format("2006-01-02")
	today := now.In(energy.Timezone()).Format("2006-01-02")
	updated := make(map[int64][]dailyIndexTask)
	for personID, dates := range s.state.pending {
		ordered := make([]string, 0, len(dates))
		for date := range dates {
			ordered = append(ordered, date)
		}
		sort.Strings(ordered) // Older eligible dates get their chance before expiry.
		for _, date := range ordered {
			if err := ctx.Err(); err != nil {
				return err
			}
			if date >= today {
				continue // The current date is not complete yet.
			}
			missing, err := dailyUnindexedGroups(ctx, personID, date)
			if err != nil {
				applogger.Error("daily memory date discovery failed", "person_id", personID, "date", date, "error", err)
				if date < cutoff {
					applogger.Warn("daily memory date left automatic window with unknown coverage", "person_id", personID, "date", date)
					delete(dates, date)
				}
				continue // Unexpired dates remain pending for the next discovery.
			}
			if date < cutoff {
				if len(missing) > 0 {
					applogger.Warn("daily memory date left automatic window with missing groups", "person_id", personID, "date", date, "missing_groups", len(missing))
				}
				delete(dates, date)
				continue
			}
			if len(missing) == 0 {
				delete(dates, date)
				continue
			}
			for _, group := range missing {
				updated[personID] = append(updated[personID], dailyIndexTask{date: date, key: group.Key})
			}
		}
		if len(dates) == 0 {
			delete(s.state.pending, personID)
		}
	}
	// Reconcile missing shards without reshuffling agents already waiting.
	// Busy agents only rejoin through finish, after their active shard ends.
	s.tasks = updated
	for personID, node := range s.queued {
		if len(s.tasks[personID]) == 0 {
			s.ready.Remove(node)
			delete(s.queued, personID)
		}
	}
	ids := make([]int64, 0, len(s.tasks))
	for personID := range s.tasks {
		ids = append(ids, personID)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, personID := range ids {
		s.enqueue(personID)
	}
	return nil
}

// enqueue puts an idle agent at the tail once, after discarding expired tasks.
func (s *dailyIndexScheduler) enqueue(personID int64) {
	if s.queued[personID] != nil {
		return
	}
	if _, busy := s.busy[personID]; busy {
		return
	}
	tasks := s.tasks[personID]
	eligible := tasks[:0]
	now := s.now()
	for _, task := range tasks {
		if dailyDateEligible(now, task.date) {
			eligible = append(eligible, task)
		}
	}
	s.tasks[personID] = eligible
	if len(eligible) == 0 {
		return
	}
	s.queued[personID] = s.ready.PushBack(personID)
}

// dispatch consumes one permit. It may skip stale queue nodes, but starts at
// most one shard; it never drains the ready queue in a single opportunity.
func (s *dailyIndexScheduler) dispatch(ctx context.Context) {
	// Stale nodes may be skipped, but one permit starts at most one shard.
	for s.ready.Len() > 0 && ctx.Err() == nil {
		node := s.ready.Front()
		personID := node.Value.(int64)
		s.ready.Remove(node)
		delete(s.queued, personID)
		if len(s.tasks[personID]) == 0 {
			continue
		}
		if !dailyDateEligible(s.now(), s.tasks[personID][0].date) {
			// A local-day boundary may have passed since this node was queued.
			// Drop expired tasks and keep later eligible ones in rotation.
			s.enqueue(personID)
			continue
		}
		workerCtx, cancel := context.WithCancel(ctx)
		task := s.tasks[personID][0]
		s.busy[personID] = cancel
		go func() {
			err := processDailyIndexTask(workerCtx, personID, task)
			select {
			case s.done <- dailyIndexResult{personID: personID, task: task, err: err}:
			case <-ctx.Done():
			}
		}()
		return
	}
}

// processDailyIndexTask rechecks a group's durable state immediately before
// generation, so a rediscovered or already completed task is harmless.
func processDailyIndexTask(ctx context.Context, personID int64, task dailyIndexTask) error {
	if !dailyDateEligible(time.Now(), task.date) {
		return nil
	}
	missing, err := dailyUnindexedGroups(ctx, personID, task.date)
	if err != nil {
		return err
	}
	for _, group := range missing {
		if group.Key == task.key {
			return writeDailyMemoryGroup(ctx, personID, task.date, group)
		}
	}
	return nil
}

// finish removes this attempt from the current queue even on failure. A failed
// shard can be rediscovered at the next five-minute check, not retried here.
func (s *dailyIndexScheduler) finish(result dailyIndexResult) {
	cancel, busy := s.busy[result.personID]
	if !busy {
		applogger.Error("daily memory worker returned an unexpected task", "person_id", result.personID, "date", result.task.date, "group_type", result.task.key.Type, "group_id", result.task.key.ID)
		return
	}
	cancel()
	delete(s.busy, result.personID)
	if result.err != nil {
		applogger.Error("daily memory group generation failed", "person_id", result.personID, "date", result.task.date, "group_type", result.task.key.Type, "group_id", result.task.key.ID, "error", result.err)
	}
	remaining := s.tasks[result.personID]
	for i, task := range remaining {
		if task == result.task {
			s.tasks[result.personID] = append(remaining[:i], remaining[i+1:]...)
			break
		}
	}
	if len(s.tasks[result.personID]) == 0 {
		delete(s.tasks, result.personID)
	}
	s.enqueue(result.personID)
}

// cancelWorkers stops active shards when the scheduler exits.
func (s *dailyIndexScheduler) cancelWorkers() {
	for _, cancel := range s.busy {
		cancel()
	}
}

// dailyRequestGate spaces shard opportunities globally without banking permits.
type dailyRequestGate struct {
	mu       sync.Mutex
	interval time.Duration
	next     time.Time
	now      func() time.Time
	sleep    func(context.Context, time.Duration) error
}

// newDailyRequestGate makes the first opportunity wait a full interval.
func newDailyRequestGate(interval time.Duration) *dailyRequestGate {
	return &dailyRequestGate{
		interval: interval,
		next:     time.Now().Add(interval),
		now:      time.Now,
		sleep:    sleepDailyRequest,
	}
}

func sleepDailyRequest(ctx context.Context, duration time.Duration) error {
	if duration <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return ctx.Err()
	}
}

// wait grants one opportunity and starts the next interval from that grant;
// elapsed idle time does not accumulate permits for a later burst.
func (g *dailyRequestGate) wait(ctx context.Context) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if delay := g.next.Sub(g.now()); delay > 0 {
		if err := g.sleep(ctx, delay); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	g.next = g.now().Add(g.interval)
	return nil
}
