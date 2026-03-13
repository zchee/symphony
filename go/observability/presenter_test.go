package observability

import (
	"errors"
	"testing"
	"time"

	"github.com/openai/symphony/go/orchestrator"
)

type staticSnapshotSource struct {
	snapshot orchestrator.Snapshot
	err      error
}

func (s staticSnapshotSource) Snapshot(_ time.Duration) (orchestrator.Snapshot, error) {
	if s.err != nil {
		return orchestrator.Snapshot{}, s.err
	}
	return s.snapshot, nil
}

type staticRefreshSource struct {
	result RefreshResult
	err    error
}

func (s staticRefreshSource) RequestRefresh() (RefreshResult, error) {
	if s.err != nil {
		return RefreshResult{}, s.err
	}
	return s.result, nil
}

func TestStatePayload(t *testing.T) {
	now := time.Date(2026, 3, 10, 2, 0, 0, 0, time.UTC)
	startedAt := now.Add(-42 * time.Second)
	snapshot := orchestrator.Snapshot{
		Running: []orchestrator.RunningSnapshot{
			{
				IssueID:       "issue-http",
				Identifier:    "MT-HTTP",
				State:         "In Progress",
				WorkerHost:    "worker-a",
				WorkspacePath: "/remote/workspaces/MT-HTTP",
				SessionID:     "thread-http",

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
			{
				IssueID:       "issue-retry",
				Identifier:    "MT-RETRY",
				Attempt:       2,
				DueInMS:       2_000,
				Error:         "boom",
				WorkerHost:    "worker-b",
				WorkspacePath: "/remote/workspaces/MT-RETRY",
			},
		},
		CodexTotals: orchestrator.TokenTotals{InputTokens: 4, OutputTokens: 8, TotalTokens: 12, SecondsRunning: 42},
		RateLimits:  map[string]any{"primary": map[string]any{"remaining": 11}},
	}

	payload := StatePayload(staticSnapshotSource{snapshot: snapshot}, 50*time.Millisecond, now)

	if counts := payload["counts"].(map[string]any); counts["running"] != 1 || counts["retrying"] != 1 {
		t.Fatalf("counts = %#v, want running=1 retrying=1", counts)
	}
	running := payload["running"].([]map[string]any)
	if len(running) != 1 || running[0]["issue_identifier"] != "MT-HTTP" || running[0]["last_message"] != "rendered" {
		t.Fatalf("running = %#v, want MT-HTTP rendered row", running)
	}
	if running[0]["worker_host"] != "worker-a" || running[0]["workspace_path"] != "/remote/workspaces/MT-HTTP" {
		t.Fatalf("running worker metadata = %#v, want worker-a and remote workspace path", running[0])
	}
	retrying := payload["retrying"].([]map[string]any)
	if len(retrying) != 1 || retrying[0]["issue_identifier"] != "MT-RETRY" {
		t.Fatalf("retrying = %#v, want MT-RETRY row", retrying)
	}
	if retrying[0]["worker_host"] != "worker-b" || retrying[0]["workspace_path"] != "/remote/workspaces/MT-RETRY" {
		t.Fatalf("retrying worker metadata = %#v, want worker-b and remote workspace path", retrying[0])
	}
}

func TestStatePayloadErrorModes(t *testing.T) {
	now := time.Date(2026, 3, 10, 2, 0, 0, 0, time.UTC)
	timeoutPayload := StatePayload(staticSnapshotSource{err: ErrSnapshotTimeout}, 10*time.Millisecond, now)
	if code := timeoutPayload["error"].(map[string]any)["code"]; code != "snapshot_timeout" {
		t.Fatalf("timeout error code = %#v, want snapshot_timeout", code)
	}

	unavailablePayload := StatePayload(staticSnapshotSource{err: ErrSnapshotUnavailable}, 10*time.Millisecond, now)
	if code := unavailablePayload["error"].(map[string]any)["code"]; code != "snapshot_unavailable" {
		t.Fatalf("unavailable error code = %#v, want snapshot_unavailable", code)
	}
}

func TestIssuePayload(t *testing.T) {
	now := time.Date(2026, 3, 10, 2, 0, 0, 0, time.UTC)
	startedAt := now.Add(-1 * time.Minute)
	lastEventAt := now.Add(-30 * time.Second)
	snapshot := orchestrator.Snapshot{
		Running: []orchestrator.RunningSnapshot{
			{
				IssueID:       "issue-http",
				Identifier:    "MT-HTTP",
				State:         "In Progress",
				WorkerHost:    "worker-a",
				WorkspacePath: "/remote/workspaces/MT-HTTP",
				SessionID:     "thread-http",

				TurnCount:          7,
				LastCodexEvent:     "notification",
				LastCodexMessage:   "rendered",
				StartedAt:          startedAt,
				LastCodexTimestamp: &lastEventAt,
				CodexInputTokens:   4,
				CodexOutputTokens:  8,
				CodexTotalTokens:   12,
			},
		},
	}

	payload, err := IssuePayload("MT-HTTP", staticSnapshotSource{snapshot: snapshot}, 50*time.Millisecond, now)
	if err != nil {
		t.Fatalf("IssuePayload() returned error: %v", err)
	}
	if payload["status"] != "running" || payload["issue_id"] != "issue-http" {
		t.Fatalf("payload = %#v, want running issue payload", payload)
	}
	workspace := payload["workspace"].(map[string]any)
	workspacePath := workspace["path"].(string)
	if workspacePath != "/remote/workspaces/MT-HTTP" || workspace["host"] != "worker-a" {
		t.Fatalf("workspace payload = %#v, want remote path and worker-a host", workspace)
	}
	if payload["last_error"] != nil {
		t.Fatalf("last_error = %#v, want nil", payload["last_error"])
	}
}

func TestIssuePayloadRetryingAndNotFound(t *testing.T) {
	now := time.Date(2026, 3, 10, 2, 0, 0, 0, time.UTC)
	snapshot := orchestrator.Snapshot{
		Retrying: []orchestrator.RetrySnapshot{
			{IssueID: "issue-retry", Identifier: "MT-RETRY", Attempt: 2, DueInMS: 5_000, Error: "boom"},
		},
	}

	payload, err := IssuePayload("MT-RETRY", staticSnapshotSource{snapshot: snapshot}, 50*time.Millisecond, now)
	if err != nil {
		t.Fatalf("IssuePayload() returned error: %v", err)
	}
	if payload["status"] != "retrying" {
		t.Fatalf("payload.status = %#v, want retrying", payload["status"])
	}
	if payload["retry"].(map[string]any)["attempt"] != 2 {
		t.Fatalf("retry payload = %#v, want attempt 2", payload["retry"])
	}

	if _, err := IssuePayload("MT-MISSING", staticSnapshotSource{snapshot: snapshot}, 50*time.Millisecond, now); !errors.Is(err, ErrIssueNotFound) {
		t.Fatalf("IssuePayload(missing) error = %v, want ErrIssueNotFound", err)
	}
}

func TestRefreshPayload(t *testing.T) {
	now := time.Date(2026, 3, 10, 2, 0, 0, 0, time.UTC)
	payload, err := RefreshPayload(staticRefreshSource{
		result: RefreshResult{
			Queued:      true,
			Coalesced:   false,
			RequestedAt: now,
			Operations:  []string{"poll", "reconcile"},
		},
	})
	if err != nil {
		t.Fatalf("RefreshPayload() returned error: %v", err)
	}
	if payload["queued"] != true || payload["coalesced"] != false {
		t.Fatalf("payload = %#v, want queued/coalesced flags", payload)
	}
	if operations := payload["operations"].([]string); len(operations) != 2 || operations[0] != "poll" {
		t.Fatalf("operations = %#v, want poll/reconcile", operations)
	}

	if _, err := RefreshPayload(staticRefreshSource{err: ErrRefreshUnavailable}); !errors.Is(err, ErrRefreshUnavailable) {
		t.Fatalf("RefreshPayload(unavailable) error = %v, want ErrRefreshUnavailable", err)
	}
}
