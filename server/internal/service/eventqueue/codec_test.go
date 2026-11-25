package eventqueue

import (
	"encoding/json"
	"reflect"
	"testing"

	"qingqiu-world-server/internal/model"
)

// Cover the result payloads that producer/restart tests do not already decode.
// Both the historical raw representation and the wrapped representation must
// retain their typed fields; only the wrapped form carries Action provenance.
func TestResultPayloadsPreserveContextAcrossStoredFormats(t *testing.T) {
	cases := []struct {
		name    string
		kind    AgentEventType
		payload any
	}{
		{"received_jinshu_list", EventTypeJinshuListed, &JinshuListedPayload{Query: "report", Page: 2, Results: []JinshuListItem{{JinshuID: 41, FromName: "Sender", Topic: "Report", IsRead: true, CreatedAt: "2026-11-24T09:00:00+08:00"}}}},
		{"sent_jinshu_list", EventTypeJinshuSentListed, &JinshuSentListedPayload{Query: "report", Page: 3, Results: []JinshuSentListItem{{JinshuID: 42, ToName: "Recipient", Topic: "Report", CreatedAt: "2026-11-24T10:00:00+08:00"}}}},
		{"owned_space_inspection", EventTypeOwnedSpaceInspected, &OwnedSpaceInspectedPayload{Scope: "work/7", Result: "report.txt: 128 bytes"}},
		{"execution_slot_available", EventTypeExecutionSlotAvailable, &ExecutionSlotAvailablePayload{WaitActionID: 51, Intention: "Review the completed report"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, wrapped := range []bool{false, true} {
				name := "legacy_raw"
				if wrapped {
					name = "wrapped"
				}
				t.Run(name, func(t *testing.T) {
					original := &AgentEvent{Type: tc.kind, SessionID: 9, EventID: 81, Payload: tc.payload}
					var encoded string
					if wrapped {
						original.TriggerAction = &TriggerAction{Background: "original request", Reason: "requested result"}
						var err error
						encoded, err = Serialize(original)
						if err != nil {
							t.Fatal(err)
						}
					} else {
						raw, err := json.Marshal(tc.payload)
						if err != nil {
							t.Fatal(err)
						}
						encoded = string(raw)
					}
					restored, err := Deserialize(model.AgentEventBuffer{EventType: int(tc.kind), SessionID: original.SessionID, EventID: original.EventID, PayloadJSON: encoded})
					if err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(restored, original) {
						t.Fatalf("stored result lost context: got=%+v want=%+v", restored, original)
					}
				})
			}
		})
	}
}
