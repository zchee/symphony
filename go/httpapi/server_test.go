package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openai/symphony/go/observability"
	"github.com/openai/symphony/go/orchestrator"
	"github.com/openai/symphony/go/runtimeconfig"
)

type staticService struct {
	snapshot    orchestrator.Snapshot
	snapshotErr error
	refresh     observability.RefreshResult
	refreshErr  error
}

func (s staticService) Snapshot(_ time.Duration) (orchestrator.Snapshot, error) {
	if s.snapshotErr != nil {
		return orchestrator.Snapshot{}, s.snapshotErr
	}
	return s.snapshot, nil
}

func (s staticService) RequestRefresh() (observability.RefreshResult, error) {
	if s.refreshErr != nil {
		return observability.RefreshResult{}, s.refreshErr
	}
	return s.refresh, nil
}

func TestAPIStateIssueAndRefresh(t *testing.T) {
	now := time.Date(2026, 3, 10, 3, 0, 0, 0, time.UTC)
	startedAt := now.Add(-42 * time.Second)
	writeServerWorkflow(t, filepath.Join(t.TempDir(), "workspaces"))

	service := staticService{
		snapshot: orchestrator.Snapshot{
			Running: []orchestrator.RunningSnapshot{
				{
					IssueID:           "issue-http",
					Identifier:        "MT-HTTP",
					State:             "In Progress",
					SessionID:         "thread-http",
					TurnCount:         7,
					LastCodexEvent:    "notification",
					LastCodexMessage:  "rendered",
					StartedAt:         startedAt,
					CodexInputTokens:  4,
					CodexOutputTokens: 8,
					CodexTotalTokens:  12,
				},
			},
			Retrying: []orchestrator.RetrySnapshot{
				{IssueID: "issue-retry", Identifier: "MT-RETRY", Attempt: 2, DueInMS: 2_000, Error: "boom"},
			},
			CodexTotals: orchestrator.TokenTotals{InputTokens: 4, OutputTokens: 8, TotalTokens: 12, SecondsRunning: 42},
			RateLimits:  map[string]any{"primary": map[string]any{"remaining": 11}},
		},
		refresh: observability.RefreshResult{
			Queued:      true,
			Coalesced:   false,
			RequestedAt: now,
			Operations:  []string{"poll", "reconcile"},
		},
	}

	handler := Handler(service, 50*time.Millisecond)

	statePayload := decodeJSON(t, performRequest(handler, http.MethodGet, "/api/v1/state"))
	if statePayload["counts"].(map[string]any)["running"] != float64(1) {
		t.Fatalf("state counts = %#v, want running 1", statePayload["counts"])
	}

	issuePayload := decodeJSON(t, performRequest(handler, http.MethodGet, "/api/v1/MT-HTTP"))
	if issuePayload["status"] != "running" || issuePayload["issue_id"] != "issue-http" {
		t.Fatalf("issue payload = %#v, want running issue-http", issuePayload)
	}

	refreshPayload := decodeJSON(t, performRequest(handler, http.MethodPost, "/api/v1/refresh"))
	if refreshPayload["queued"] != true || refreshPayload["coalesced"] != false {
		t.Fatalf("refresh payload = %#v, want queued/coalesced flags", refreshPayload)
	}
}

func TestAPIErrorModesAndMethodConstraints(t *testing.T) {
	handler := Handler(staticService{
		snapshotErr: observability.ErrSnapshotUnavailable,
		refreshErr:  observability.ErrRefreshUnavailable,
	}, 1*time.Millisecond)

	assertStatusAndCode(t, performRequest(handler, http.MethodPost, "/api/v1/state"), http.StatusMethodNotAllowed, "method_not_allowed")
	assertStatusAndCode(t, performRequest(handler, http.MethodGet, "/api/v1/refresh"), http.StatusMethodNotAllowed, "method_not_allowed")
	assertStatusAndCode(t, performRequest(handler, http.MethodPost, "/"), http.StatusMethodNotAllowed, "method_not_allowed")
	assertStatusAndCode(t, performRequest(handler, http.MethodPost, "/api/v1/MT-1"), http.StatusMethodNotAllowed, "method_not_allowed")
	assertStatusAndCode(t, performRequest(handler, http.MethodGet, "/unknown"), http.StatusNotFound, "not_found")

	statePayload := decodeJSON(t, performRequest(handler, http.MethodGet, "/api/v1/state"))
	if statePayload["error"].(map[string]any)["code"] != "snapshot_unavailable" {
		t.Fatalf("state error payload = %#v, want snapshot_unavailable", statePayload["error"])
	}
	assertStatusAndCode(t, performRequest(handler, http.MethodPost, "/api/v1/refresh"), http.StatusServiceUnavailable, "orchestrator_unavailable")
}

func TestAPISnapshotTimeout(t *testing.T) {
	handler := Handler(staticService{
		snapshotErr: observability.ErrSnapshotTimeout,
	}, 1*time.Millisecond)

	payload := decodeJSON(t, performRequest(handler, http.MethodGet, "/api/v1/state"))
	if payload["error"].(map[string]any)["code"] != "snapshot_timeout" {
		t.Fatalf("state timeout payload = %#v, want snapshot_timeout", payload["error"])
	}
}

func TestRootDashboardHTML(t *testing.T) {
	service := staticService{
		snapshot: orchestrator.Snapshot{
			Running: []orchestrator.RunningSnapshot{
				{
					IssueID:           "issue-http",
					Identifier:        "MT-HTTP",
					State:             "In Progress",
					SessionID:         "thread-http",
					TurnCount:         7,
					LastCodexEvent:    "notification",
					LastCodexMessage:  "rendered",
					StartedAt:         time.Date(2026, 3, 10, 3, 0, 0, 0, time.UTC),
					CodexInputTokens:  4,
					CodexOutputTokens: 8,
					CodexTotalTokens:  12,
				},
			},
			CodexTotals: orchestrator.TokenTotals{InputTokens: 4, OutputTokens: 8, TotalTokens: 12, SecondsRunning: 42},
			Retrying: []orchestrator.RetrySnapshot{
				{IssueID: "issue-retry", Identifier: "MT-RETRY", Attempt: 2, DueInMS: 2_000, Error: "boom"},
			},
			RateLimits: map[string]any{
				"limit_id": "codex",
				"primary":  map[string]any{"remaining": 11, "limit": 100},
			},
		},
	}
	handler := Handler(service, 10*time.Millisecond)
	rec := performRequest(handler, http.MethodGet, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if body == "" ||
		!contains(body, "Symphony Dashboard") ||
		!contains(body, "/dashboard.css") ||
		!contains(body, "Raw State JSON") ||
		!contains(body, "Queue Refresh") ||
		!contains(body, "Running Sessions") ||
		!contains(body, "Retry Queue") ||
		!contains(body, "MT-HTTP") ||
		!contains(body, "/api/v1/MT-HTTP") ||
		!contains(body, "MT-RETRY") {
		t.Fatalf("GET / body = %q, want richer dashboard content", body)
	}
}

func TestRootDashboardUnavailableState(t *testing.T) {
	handler := Handler(staticService{
		snapshotErr: observability.ErrSnapshotUnavailable,
	}, 10*time.Millisecond)
	rec := performRequest(handler, http.MethodGet, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want 200", rec.Code)
	}
	if body := rec.Body.String(); !contains(body, "Snapshot Unavailable") || !contains(body, "Raw State JSON") {
		t.Fatalf("GET / unavailable body = %q, want unavailable dashboard state", body)
	}
}

func TestDashboardCSSRoute(t *testing.T) {
	handler := Handler(staticService{}, 10*time.Millisecond)
	rec := performRequest(handler, http.MethodGet, "/dashboard.css")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /dashboard.css status = %d, want 200", rec.Code)
	}
	if contentType := rec.Header().Get("Content-Type"); !strings.Contains(contentType, "text/css") {
		t.Fatalf("GET /dashboard.css content-type = %q, want text/css", contentType)
	}
	if body := rec.Body.String(); !contains(body, "--bg:") || !contains(body, ".summary-grid") {
		t.Fatalf("GET /dashboard.css body = %q, want dashboard stylesheet", body)
	}
}

func writeServerWorkflow(t *testing.T, workspaceRoot string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "WORKFLOW.md")
	content := "---\ntracker:\n  kind: \"linear\"\n  api_key: \"token\"\n  project_slug: \"project\"\nworkspace:\n  root: \"" + workspaceRoot + "\"\n---\nPrompt\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("os.WriteFile(%q) failed: %v", path, err)
	}
	if err := runtimeconfig.SetWorkflowFilePath(path); err != nil {
		t.Fatalf("runtimeconfig.SetWorkflowFilePath(%q) failed: %v", path, err)
	}
}

func performRequest(handler http.Handler, method, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func decodeJSON(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal(%q) failed: %v", rec.Body.String(), err)
	}
	return payload
}

func assertStatusAndCode(t *testing.T, rec *httptest.ResponseRecorder, wantStatus int, wantCode string) {
	t.Helper()
	if rec.Code != wantStatus {
		t.Fatalf("status = %d, want %d body=%q", rec.Code, wantStatus, rec.Body.String())
	}
	payload := decodeJSON(t, rec)
	if payload["error"].(map[string]any)["code"] != wantCode {
		t.Fatalf("error payload = %#v, want code %q", payload["error"], wantCode)
	}
}

func contains(value, needle string) bool {
	return strings.Contains(value, needle)
}
