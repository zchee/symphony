package prompt

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openai/symphony/go/runtimeconfig"
	"github.com/openai/symphony/go/workflow"
)

type issueFixture struct {
	Identifier  string    `json:"identifier"`
	Title       string    `json:"title"`
	Description *string   `json:"description"`
	State       string    `json:"state"`
	URL         string    `json:"url"`
	Labels      []any     `json:"labels"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func TestBuildRendersIssueAndAttemptValues(t *testing.T) {
	writePromptWorkflow(t, "Ticket {{ issue.identifier }} {{ issue.title }} labels={{ issue.labels }} attempt={{ attempt }}")

	description := "Replace transport layer"
	issue := issueFixture{
		Identifier:  "S-1",
		Title:       "Refactor backend request path",
		Description: &description,
		State:       "Todo",
		URL:         "https://example.org/issues/S-1",
		Labels:      []any{"backend"},
	}
	attempt := 3

	rendered, err := Build(issue, &attempt)
	if err != nil {
		t.Fatalf("Build() returned error: %v", err)
	}

	if !strings.Contains(rendered, "Ticket S-1 Refactor backend request path") {
		t.Fatalf("Build() = %q, missing identifier/title", rendered)
	}
	if !strings.Contains(rendered, "labels=backend") {
		t.Fatalf("Build() = %q, missing labels output", rendered)
	}
	if !strings.Contains(rendered, "attempt=3") {
		t.Fatalf("Build() = %q, missing attempt output", rendered)
	}
}

func TestBuildRendersDatetimeFields(t *testing.T) {
	writePromptWorkflow(t, "Ticket {{ issue.identifier }} created={{ issue.created_at }} updated={{ issue.updated_at }}")

	description := "Prompt should serialize datetimes"
	issue := issueFixture{
		Identifier:  "MT-697",
		Title:       "Live smoke",
		Description: &description,
		State:       "Todo",
		URL:         "https://example.org/issues/MT-697",
		Labels:      []any{},
		CreatedAt:   time.Date(2026, 2, 26, 18, 6, 48, 0, time.UTC),
		UpdatedAt:   time.Date(2026, 2, 26, 18, 7, 3, 0, time.UTC),
	}

	rendered, err := Build(issue, nil)
	if err != nil {
		t.Fatalf("Build() returned error: %v", err)
	}

	if !strings.Contains(rendered, "created=2026-02-26T18:06:48Z") {
		t.Fatalf("Build() = %q, missing created_at rendering", rendered)
	}
	if !strings.Contains(rendered, "updated=2026-02-26T18:07:03Z") {
		t.Fatalf("Build() = %q, missing updated_at rendering", rendered)
	}
}

func TestBuildUsesStrictVariableRendering(t *testing.T) {
	writePromptWorkflow(t, "Work on ticket {{ missing.ticket_id }} and follow these steps.")

	description := "Reproduce and fix"
	issue := issueFixture{
		Identifier:  "MT-123",
		Title:       "Investigate broken sync",
		Description: &description,
		State:       "In Progress",
		URL:         "https://example.org/issues/MT-123",
		Labels:      []any{"bug"},
	}

	_, err := Build(issue, nil)
	var renderErr *RenderError
	if !errors.As(err, &renderErr) {
		t.Fatalf("Build() error = %v, want RenderError", err)
	}
}

func TestBuildSurfacesInvalidTemplateContentWithPromptContext(t *testing.T) {
	writePromptWorkflow(t, "{% if issue.identifier %}")

	description := "Invalid template syntax"
	issue := issueFixture{
		Identifier:  "MT-999",
		Title:       "Broken prompt",
		Description: &description,
		State:       "Todo",
		URL:         "https://example.org/issues/MT-999",
	}

	_, err := Build(issue, nil)
	var parseErr *ParseError
	if !errors.As(err, &parseErr) {
		t.Fatalf("Build() error = %v, want ParseError", err)
	}
	if !strings.Contains(parseErr.Error(), `template="{% if issue.identifier %}"`) {
		t.Fatalf("ParseError = %q, missing template context", parseErr.Error())
	}
}

func TestBuildUsesDefaultTemplateWhenWorkflowPromptIsBlank(t *testing.T) {
	writePromptWorkflow(t, "   \n")

	description := "Include enough issue context to start working."
	issue := issueFixture{
		Identifier:  "MT-777",
		Title:       "Make fallback prompt useful",
		Description: &description,
		State:       "In Progress",
		URL:         "https://example.org/issues/MT-777",
		Labels:      []any{"prompt"},
	}

	rendered, err := Build(issue, nil)
	if err != nil {
		t.Fatalf("Build() returned error: %v", err)
	}

	assertContainsAll(t, rendered,
		"You are working on a Linear issue.",
		"Identifier: MT-777",
		"Title: Make fallback prompt useful",
		"Body:",
		"Include enough issue context to start working.",
	)
}

func TestBuildDefaultTemplateHandlesMissingIssueBody(t *testing.T) {
	writePromptWorkflow(t, "")

	issue := issueFixture{
		Identifier:  "MT-778",
		Title:       "Handle empty body",
		Description: nil,
		State:       "Todo",
		URL:         "https://example.org/issues/MT-778",
	}

	rendered, err := Build(issue, nil)
	if err != nil {
		t.Fatalf("Build() returned error: %v", err)
	}

	assertContainsAll(t, rendered,
		"Identifier: MT-778",
		"Title: Handle empty body",
		"No description provided.",
	)
}

func TestBuildReportsWorkflowLoadFailuresSeparately(t *testing.T) {
	workflow.ResetDefaultStoreForTest()
	missingPath := filepath.Join(t.TempDir(), "missing-workflow.md")
	if err := runtimeconfig.SetWorkflowFilePath(missingPath); err != nil {
		t.Fatalf("runtimeconfig.SetWorkflowFilePath(%q) failed: %v", missingPath, err)
	}

	description := "Missing workflow file"
	issue := issueFixture{
		Identifier:  "MT-780",
		Title:       "Workflow unavailable",
		Description: &description,
		State:       "Todo",
		URL:         "https://example.org/issues/MT-780",
	}

	_, err := Build(issue, nil)
	var workflowErr *WorkflowUnavailableError
	if !errors.As(err, &workflowErr) {
		t.Fatalf("Build() error = %v, want WorkflowUnavailableError", err)
	}
}

func TestBuildRendersCheckedInWorkflowTemplate(t *testing.T) {
	workflow.ResetDefaultStoreForTest()
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("filepath.Abs(repo root) failed: %v", err)
	}
	workflowPath := filepath.Join(repoRoot, "elixir", "WORKFLOW.md")
	if err := runtimeconfig.SetWorkflowFilePath(workflowPath); err != nil {
		t.Fatalf("runtimeconfig.SetWorkflowFilePath(%q) failed: %v", workflowPath, err)
	}

	description := "Render with rich template variables"
	issue := issueFixture{
		Identifier:  "MT-616",
		Title:       "Use rich templates for WORKFLOW.md",
		Description: &description,
		State:       "In Progress",
		URL:         "https://example.org/issues/MT-616/use-rich-templates-for-workflowmd",
		Labels:      []any{"templating", "workflow"},
	}
	attempt := 2

	rendered, err := Build(issue, &attempt)
	if err != nil {
		t.Fatalf("Build() returned error: %v", err)
	}

	assertContainsAll(t, rendered,
		"You are working on a Linear ticket `MT-616`",
		"Issue context:",
		"Identifier: MT-616",
		"Title: Use rich templates for WORKFLOW.md",
		"Current status: In Progress",
		"https://example.org/issues/MT-616/use-rich-templates-for-workflowmd",
		"This is an unattended orchestration session.",
		"Only stop early for a true blocker",
		`Do not include "next steps for user"`,
		"open and follow `.codex/skills/land/SKILL.md`",
		"Do not call `gh pr merge` directly",
		"Continuation context:",
		"retry attempt #2",
	)
}

func TestBuildAddsContinuationGuidanceForRetries(t *testing.T) {
	writePromptWorkflow(t, "{% if attempt %}Retry #{{ attempt }}{% endif %}")

	description := "Retry flow"
	issue := issueFixture{
		Identifier:  "MT-201",
		Title:       "Continue autonomous ticket",
		Description: &description,
		State:       "In Progress",
		URL:         "https://example.org/issues/MT-201",
	}
	attempt := 2

	rendered, err := Build(issue, &attempt)
	if err != nil {
		t.Fatalf("Build() returned error: %v", err)
	}
	if rendered != "Retry #2" {
		t.Fatalf("Build() = %q, want %q", rendered, "Retry #2")
	}
}

func TestBuildNormalizesNestedDateLikeValuesWithoutCrashing(t *testing.T) {
	writePromptWorkflow(t, "Ticket {{ issue.identifier }}")

	description := "Prompt builder should normalize nested terms"
	issue := issueFixture{
		Identifier:  "MT-701",
		Title:       "Serialize nested values",
		Description: &description,
		State:       "Todo",
		URL:         "https://example.org/issues/MT-701",
		Labels: []any{
			time.Date(2026, 2, 27, 12, 34, 56, 0, time.UTC),
			map[string]any{"phase": "test"},
			struct {
				URL string `json:"url"`
			}{URL: "https://example.org/issues/MT-701"},
		},
	}

	rendered, err := Build(issue, nil)
	if err != nil {
		t.Fatalf("Build() returned error: %v", err)
	}
	if rendered != "Ticket MT-701" {
		t.Fatalf("Build() = %q, want %q", rendered, "Ticket MT-701")
	}
}

func writePromptWorkflow(t *testing.T, prompt string) {
	t.Helper()
	workflow.ResetDefaultStoreForTest()

	dir := t.TempDir()
	path := filepath.Join(dir, "WORKFLOW.md")
	content := "---\ntracker:\n  kind: linear\n  project_slug: \"project\"\n  api_key: \"token\"\n---\n" + prompt
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("os.WriteFile(%q) failed: %v", path, err)
	}

	if err := runtimeconfig.SetWorkflowFilePath(path); err != nil {
		t.Fatalf("runtimeconfig.SetWorkflowFilePath(%q) failed: %v", path, err)
	}
}

func assertContainsAll(t *testing.T, rendered string, want ...string) {
	t.Helper()

	for _, fragment := range want {
		if !strings.Contains(rendered, fragment) {
			t.Fatalf("rendered prompt = %q, missing fragment %q", rendered, fragment)
		}
	}
}
