package privatespace

import (
	"context"
	"strings"
	"time"

	"gorm.io/gorm"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/dops"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/eventqueue"
	"qingqiu-world-server/internal/service/llm"
	"qingqiu-world-server/internal/service/memory"
	"qingqiu-world-server/internal/service/tools"
)

// digestTimeout bounds the digest LLM call so a stalled provider cannot
// block the loop's exit path indefinitely.
const digestTimeout = 2 * time.Minute

// digestMaxInputBytes bounds the reasoning trail fed to the summarizer.
const digestMaxInputBytes = 32 * 1024

// digestSystemPrompt instructs the agent to write the digest in its own
// first-person voice, focusing on decisions and ideas while never reproducing
// private file contents.
const digestSystemPrompt = `You are writing a short digest of your last private-space session, for yourself.

Write a few sentences:
- Focus on what I decided, explored, considered, and plan to do next — the reasoning and ideas, not the artifacts.
- Do NOT quote or reproduce file contents, code, or any other private material.
- Write in the same language the session used.
- Output only the digest text, no headings or extra commentary.`

// generateDigest produces the session digest and delivers it as a
// PSCompleted event so the agent's runtime can observe it into memory.
//
// Producer chain (mirrors SendBiographyEvent): persist the digest via dops,
// record the memory event, then enqueue the runtime event.
func (l *Loop) generateDigest() {
	trail := l.buildReasoningTrail()
	if trail == "" {
		applogger.Info("PrivateSpace digest skipped: no substantive conversation",
			"person_id", l.personID,
		)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), digestTimeout)
	defer cancel()

	digest, err := l.llmClient.Chat(ctx, []llm.Message{
		{Role: "system", Content: digestSystemPrompt},
		{Role: "user", Content: trail},
	})
	if err != nil {
		applogger.Error("PrivateSpace digest generation failed",
			"person_id", l.personID, "error", err,
		)
		return
	}
	digest = strings.TrimSpace(digest)
	if digest == "" {
		applogger.Error("PrivateSpace digest generation returned empty content",
			"person_id", l.personID,
		)
		return
	}

	record, err := dops.CreatePSDigest(l.personID, digest, l.participatingActionIDs()...)
	if err != nil {
		applogger.Error("PrivateSpace failed to persist digest",
			"person_id", l.personID, "error", err,
		)
		return
	}
	handoff := &model.FocusHandoff{
		PersonID:           l.personID,
		SessionID:          0,
		WorkID:             0,
		Source:             model.FocusSourcePrivate,
		Status:             model.FocusHandoffCompleted,
		Orientation:        "Private-space reflection",
		Summary:            digest,
		ConfirmedFindings:  "",
		ArtifactReferences: "",
		Unresolved:         "",
		NextStep:           "Review this handoff when a later focus is relevant.",
	}
	if err := dops.CreateFocusHandoff(handoff); err != nil {
		applogger.Error("PrivateSpace failed to persist focus handoff", "person_id", l.personID, "digest_id", record.ID, "error", err)
	} else {
		if err := memory.RefreshSource(model.MemorySourceFocusHandoff, handoff.ID); err != nil {
			applogger.Error("PrivateSpace failed to index focus handoff", "handoff_id", handoff.ID, "error", err)
		}
	}

	var eventID int64
	err = database.DB.Transaction(func(tx *gorm.DB) error {
		var err error
		eventID, err = memory.RecordReferencedEventTx(tx, model.EventTypePSDigest, record.ID)
		if err != nil {
			return err
		}
		return eventqueue.AppendTx(tx, &eventqueue.AgentEvent{Type: eventqueue.EventTypePSCompleted, EventID: eventID,
			Payload: &eventqueue.PSCompletedPayload{DigestID: record.ID, Digest: digest}}, l.personID)
	})
	if err != nil {
		applogger.Error("PrivateSpace: persist digest Event and delivery", "digest_id", record.ID, "error", err)
		return
	}
	if err := memory.RefreshSource(model.MemorySourceEvent, eventID); err != nil {
		applogger.Error("PrivateSpace: index digest Event", "event_id", eventID, "error", err)
	}
	memory.EnqueueEventEmbedding(eventID, digest)
	eventqueue.Notify(l.personID)

	applogger.Info("PrivateSpace digest dispatched",
		"person_id", l.personID,
		"digest_id", record.ID,
		"event_id", eventID,
	)
}

// buildReasoningTrail collects the agent's own assistant utterances and
// injected thoughts from the accumulated conversation — the decisions and
// ideas this digest is about. Tool results are excluded by design so private
// file contents never leave the private space through the digest.
func (l *Loop) buildReasoningTrail() string {
	var parts []string
	for _, m := range l.messages {
		switch m.Role {
		case "assistant":
			if m.Content != "" {
				parts = append(parts, "Me: "+m.Content)
			}
		case "user":
			if strings.HasPrefix(m.Content, "[I thought] ") {
				parts = append(parts, m.Content)
			}
		}
	}
	if len(parts) == 0 {
		return ""
	}
	trail, _ := tools.TruncateHead(strings.Join(parts, "\n\n"), digestMaxInputBytes)
	return trail
}
