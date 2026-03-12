package httpui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openai/symphony/go/internal/orchestrator"
	"github.com/openai/symphony/go/internal/runtimeconfig"
	"github.com/openai/symphony/go/internal/workflow"
)

func TestRenderRootLiveSnapshot(t *testing.T) {
	writeHTTPUIWorkflow(t)

	now := time.Date(2026, 3, 11, 9, 0, 0, 0, time.UTC)
	nextPoll := int64(2_000)
	startedAt := now.Add(-45 * time.Second)
	lastEventAt := now.Add(-3 * time.Second)

	page, err := RenderRoot(&orchestrator.Snapshot{
		Running: []orchestrator.RunningSnapshot{
			{
				IssueID:            "issue-1",
				Identifier:         "MT-101",
				State:              "In Progress",
				SessionID:          "thread-live",
				CodexAppServerPID:  "4242",
				CodexTotalTokens:   128,
				TurnCount:          3,
				StartedAt:          startedAt,
				LastCodexTimestamp: &lastEventAt,
				LastCodexMessage:   map[string]any{"event": "session_started", "session_id": "thread-live"},
			},
		},
		Retrying: []orchestrator.RetrySnapshot{
			{IssueID: "issue-2", Identifier: "MT-102", Attempt: 2, DueInMS: 1_250, Error: "boom"},
		},
		CodexTotals: orchestrator.TokenTotals{TotalTokens: 128, SecondsRunning: 45},
		RateLimits: map[string]any{
			"limit_id": "codex",
			"primary":  map[string]any{"remaining": 11, "limit": 100},
		},
		Polling: orchestrator.PollingSnapshot{NextPollInMS: &nextPoll},
	}, nil, now)
	if err != nil {
		t.Fatalf("RenderRoot(live snapshot) failed: %v", err)
	}

	html := string(page)
	if !strings.Contains(html, "Symphony Dashboard") ||
		!strings.Contains(html, "MT-101") ||
		!strings.Contains(html, "/api/v1/MT-101") ||
		!strings.Contains(html, "Retry Queue") ||
		!strings.Contains(html, "Queue Refresh") {
		t.Fatalf("RenderRoot(live snapshot) = %q, want dashboard shell with issue links and sections", html)
	}
}

func TestRenderRootUnavailableSnapshot(t *testing.T) {
	writeHTTPUIWorkflow(t)

	page, err := RenderRoot(nil, errUnavailable{}, time.Date(2026, 3, 11, 9, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("RenderRoot(unavailable) failed: %v", err)
	}
	html := string(page)
	if !strings.Contains(html, "Snapshot Unavailable") || !strings.Contains(html, "Raw State JSON") {
		t.Fatalf("RenderRoot(unavailable) = %q, want unavailable state with API link", html)
	}
}

func TestStylesheetIncludesDashboardSelectors(t *testing.T) {
	css := Stylesheet()
	if !strings.Contains(css, ".summary-grid") || !strings.Contains(css, ".panel") || !strings.Contains(css, "--accent:") {
		t.Fatalf("Stylesheet() = %q, want dashboard selectors and variables", css)
	}
}

type errUnavailable struct{}

func (errUnavailable) Error() string { return "snapshot unavailable" }

func writeHTTPUIWorkflow(t *testing.T) {
	t.Helper()
	workflow.ResetDefaultStoreForTest()
	dir := t.TempDir()
	path := filepath.Join(dir, "WORKFLOW.md")
	content := "---\ntracker:\n  kind: \"linear\"\n  api_key: \"token\"\n  project_slug: \"project\"\n---\nPrompt\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("os.WriteFile(%q) failed: %v", path, err)
	}
	if err := runtimeconfig.SetWorkflowFilePath(path); err != nil {
		t.Fatalf("runtimeconfig.SetWorkflowFilePath(%q) failed: %v", path, err)
	}
}
