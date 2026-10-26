package experience

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/dops"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/aos"
	"qingqiu-world-server/internal/service/llm"
)

// reflectionTimeout bounds one reflection LLM call. The reflection output is
// a large structured JSON and the model generates at ~40 tokens/s, so 60s
// was not enough (observed as repeated deadline-exceeded retries). Aligned
// with digestTimeout.
const reflectionTimeout = 2 * time.Minute

// reflectOutput is the structured output from the LLM during reflection.
type reflectOutput struct {
	Title       string `json:"title" jsonschema:"description=Transferable lesson stated as a general principle"`
	Description string `json:"description" jsonschema:"description=One sentence stating what this teaches — used for semantic matching. State the insight, not what was done."`
	WhenToUse   string `json:"when_to_use" jsonschema:"description=What work patterns, trigger phrases, or problem patterns indicate this experience applies. Each on its own line. Helps distinguish similar but inapplicable situations from different but applicable ones."`
	Guidelines  string `json:"guidelines" jsonschema:"description=Actionable advice with rationale. What to do, why, and in what order. Decision heuristics, sequencing rules, proven patterns."`
	Pitfalls    string `json:"pitfalls" jsonschema:"description=Known failure modes. What can go wrong, early warning signs, and how to prevent or recover."`
	Procedure   string `json:"procedure" jsonschema:"description=Numbered steps. Only include if a repeatable, cross-project workflow emerged. Leave empty if none."`
	UpdateExpID int64  `json:"update_exp_id" jsonschema:"description=If this experience refines or overlaps with an existing experience you already have, set this to that experience's id. Leave 0 to create a new experience."`
	Skip        bool   `json:"skip" jsonschema:"description=Set to true if nothing worth extracting"`
}

// CheckReflection scans registered Workspace notes, including historical
// directories registered during migration, and reflects changed content.
//
// Dedup is file-based: <workspace metadata>/fingerprint.txt stores the SHA-256
// hash of notes.jsonl as it was at the end of the last reflection. If the
// current notes.jsonl hash matches, the Workspace is skipped. If fingerprint.txt
// is missing (first reflection) or differs, reflection runs.
//
// This is the public entry point called from the agent heartbeat.
// Safe to call when the embedding service is not configured — does nothing.
func CheckReflection(ctx context.Context, personID int64) {
	if embeddingSvc == nil {
		return
	}
	var workspaces []model.Workspace
	if err := database.DB.Where("person_id = ?", personID).Find(&workspaces).Error; err != nil {
		applogger.Error("CheckReflection: failed to list Workspaces", "person_id", personID, "error", err)
		return
	}
	for _, record := range workspaces {
		metaDir, err := aos.GetWorkspaceMetaDir(record)
		if err != nil {
			applogger.Error("CheckReflection: Workspace metadata unavailable", "person_id", personID, "workspace_id", record.ID, "error", err)
			continue
		}
		checkNotesReflection(ctx, personID, model.AgentExperienceSourceWorkspaceReflection, record.ID, metaDir)
	}
}

// checkNotesReflection applies the same fingerprint rule to either source.
// A missing notes file is normal for a Workspace that has never been used.
func checkNotesReflection(ctx context.Context, personID int64, source model.AgentExperienceSource, sourceID int64, metaDir string) {
	fingerprint, err := aos.NotesFingerprint(metaDir)
	if err != nil {
		applogger.Error("CheckReflection: failed to read notes", "person_id", personID, "source", source, "source_id", sourceID, "error", err)
		return
	}
	if fingerprint == "" {
		return
	}
	fpFile := filepath.Join(metaDir, "fingerprint.txt")
	last, err := os.ReadFile(fpFile)
	if err != nil && !os.IsNotExist(err) {
		applogger.Error("CheckReflection: failed to read fingerprint", "file", fpFile, "error", err)
		return
	}
	if err == nil && string(last) == fingerprint {
		return
	}
	content := formatNotesForReflection(aos.ReadAllNotes(metaDir))
	if content == "" {
		return
	}
	go reflectNotes(ctx, personID, source, sourceID, content, fingerprint, fpFile)
}

// reflectNotes runs the LLM reflection for one Workspace's notes and
// writes the new fingerprint to fpFile on success (including skip), so the
// next heartbeat will not re-trigger reflection for unchanged notes.
//
// Runs in a goroutine — errors are logged internally; callers need not
// handle return values.
//
// On LLM or parse failure, the fingerprint is NOT written, so the next
// heartbeat will retry.
func reflectNotes(ctx context.Context, personID int64, source model.AgentExperienceSource, sourceID int64, notesContent, currentFingerprint, fpFile string) {
	ctx, cancel := context.WithTimeout(ctx, reflectionTimeout)
	defer cancel()

	// Load agent config LLM config from DB
	var ac model.AgentConfig
	if err := database.DB.Where("person_id = ?", personID).First(&ac).Error; err != nil {
		applogger.Error("Reflection: failed to load agent config", "person_id", personID, "error", err)
		return
	}
	llmCfg, err := dops.GetLLMConfig(ac.LLMConfigID)
	if err != nil {
		applogger.Error("Reflection: failed to load LLM config", "person_id", personID, "error", err)
		return
	}
	chatModel := llm.NewChatModelWithTemperature(
		llmCfg.BaseURL, llmCfg.APIKey, llmCfg.ModelID, llm.TemperatureControlled,
	)

	schema := llm.GenerateSchema[reflectOutput]()

	// Note: existing experiences are NOT loaded into the prompt. Loading them
	// would bloat the context as the experience library grows. Instead, the
	// LLM is given the option to return update_exp_id from its own knowledge
	// of exp_ids it has seen during focused-work execution (via scan/recall tools).
	prompt := `Distill transferable experience from notes about work in one Workspace.

The notes below may cover several Works that used the same file environment. Extract only the abstract knowledge that could help with a completely different future work — do not summarize or reorganize the notes themselves.

Strip work-identifying details (project names, person names, specific file paths) and host-environment coupling (system-specific tools like write_notes/wake_me_when, internal APIs, system config) — these are not transferable. Keep concrete technical details (domain APIs like Canvas/fillText, library names, function signatures, algorithm steps) — they are the actionable value, not host coupling.

Fill each output field as follows:

title: The transferable lesson, stated as a general principle.

description: One sentence stating the core insight — used for semantic matching. State what this teaches, not what was done.

when_to_use: What work patterns, trigger phrases, or problem patterns indicate this experience applies. Helps distinguish "looks similar but isn't" from "looks different but is". Leave empty if the lesson applies broadly.

guidelines: Actionable advice with rationale. What to do, why, and in what order. Decision heuristics, sequencing rules, proven patterns.

pitfalls: Known failure modes. What can go wrong, early warning signs, and how to prevent or recover. Leave empty if no pitfalls were encountered.

procedure: Numbered steps. Only include if a repeatable workflow emerged. Leave empty if none.

update_exp_id: If this experience refines or overlaps with an existing experience you already have (e.g., an exp_id you saw via scan_my_experience / recall_my_experience during this focused-work run), set this to that experience's id. Otherwise, leave it as 0 to create a new experience.

skip: true only if the log contains nothing transferable.

## Focused-work log
` + notesContent

	messages := []llm.Message{
		{Role: "user", Content: prompt},
	}

	schemaDef := llm.JSONSchemaDefinition{
		Name:   "reflect_output",
		Schema: schema,
		Strict: true,
	}

	response, err := chatModel.ChatWithJSONSchema(ctx, messages, schemaDef)
	if err != nil {
		applogger.Error("Reflection: LLM call failed", "person_id", personID, "error", err)
		return
	}

	var output reflectOutput
	if err := json.Unmarshal([]byte(response), &output); err != nil {
		applogger.Error("Reflection: failed to parse LLM output", "person_id", personID, "error", err)
		return
	}

	if output.Skip {
		applogger.Info("Reflection: nothing worth extracting", "person_id", personID, "source", source, "source_id", sourceID)
		writeFingerprint(fpFile, currentFingerprint)
		return
	}

	if output.Title == "" || output.Description == "" {
		applogger.Error("Reflection: LLM output missing required fields",
			"person_id", personID,
			"has_title", output.Title != "",
			"has_description", output.Description != "",
		)
		return
	}

	// Branch: update an existing experience or create a new one.
	// The LLM returns update_exp_id when it recognizes (from exp_ids it saw
	// during focused-work execution) that this lesson refines an existing one.
	if output.UpdateExpID > 0 {
		if err := updateExperience(ctx, output.UpdateExpID, personID,
			output.Title, output.Description, output.WhenToUse,
			output.Guidelines, output.Pitfalls, output.Procedure); err != nil {
			applogger.Error("Reflection: failed to update experience",
				"person_id", personID,
				"exp_id", output.UpdateExpID,
				"error", err,
			)
			return
		}
		applogger.Info("Reflection: experience updated",
			"person_id", personID,
			"exp_id", output.UpdateExpID,
			"source", source,
			"source_id", sourceID,
		)
		writeFingerprint(fpFile, currentFingerprint)
		return
	}

	// The source points to the historical Session or registered Workspace that
	// owns these notes; it never assigns shared notes to an individual Work.
	if _, err := createExperience(ctx, personID, source, sourceID,
		output.Title, output.Description, output.WhenToUse, output.Guidelines, output.Pitfalls, output.Procedure); err != nil {
		applogger.Error("Reflection: failed to save experience", "person_id", personID, "error", err)
		return
	}

	applogger.Info("Reflection: experience created",
		"person_id", personID,
		"source", source,
		"source_id", sourceID,
	)
	writeFingerprint(fpFile, currentFingerprint)
}

// writeFingerprint persists the given fingerprint to fpFile so the next
// heartbeat can detect whether notes.jsonl has changed since this reflection.
// Failures are logged but do not abort the caller — a missed write only
// causes a redundant re-reflection on the next heartbeat, which is safe.
func writeFingerprint(fpFile, fingerprint string) {
	if err := os.WriteFile(fpFile, []byte(fingerprint), 0644); err != nil {
		applogger.Error("Reflection: failed to write fingerprint file",
			"file", fpFile,
			"error", err,
		)
	}
}

// formatNotesForReflection renders note entries as markdown for the reflection
// LLM prompt. The reflection pipeline uses its own format — full content with
// timestamp and type headers — independent of how other callers format notes.
func formatNotesForReflection(entries []aos.NoteEntry) string {
	if len(entries) == 0 {
		return ""
	}
	parts := make([]string, len(entries))
	for i, e := range entries {
		ts := e.DisplayTimestamp()
		parts[i] = fmt.Sprintf("## [%s] %s\n\n%s", ts, e.Type.String(), e.Content)
	}
	return strings.Join(parts, "\n---\n\n")
}
