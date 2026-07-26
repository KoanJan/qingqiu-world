package experience

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/dops"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/llm"
)

// IngestSkillParams carries the raw external skill content to be refined.
type IngestSkillParams struct {
	FileName   string // Skill file name
	RawContent string // Raw SKILL.md content
}

// LineRange represents a contiguous range of lines from the original skill file.
// Both Start and End are 1-based and inclusive.
type LineRange struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// SectionRef represents either a direct line-number reference to the original
// skill content or LLM-generated text (fallback).
//
//   - PRESERVE: LineRange is non-nil, Content is empty → the original lines are used verbatim.
//   - SUPPLEMENT: LineRange is non-nil, Content is non-empty → the original lines are used
//     with the supplement appended by resolveSectionRef.
//   - GENERATE: LineRange is nil, Content is non-empty → LLM-generated text is used.
//   - EMPTY: both are empty → the field has no content.
type SectionRef struct {
	Content   string     `json:"content"`
	LineRange *LineRange `json:"line_range,omitempty"`
}

// ingestOutput is the structured output from the LLM during skill ingestion.
//
// NOTE: Title and Description are RETAINED as fallback fields. Under normal
// operation they are ignored in favour of the YAML frontmatter values. They
// are only consumed when frontmatter parsing fails (see finalizePublicExperience).
type ingestOutput struct {
	Title       string     `json:"title" jsonschema:"description=Transferable lesson stated as a general principle (FALLBACK — only used when YAML frontmatter parsing fails)"`
	Description string     `json:"description" jsonschema:"description=One sentence stating what this teaches (FALLBACK — only used when YAML frontmatter parsing fails)"`
	WhenToUse   SectionRef `json:"when_to_use" jsonschema:"description=What task signatures, trigger phrases, or problem patterns indicate this experience applies. Each on its own line."`
	Guidelines  SectionRef `json:"guidelines" jsonschema:"description=Actionable advice with rationale. What to do, why, and in what order. Decision heuristics, sequencing rules, proven patterns."`
	Pitfalls    SectionRef `json:"pitfalls" jsonschema:"description=Known failure modes. What can go wrong, early warning signs, and how to prevent or recover."`
	Procedure   SectionRef `json:"procedure" jsonschema:"description=Numbered steps. Only include if a repeatable, cross-project workflow emerged. Leave empty if none."`
	Skip        bool       `json:"skip" jsonschema:"description=Set to true if nothing worth extracting (e.g., pure reference with no structural pattern)"`
}

// processingLocks prevents concurrent distillation of the same uploaded skill.
// Keys are uploaded_skill IDs. This is a runtime-only lock — the DB has no
// status field on UploadedSkill, so the lock is lost on process restart
// (which is acceptable: a restart means no goroutine is running).
var processingLocks sync.Map // key: int64, value: struct{}

// IngestSkill creates an UploadedSkill record and a pre-written PublicExperience
// (Status=Generating, empty content), then kicks off background LLM distillation.
// Returns immediately so the frontend can show the new record in the list.
func IngestSkill(ctx context.Context, params IngestSkillParams) (*model.UploadedSkill, error) {
	panicIfNotReady()

	fm, err := ExtractSkillFrontmatter(params.RawContent)
	var title, desc string
	if err != nil {
		applogger.Warn("IngestSkill: failed to extract YAML frontmatter, will use fallback title",
			"error", err,
		)
	} else if fm != nil {
		title = fm.Name
		desc = fm.Description
	}
	if title == "" {
		if len(params.RawContent) > 100 {
			title = params.RawContent[:100]
		} else {
			title = params.RawContent
		}
	}

	uploaded := &model.UploadedSkill{
		FileName:   params.FileName,
		Title:      title,
		RawContent: params.RawContent,
	}
	if err := database.DB.Create(uploaded).Error; err != nil {
		return nil, fmt.Errorf("save uploaded skill: %w", err)
	}

	exp := &model.PublicExperience{
		Title:       title,
		Description: desc,
		SourceType:  model.PublicExperienceSourceIngestion,
		SourceID:    uploaded.ID,
		Status:      model.PublicExperienceStatusGenerating,
	}
	if err := database.DB.Create(exp).Error; err != nil {
		applogger.Error("IngestSkill: failed to pre-write public experience",
			"uploaded_skill_id", uploaded.ID,
			"error", err,
		)
		return nil, fmt.Errorf("pre-write public experience: %w", err)
	}

	applogger.Info("IngestSkill: submitted for async processing",
		"uploaded_skill_id", uploaded.ID,
		"public_experience_id", exp.ID,
		"file_name", params.FileName,
	)

	go processIngestion(*uploaded, exp.ID)
	return uploaded, nil
}

// RedistillPublicExperience re-triggers LLM distillation for an existing public
// experience that is in Error (or stuck in Generating) state. Resets the record
// to Generating with empty content, then starts a background goroutine.
func RedistillPublicExperience(ctx context.Context, expID int64) error {
	panicIfNotReady()

	var exp model.PublicExperience
	if err := database.DB.First(&exp, expID).Error; err != nil {
		return fmt.Errorf("public experience not found: %w", err)
	}

	if exp.SourceType != model.PublicExperienceSourceIngestion {
		return fmt.Errorf("only ingestion-sourced experiences can be re-distilled")
	}

	var uploaded model.UploadedSkill
	if err := database.DB.First(&uploaded, exp.SourceID).Error; err != nil {
		return fmt.Errorf("uploaded skill not found: %w", err)
	}

	fm, err := ExtractSkillFrontmatter(uploaded.RawContent)
	var title, description string
	if err != nil {
		applogger.Warn("RedistillPublicExperience: failed to extract YAML frontmatter, will use fallback title",
			"error", err,
		)
	} else if fm != nil {
		title = fm.Name
		description = fm.Description
	}
	if title == "" {
		title = uploaded.Title
	}

	if err := database.DB.Model(&exp).Updates(map[string]interface{}{
		"title":       title,
		"description": description,
		"when_to_use": "",
		"guidelines":  "",
		"pitfalls":    "",
		"procedure":   "",
		"status":      model.PublicExperienceStatusGenerating,
	}).Error; err != nil {
		return fmt.Errorf("reset public experience: %w", err)
	}

	applogger.Info("RedistillPublicExperience: re-distilling",
		"public_experience_id", expID,
		"uploaded_skill_id", uploaded.ID,
	)

	go processIngestion(uploaded, expID)
	return nil
}

// processIngestion runs the LLM distillation and updates the pre-written
// PublicExperience. Runs in a goroutine. Uses a runtime sync.Map lock to
// prevent concurrent processing of the same uploaded skill.
func processIngestion(uploaded model.UploadedSkill, expID int64) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	if _, loaded := processingLocks.LoadOrStore(uploaded.ID, struct{}{}); loaded {
		applogger.Info("processIngestion: already in progress",
			"uploaded_skill_id", uploaded.ID,
		)
		return
	}
	defer processingLocks.Delete(uploaded.ID)

	sysCfg := dops.GetSystemLLMConfig()
	if sysCfg == nil {
		applogger.Error("processIngestion: system LLM config not configured",
			"uploaded_skill_id", uploaded.ID,
		)
		markPublicExperienceError(expID)
		return
	}

	chatModel := llm.NewChatModelWithTemperature(
		sysCfg.BaseURL, sysCfg.APIKey, sysCfg.ModelID, llm.TemperatureControlled,
	)

	schema := llm.GenerateSchema[ingestOutput]()

	fm, parseErr := ExtractSkillFrontmatter(uploaded.RawContent)
	if parseErr != nil {
		applogger.Warn("processIngestion: YAML frontmatter parse failed, will use LLM fallback for title/description",
			"uploaded_skill_id", uploaded.ID,
			"error", parseErr,
		)
	}

	lineNumberedContent := FormatRawWithLineNumbers(uploaded.RawContent)
	prompt := buildIngestionPrompt(lineNumberedContent)

	messages := []llm.Message{
		{Role: "user", Content: prompt},
	}

	schemaDef := llm.JSONSchemaDefinition{
		Name:   "ingest_output",
		Schema: schema,
		Strict: true,
	}

	response, err := chatModel.ChatWithJSONSchema(ctx, messages, schemaDef)
	if err != nil {
		applogger.Error("processIngestion: LLM call failed",
			"uploaded_skill_id", uploaded.ID,
			"public_experience_id", expID,
			"error", err,
		)
		markPublicExperienceError(expID)
		return
	}

	var output ingestOutput
	if err := json.Unmarshal([]byte(response), &output); err != nil {
		applogger.Error("processIngestion: parse LLM output failed",
			"uploaded_skill_id", uploaded.ID,
			"public_experience_id", expID,
			"error", err,
		)
		markPublicExperienceError(expID)
		return
	}

	if output.Skip {
		applogger.Info("processIngestion: nothing worth extracting, deleting pre-written record",
			"uploaded_skill_id", uploaded.ID,
			"public_experience_id", expID,
			"file_name", uploaded.FileName,
		)
		deletePublicExperience(expID)
		return
	}

	if output.WhenToUse.LineRange == nil && output.WhenToUse.Content == "" &&
		output.Guidelines.LineRange == nil && output.Guidelines.Content == "" &&
		output.Pitfalls.LineRange == nil && output.Pitfalls.Content == "" &&
		output.Procedure.LineRange == nil && output.Procedure.Content == "" {
		applogger.Info("processIngestion: LLM returned no content for any section, deleting pre-written record",
			"uploaded_skill_id", uploaded.ID,
			"public_experience_id", expID,
		)
		deletePublicExperience(expID)
		return
	}

	var fmName string
	if fm != nil {
		fmName = fm.Name
	}
	if fmName == "" && (output.Title == "" || output.Description == "") {
		applogger.Error("processIngestion: no frontmatter and LLM output missing required fields",
			"uploaded_skill_id", uploaded.ID,
			"public_experience_id", expID,
		)
		markPublicExperienceError(expID)
		return
	}

	if err := finalizePublicExperience(ctx, expID, fm, output, uploaded.RawContent); err != nil {
		applogger.Error("processIngestion: finalize failed",
			"uploaded_skill_id", uploaded.ID,
			"public_experience_id", expID,
			"error", err,
		)
		markPublicExperienceError(expID)
		return
	}

	applogger.Info("processIngestion: public experience finalized",
		"uploaded_skill_id", uploaded.ID,
		"public_experience_id", expID,
		"file_name", uploaded.FileName,
	)
}

// buildIngestionPrompt constructs the LLM prompt for skill ingestion.
func buildIngestionPrompt(lineNumberedContent string) string {
	var sb strings.Builder
	sb.WriteString(`Refine an external skill document into a host-agnostic experience record.

The content below comes from a skill file with LINE NUMBERS. Each line is prefixed with its 1-based line number.

## Output Format

For each field (when_to_use, guidelines, pitfalls, procedure), you must decide:

1. **PRESERVE (preferred)**: If the original skill has a section whose semantic meaning matches the field,
   set ` + "`line_range`" + ` with the exact start/end line numbers and leave ` + "`content`" + ` empty.
   - For example, if lines 10-25 are titled "When to use this skill", set when_to_use.line_range to {start: 10, end: 25}.
   - Only include the content lines — exclude the section heading itself.

2. **SUPPLEMENT**: If the original section is mostly correct but needs minor supplements (e.g.,
   adding a note about a failure mode), set ` + "`line_range`" + ` to reference the original
   AND put the supplement in ` + "`content`" + `.

3. **GENERATE (fallback)**: If no matching section exists in the original, set ` + "`line_range`" + ` to null
   and provide the ` + "`content`" + ` yourself.

4. **DECOUPLE**: When referencing original content, mentally strip host-environment coupling
   (system-specific tool names, internal APIs, config paths). If the referenced lines contain
   significant host coupling that a simple line reference would carry forward, use SUPPLEMENT
   mode instead — point to the lines AND note what to strip in ` + "`content`" + `.

## Classification rules

1. **Method skills** — teach a workflow, decision framework, or technique.
   - Keep: actionable advice, decision heuristics, workflow steps, failure modes, trigger conditions,
     and concrete technical details (domain APIs like Canvas/fillText, library names,
     function signatures, algorithm steps).
   - Discard: host-environment coupling (system-specific tools like write_notes/wake_me_when,
     internal APIs, system config, script paths like start.sh/stop.sh), instructions for humans.

2. **Reference skills** — primarily list specific values (color codes, font names, API endpoints,
   file paths, company/product names, environment-specific strings).
   - Extract the structural pattern only — not the values.
   - Use placeholder notation like <primary-color>, <heading-font>, <api-base-url>
     when describing the pattern.
   - If no structural pattern remains after removing values, output empty fields and set skip=true.

## Fields

title: If the skill file has a YAML frontmatter with a ` + "`name`" + ` field, copy that value directly — do not modify. If no frontmatter or no ` + "`name`" + ` field exists, generate a short, transferable principle name derived from the skill's core method.
description: If the skill file has a YAML frontmatter with a ` + "`description`" + ` field, copy that value directly — do not modify. If no frontmatter or no ` + "`description`" + ` field exists, generate one sentence stating the core skill insight — used for semantic matching.

when_to_use: What task signatures, trigger phrases, or problem patterns indicate this skill applies.
guidelines: Actionable advice with rationale. What to do, why, and in what order.
pitfalls: Known failure modes. What can go wrong, early warning signs, and how to prevent or recover.
procedure: Numbered steps. Only include if a repeatable workflow is described.
  Leave line_range null and content empty if none.

skip: Set to true ONLY if the entire skill contains nothing transferable.

## Skill content (with line numbers)

`)
	sb.WriteString(lineNumberedContent)
	return sb.String()
}
