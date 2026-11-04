package memory

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/energy"
)

func TestDailyRequestIntervalUsesSupportedRange(t *testing.T) {
	for _, tc := range []struct {
		ms   int
		want time.Duration
	}{
		{-100, time.Second},
		{500, time.Second},
		{5000, 5 * time.Second},
		{61000, time.Minute},
	} {
		if got := dailyRequestInterval(tc.ms); got != tc.want {
			t.Errorf("interval for %d ms = %s, want %s", tc.ms, got, tc.want)
		}
	}
}

func TestDailyRequestGateSpacesOneHundredOpportunitiesWithoutBanking(t *testing.T) {
	interval := 5 * time.Second
	clock := time.Unix(1000, 0)
	starts := make([]time.Time, 0, 100)
	gate := &dailyRequestGate{
		interval: interval,
		next:     clock.Add(interval),
		now:      func() time.Time { return clock },
		sleep: func(ctx context.Context, duration time.Duration) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			clock = clock.Add(duration)
			starts = append(starts, clock)
			return nil
		},
	}
	var workers sync.WaitGroup
	workers.Add(100)
	errs := make(chan error, 100)
	for i := 0; i < 100; i++ {
		go func() {
			defer workers.Done()
			errs <- gate.wait(context.Background())
		}()
	}
	workers.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(starts) != 100 || !starts[0].Equal(time.Unix(1000, 0).Add(interval)) {
		t.Fatalf("startup rate gate issued unexpected starts: %+v", starts)
	}
	for i := 1; i < len(starts); i++ {
		if starts[i].Sub(starts[i-1]) < interval {
			t.Fatalf("opportunity %d started %s after prior opportunity", i, starts[i].Sub(starts[i-1]))
		}
	}
	clock = clock.Add(time.Hour)
	if err := gate.wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	previous := clock
	if err := gate.wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if clock.Sub(previous) != interval {
		t.Fatalf("idle time banked a second permit: gap=%s", clock.Sub(previous))
	}
}

func TestDailySchedulerRotatesAgentsAndDefersFailedShard(t *testing.T) {
	s := newDailyIndexScheduler()
	date := time.Now().In(energy.Timezone()).AddDate(0, 0, -1).Format("2006-01-02")
	first := dailyIndexTask{date: date, key: dailyGroupKey{Type: model.DailyMemoryGroupWork, ID: 1}}
	second := dailyIndexTask{date: first.date, key: dailyGroupKey{Type: model.DailyMemoryGroupWork, ID: 2}}
	for id := int64(1); id <= 100; id++ {
		s.tasks[id] = []dailyIndexTask{first}
		s.enqueue(id)
		s.enqueue(id)
	}
	s.tasks[1] = []dailyIndexTask{first, second}
	if s.ready.Len() != 100 || len(s.queued) != 100 {
		t.Fatalf("ready agents=%d, node map=%d", s.ready.Len(), len(s.queued))
	}
	head := s.ready.Front()
	if head.Value.(int64) != 1 {
		t.Fatalf("first agent=%v, want 1", head.Value)
	}
	s.ready.Remove(head)
	delete(s.queued, 1)
	s.busy[1] = func() {}
	s.enqueue(1)
	if s.ready.Len() != 99 {
		t.Fatal("busy agent was requeued")
	}
	s.finish(dailyIndexResult{personID: 1, task: first, err: errors.New("summary failed")})
	if s.ready.Len() != 100 || s.ready.Back().Value.(int64) != 1 {
		t.Fatal("agent with another shard did not rejoin at the tail")
	}
	if len(s.tasks[1]) != 1 || s.tasks[1][0] != second {
		t.Fatalf("failed shard was retried in the same discovery cycle: %+v", s.tasks[1])
	}
	s.ready.Remove(s.queued[1])
	delete(s.queued, 1)
	s.busy[1] = func() {}
	s.finish(dailyIndexResult{personID: 1, task: second})
	if s.queued[1] != nil || len(s.tasks[1]) != 0 {
		t.Fatal("agent without remaining work stayed in the ready queue")
	}
}

func TestDailySchedulerDispatchConsumesOnlyOneOpportunity(t *testing.T) {
	recallTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s := newDailyIndexScheduler()
	date := time.Now().In(energy.Timezone()).AddDate(0, 0, -1).Format("2006-01-02")
	for id := int64(1); id <= 1000; id++ {
		s.tasks[id] = []dailyIndexTask{{date: date, key: dailyGroupKey{Type: model.DailyMemoryGroupWork, ID: id}}}
		s.enqueue(id)
	}

	s.dispatch(ctx)
	if len(s.busy) != 1 || s.ready.Len() != 999 {
		t.Fatalf("one opportunity started %d workers; ready agents=%d", len(s.busy), s.ready.Len())
	}
	// A later discovery may run while the first shard is still active. Only a
	// new opportunity may remove the next agent, regardless of worker state.
	if _, busy := s.busy[1]; !busy || s.ready.Front().Value.(int64) != 2 {
		t.Fatal("first opportunity did not select the queue head")
	}
	s.dispatch(ctx)
	if len(s.busy) != 2 || s.ready.Len() != 998 {
		t.Fatalf("second opportunity did not start exactly one other agent: workers=%d ready=%d", len(s.busy), s.ready.Len())
	}
	if _, busy := s.busy[2]; !busy {
		t.Fatal("second opportunity did not select the next agent")
	}
	for range 2 {
		select {
		case result := <-s.done:
			s.finish(result)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}

func TestDailySchedulerRediscoversMissingShardAndDropsExpiredDate(t *testing.T) {
	db := recallTestDB(t)
	today := time.Now().In(energy.Timezone())
	yesterday := today.AddDate(0, 0, -1).Truncate(time.Second)
	old := today.AddDate(0, 0, -8).Truncate(time.Second)
	for _, at := range []time.Time{yesterday, old} {
		if err := db.Create(&model.Decision{PersonID: 1, CreatedAt: at, Thoughts: "A past consideration"}).Error; err != nil {
			t.Fatal(err)
		}
	}
	date := yesterday.Format("2006-01-02")
	s := newDailyIndexScheduler()
	if err := s.discover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(s.tasks[1]) != 1 || s.tasks[1][0].date != date {
		t.Fatalf("startup work=%+v; want only yesterday", s.tasks[1])
	}
	s.ready.Remove(s.queued[1])
	delete(s.queued, 1)
	s.busy[1] = func() {}
	s.finish(dailyIndexResult{personID: 1, task: s.tasks[1][0], err: errors.New("transient")})
	if len(s.tasks[1]) != 0 || s.ready.Len() != 0 {
		t.Fatal("failed shard was retried immediately")
	}
	if err := s.discover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(s.tasks[1]) != 1 || s.ready.Len() != 1 {
		t.Fatalf("next discovery did not restore missing shard: %+v", s.tasks[1])
	}
	s.state.pending[1][old.Format("2006-01-02")] = struct{}{}
	if err := s.discover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, found := s.state.pending[1][old.Format("2006-01-02")]; found {
		t.Fatal("expired date remained in pending work")
	}
	for _, task := range s.tasks[1] {
		if task.date != date {
			t.Fatalf("expired date was scheduled: %+v", task)
		}
	}
}
