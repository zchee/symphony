package orchestrator

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openai/symphony/go/domain"
	"github.com/openai/symphony/go/runtimeconfig"
)

func TestSortIssuesForDispatchOrdersByPriorityThenOldestCreatedAt(t *testing.T) {
	older := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)

	highOld := domain.Issue{ID: "issue-old-high", Identifier: "MT-200", Title: "Old high", State: "Todo", Priority: intPtr(1), CreatedAt: &older, AssignedToWorker: true}
	highNew := domain.Issue{ID: "issue-new-high", Identifier: "MT-201", Title: "New high", State: "Todo", Priority: intPtr(1), CreatedAt: &newer, AssignedToWorker: true}
	lowOld := domain.Issue{ID: "issue-old-low", Identifier: "MT-199", Title: "Old low", State: "Todo", Priority: intPtr(2), CreatedAt: &older, AssignedToWorker: true}

	sorted := SortIssuesForDispatch([]domain.Issue{lowOld, highNew, highOld})
	if sorted[0].Identifier != "MT-200" || sorted[1].Identifier != "MT-201" || sorted[2].Identifier != "MT-199" {
		t.Fatalf("sorted identifiers = %#v, want MT-200, MT-201, MT-199", identifiers(sorted))
	}
}

func TestShouldDispatchIssueHonorsBlockersAssignmentAndStateLimits(t *testing.T) {
	writeOrchestratorWorkflow(t, map[string]any{
		"tracker": map[string]any{
			"kind":            "linear",
			"api_key":         "token",
			"project_slug":    "project",
			"active_states":   []any{"Todo", "In Progress"},
			"terminal_states": []any{"Closed", "Cancelled", "Canceled", "Duplicate"},
		},
		"agent": map[string]any{
			"max_concurrent_agents": 3,
		},
	})

	state := NewState(time.Now())
	state.MaxConcurrentAgents = 3

	blocked := domain.Issue{
		ID:               "blocked-1",
		Identifier:       "MT-1001",
		Title:            "Blocked work",
		State:            "Todo",
		BlockedBy:        []domain.BlockerRef{{ID: "blocker-1", Identifier: "MT-1002", State: "In Progress"}},
		AssignedToWorker: true,
	}
	if ShouldDispatchIssue(blocked, state) {
		t.Fatal("ShouldDispatchIssue(blocked) = true, want false")
	}

	assignedAway := domain.Issue{
		ID:               "assigned-away-1",
		Identifier:       "MT-1007",
		Title:            "Owned elsewhere",
		State:            "Todo",
		AssignedToWorker: false,
		AssigneeID:       "user-2",
	}
	if ShouldDispatchIssue(assignedAway, state) {
		t.Fatal("ShouldDispatchIssue(assignedAway) = true, want false")
	}

	ready := domain.Issue{
		ID:               "ready-1",
		Identifier:       "MT-1003",
		Title:            "Ready work",
		State:            "Todo",
		BlockedBy:        []domain.BlockerRef{{ID: "blocker-2", Identifier: "MT-1004", State: "Closed"}},
		AssignedToWorker: true,
	}
	if !ShouldDispatchIssue(ready, state) {
		t.Fatal("ShouldDispatchIssue(ready) = false, want true")
	}
}

func TestRevalidateIssueForDispatchSkipsWhenStaleBlockerAppears(t *testing.T) {
	writeOrchestratorWorkflow(t, nil)

	stale := domain.Issue{
		ID:               "blocked-2",
		Identifier:       "MT-1005",
		Title:            "Stale blocked work",
		State:            "Todo",
		AssignedToWorker: true,
	}
	refreshed := domain.Issue{
		ID:               "blocked-2",
		Identifier:       "MT-1005",
		Title:            "Stale blocked work",
		State:            "Todo",
		BlockedBy:        []domain.BlockerRef{{ID: "blocker-3", Identifier: "MT-1006", State: "In Progress"}},
		AssignedToWorker: true,
	}

	issue, skipped, err := RevalidateIssueForDispatch(stale, func(ids []string) ([]domain.Issue, error) {
		if len(ids) != 1 || ids[0] != "blocked-2" {
			t.Fatalf("issue fetch ids = %#v, want [blocked-2]", ids)
		}
		return []domain.Issue{refreshed}, nil
	})
	if err != nil {
		t.Fatalf("RevalidateIssueForDispatch() returned error: %v", err)
	}
	if !skipped {
		t.Fatal("RevalidateIssueForDispatch() skipped = false, want true")
	}
	if issue.Identifier != "MT-1005" || len(issue.BlockedBy) != 1 {
		t.Fatalf("revalidated issue = %#v, want refreshed blocked issue", issue)
	}
}

func TestReconcileIssueStatesStopsOrRefreshesRunningEntries(t *testing.T) {
	testRoot := t.TempDir()
	workspaceRoot := filepath.Join(testRoot, "workspaces")
	nonTerminalWorkspace := filepath.Join(workspaceRoot, "MT-555")
	terminalWorkspace := filepath.Join(workspaceRoot, "MT-556")
	mustMkdirAll(t, nonTerminalWorkspace)
	mustMkdirAll(t, terminalWorkspace)

	writeOrchestratorWorkflow(t, map[string]any{
		"workspace": map[string]any{
			"root": workspaceRoot,
		},
		"tracker": map[string]any{
			"kind":            "linear",
			"api_key":         "token",
			"project_slug":    "project",
			"active_states":   []any{"Todo", "In Progress", "In Review"},
			"terminal_states": []any{"Closed", "Cancelled", "Canceled", "Duplicate"},
		},
	})

	stopped := map[string]int{}
	state := NewState(time.Now())
	state.Running["issue-1"] = RunningEntry{
		Identifier: "MT-555",
		Issue:      domain.Issue{ID: "issue-1", Identifier: "MT-555", State: "Todo", Title: "Queued"},
		StartedAt:  time.Now(),
		Stop:       func() { stopped["issue-1"]++ },
	}
	state.Running["issue-2"] = RunningEntry{
		Identifier: "MT-556",
		Issue:      domain.Issue{ID: "issue-2", Identifier: "MT-556", State: "In Progress", Title: "Done soon"},
		StartedAt:  time.Now(),
		Stop:       func() { stopped["issue-2"]++ },
	}
	state.Running["issue-3"] = RunningEntry{
		Identifier: "MT-557",
		Issue:      domain.Issue{ID: "issue-3", Identifier: "MT-557", State: "Todo", Title: "Active"},
		StartedAt:  time.Now(),
		Stop:       func() { stopped["issue-3"]++ },
	}
	state.Claimed["issue-1"] = struct{}{}
	state.Claimed["issue-2"] = struct{}{}
	state.Claimed["issue-3"] = struct{}{}

	state = ReconcileIssueStates([]domain.Issue{
		{ID: "issue-1", Identifier: "MT-555", State: "Backlog", Title: "Queued"},
		{ID: "issue-2", Identifier: "MT-556", State: "Closed", Title: "Done"},
		{ID: "issue-3", Identifier: "MT-557", State: "In Progress", Title: "Active", AssignedToWorker: true},
	}, state)

	if _, ok := state.Running["issue-1"]; ok {
		t.Fatal("issue-1 still running after non-active reconcile")
	}
	if _, ok := state.Claimed["issue-1"]; ok {
		t.Fatal("issue-1 still claimed after non-active reconcile")
	}
	if _, err := os.Stat(nonTerminalWorkspace); err != nil {
		t.Fatalf("non-terminal workspace removed unexpectedly: %v", err)
	}
	if stopped["issue-1"] != 1 {
		t.Fatalf("issue-1 stop count = %d, want 1", stopped["issue-1"])
	}

	if _, ok := state.Running["issue-2"]; ok {
		t.Fatal("issue-2 still running after terminal reconcile")
	}
	if _, err := os.Stat(terminalWorkspace); !os.IsNotExist(err) {
		t.Fatalf("terminal workspace still exists, err=%v", err)
	}
	if stopped["issue-2"] != 1 {
		t.Fatalf("issue-2 stop count = %d, want 1", stopped["issue-2"])
	}

	if entry, ok := state.Running["issue-3"]; !ok || entry.Issue.State != "In Progress" {
		t.Fatalf("issue-3 running entry = %#v, want refreshed active issue", entry)
	}
}

func TestHandleWorkerExitSchedulesContinuationAndBackoffRetries(t *testing.T) {
	now := time.Now()
	state := NewState(now)
	state.Running["issue-resume"] = RunningEntry{
		Identifier: "MT-558",
		Issue:      domain.Issue{ID: "issue-resume", Identifier: "MT-558", State: "In Progress", Title: "Resume"},
		StartedAt:  now.Add(-5 * time.Second),
	}
	state.Claimed["issue-resume"] = struct{}{}

	state = HandleWorkerExit(state, "issue-resume", ErrWorkerNormal, now)
	retry := state.RetryAttempts["issue-resume"]
	if retry.Attempt != 1 {
		t.Fatalf("continuation retry attempt = %d, want 1", retry.Attempt)
	}
	if delta := retry.DueAtMS - now.UnixMilli(); delta < 500 || delta > 1_100 {
		t.Fatalf("continuation retry delta = %dms, want roughly 1000ms", delta)
	}
	if _, ok := state.Completed["issue-resume"]; !ok {
		t.Fatal("issue-resume not marked completed after normal exit")
	}

	state = NewState(now)
	state.Running["issue-crash"] = RunningEntry{
		Identifier:   "MT-559",
		Issue:        domain.Issue{ID: "issue-crash", Identifier: "MT-559", State: "In Progress", Title: "Crash"},
		RetryAttempt: 2,
		StartedAt:    now.Add(-5 * time.Second),
	}
	state.Claimed["issue-crash"] = struct{}{}

	state = HandleWorkerExit(state, "issue-crash", errors.New("boom"), now)
	retry = state.RetryAttempts["issue-crash"]
	if retry.Attempt != 3 || retry.Error != "agent exited: boom" {
		t.Fatalf("crash retry = %#v, want attempt 3 with boom message", retry)
	}
	if delta := retry.DueAtMS - now.UnixMilli(); delta < 39_500 || delta > 40_500 {
		t.Fatalf("crash retry delta = %dms, want roughly 40000ms", delta)
	}
}

func TestApplyCodexUpdateAndSnapshotTrackSessionTokensAndRateLimits(t *testing.T) {
	now := time.Date(2026, 3, 10, 1, 30, 0, 0, time.UTC)
	state := NewState(now)
	state.Running["issue-snapshot"] = RunningEntry{
		Identifier: "MT-188",
		Issue:      domain.Issue{ID: "issue-snapshot", Identifier: "MT-188", State: "In Progress", Title: "Snapshot test"},
		StartedAt:  now.Add(-75 * time.Second),
	}
	state.Claimed["issue-snapshot"] = struct{}{}

	state = ApplyCodexUpdate(state, "issue-snapshot", CodexUpdate{
		Event:     "session_started",
		SessionID: "thread-live-turn-live",
		Timestamp: now,
	})
	state = ApplyCodexUpdate(state, "issue-snapshot", CodexUpdate{
		Event:             "notification",
		CodexAppServerPID: "4242",
		Timestamp:         now,
		Payload: map[string]any{
			"method": "thread/tokenUsage/updated",
			"params": map[string]any{
				"tokenUsage": map[string]any{
					"total": map[string]any{
						"inputTokens":  12,
						"outputTokens": 4,
						"totalTokens":  16,
					},
				},
			},
			"rate_limits": map[string]any{
				"limit_id": "codex",
				"primary":  map[string]any{"remaining": 90, "limit": 100},
			},
		},
	})

	snapshot := SnapshotState(state, now)
	if len(snapshot.Running) != 1 {
		t.Fatalf("len(snapshot.Running) = %d, want 1", len(snapshot.Running))
	}
	entry := snapshot.Running[0]
	if entry.SessionID != "thread-live-turn-live" || entry.TurnCount != 1 {
		t.Fatalf("snapshot running entry = %#v, want session id and turn count", entry)
	}
	if entry.CodexAppServerPID != "4242" || entry.CodexInputTokens != 12 || entry.CodexOutputTokens != 4 || entry.CodexTotalTokens != 16 {
		t.Fatalf("snapshot token entry = %#v, want pid 4242 and 12/4/16 tokens", entry)
	}
	if snapshot.RateLimits["limit_id"] != "codex" {
		t.Fatalf("snapshot rate limits = %#v, want codex limit map", snapshot.RateLimits)
	}

	state = HandleWorkerExit(state, "issue-snapshot", ErrWorkerNormal, now)
	if state.CodexTotals.InputTokens != 12 || state.CodexTotals.OutputTokens != 4 || state.CodexTotals.TotalTokens != 16 {
		t.Fatalf("state.CodexTotals = %#v, want 12/4/16", state.CodexTotals)
	}
	if state.CodexTotals.SecondsRunning < 70 {
		t.Fatalf("state.CodexTotals.SecondsRunning = %d, want >= 70", state.CodexTotals.SecondsRunning)
	}
}

func TestApplyCodexUpdateTracksTurnCompletedUsageWhenPresent(t *testing.T) {
	now := time.Date(2026, 3, 10, 1, 35, 0, 0, time.UTC)
	state := NewState(now)
	state.Running["issue-turn-completed-usage"] = RunningEntry{
		Identifier: "MT-202",
		Issue:      domain.Issue{ID: "issue-turn-completed-usage", Identifier: "MT-202", State: "In Progress", Title: "Turn completed usage"},
		StartedAt:  now.Add(-30 * time.Second),
	}
	state.Claimed["issue-turn-completed-usage"] = struct{}{}

	state = ApplyCodexUpdate(state, "issue-turn-completed-usage", CodexUpdate{
		Event:     "turn_completed",
		Timestamp: now,
		Payload: map[string]any{
			"method": "turn/completed",
			"params": map[string]any{
				"turn": map[string]any{"status": "completed"},
				"usage": map[string]any{
					"input_tokens":  "12",
					"output_tokens": 4,
					"total_tokens":  16,
				},
			},
		},
	})

	snapshot := SnapshotState(state, now)
	if len(snapshot.Running) != 1 {
		t.Fatalf("len(snapshot.Running) = %d, want 1", len(snapshot.Running))
	}
	entry := snapshot.Running[0]
	if entry.CodexInputTokens != 12 || entry.CodexOutputTokens != 4 || entry.CodexTotalTokens != 16 {
		t.Fatalf("snapshot token entry = %#v, want 12/4/16", entry)
	}

	state = HandleWorkerExit(state, "issue-turn-completed-usage", ErrWorkerNormal, now)
	if state.CodexTotals.InputTokens != 12 || state.CodexTotals.OutputTokens != 4 || state.CodexTotals.TotalTokens != 16 {
		t.Fatalf("state.CodexTotals = %#v, want 12/4/16", state.CodexTotals)
	}
}

func TestApplyCodexUpdateTracksNestedRateLimitsAndPromptCompletionUsage(t *testing.T) {
	now := time.Date(2026, 3, 10, 1, 40, 0, 0, time.UTC)
	state := NewState(now)
	state.Running["issue-token-count"] = RunningEntry{
		Identifier: "MT-221",
		Issue:      domain.Issue{ID: "issue-token-count", Identifier: "MT-221", State: "In Progress", Title: "Token count"},
		StartedAt:  now.Add(-45 * time.Second),
	}
	state.Claimed["issue-token-count"] = struct{}{}

	rateLimits := map[string]any{
		"limit_id":  "codex",
		"primary":   map[string]any{"remaining": 90, "limit": 100},
		"secondary": nil,
		"credits":   map[string]any{"has_credits": false, "unlimited": false},
	}

	state = ApplyCodexUpdate(state, "issue-token-count", CodexUpdate{
		Event:     "notification",
		Timestamp: now,
		Payload: map[string]any{
			"method": "codex/event/token_count",
			"params": map[string]any{
				"msg": map[string]any{
					"type": "event_msg",
					"payload": map[string]any{
						"type": "token_count",
						"info": map[string]any{
							"total_token_usage": map[string]any{
								"prompt_tokens":     10,
								"completion_tokens": 5,
								"total_tokens":      15,
							},
						},
						"rate_limits": rateLimits,
					},
				},
			},
		},
	})

	snapshot := SnapshotState(state, now)
	if len(snapshot.Running) != 1 {
		t.Fatalf("len(snapshot.Running) = %d, want 1", len(snapshot.Running))
	}
	entry := snapshot.Running[0]
	if entry.CodexInputTokens != 10 || entry.CodexOutputTokens != 5 || entry.CodexTotalTokens != 15 {
		t.Fatalf("snapshot token entry = %#v, want 10/5/15", entry)
	}
	if snapshot.RateLimits["limit_id"] != "codex" {
		t.Fatalf("snapshot rate limits = %#v, want nested codex rate limits", snapshot.RateLimits)
	}
}

func TestSelectWorkerHostHonorsPerHostCapacityAndPreferredHost(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "WORKFLOW.md")
	workflowSource := `---
tracker:
  kind: "linear"
  api_key: "token"
  project_slug: "project"
workspace:
  root: "` + filepath.Join(dir, "workspaces") + `"
agent:
  max_concurrent_agents: 10
worker:
  ssh_hosts:
    - "worker-a"
    - "worker-b"
  max_concurrent_agents_per_host: 1
codex:
  command: "codex app-server"
---
Prompt
`
	if err := os.WriteFile(path, []byte(workflowSource), 0o644); err != nil {
		t.Fatalf("os.WriteFile(%q) failed: %v", path, err)
	}
	if err := runtimeconfig.SetWorkflowFilePath(path); err != nil {
		t.Fatalf("runtimeconfig.SetWorkflowFilePath(%q) failed: %v", path, err)
	}

	state := NewState(time.Now())
	state.Running["issue-1"] = RunningEntry{WorkerHost: "worker-a"}
	if host, err := SelectWorkerHost(state, ""); err != nil || host != "worker-b" {
		t.Fatalf("SelectWorkerHost() = %q, %v, want worker-b", host, err)
	}
	if host, err := SelectWorkerHost(state, "worker-b"); err != nil || host != "worker-b" {
		t.Fatalf("SelectWorkerHost(preferred) = %q, %v, want worker-b", host, err)
	}

	state.Running["issue-2"] = RunningEntry{WorkerHost: "worker-b"}
	if _, err := SelectWorkerHost(state, ""); !errors.Is(err, ErrNoWorkerCapacity) {
		t.Fatalf("SelectWorkerHost(full) error = %v, want ErrNoWorkerCapacity", err)
	}
}

func writeOrchestratorWorkflow(t *testing.T, overrides map[string]any) {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "WORKFLOW.md")
	workspaceRoot := filepath.Join(dir, "workspaces")
	if value, ok := nestedStringOverride(overrides, "workspace", "root"); ok {
		workspaceRoot = value
	}

	lines := []string{
		"---",
		"tracker:",
		`  kind: "linear"`,
		`  api_key: "token"`,
		`  project_slug: "project"`,
		"workspace:",
		`  root: "` + workspaceRoot + `"`,
		"agent:",
		`  max_concurrent_agents: 10`,
		"codex:",
		`  command: "codex app-server"`,
	}
	if overrides != nil {
		if tracker, ok := overrides["tracker"].(map[string]any); ok {
			if active, ok := tracker["active_states"].([]any); ok {
				lines = append(lines, "  # tracker overrides")
				lines = append(lines[:2], lines[2:]...)
				_ = active
			}
		}
	}
	if tracker, ok := overrides["tracker"].(map[string]any); ok {
		lines = []string{"---", "tracker:"}
		lines = append(lines, `  kind: "`+stringOr(tracker["kind"], "linear")+`"`)
		lines = append(lines, `  api_key: "`+stringOr(tracker["api_key"], "token")+`"`)
		lines = append(lines, `  project_slug: "`+stringOr(tracker["project_slug"], "project")+`"`)
		if active, ok := tracker["active_states"].([]any); ok {
			lines = append(lines, "  active_states: ["+joinYAML(active)+"]")
		}
		if terminal, ok := tracker["terminal_states"].([]any); ok {
			lines = append(lines, "  terminal_states: ["+joinYAML(terminal)+"]")
		}
		lines = append(lines, "workspace:", `  root: "`+workspaceRoot+`"`)
	} else {
		lines = []string{
			"---",
			"tracker:",
			`  kind: "linear"`,
			`  api_key: "token"`,
			`  project_slug: "project"`,
			"workspace:",
			`  root: "` + workspaceRoot + `"`,
		}
	}
	if agent, ok := overrides["agent"].(map[string]any); ok {
		lines = append(lines, "agent:")
		lines = append(lines, `  max_concurrent_agents: `+stringOr(agent["max_concurrent_agents"], "10"))
	} else {
		lines = append(lines, "agent:", `  max_concurrent_agents: 10`)
	}
	lines = append(lines, "codex:", `  command: "codex app-server"`, "---", "Prompt")

	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(%q) failed: %v", path, err)
	}
	if err := runtimeconfig.SetWorkflowFilePath(path); err != nil {
		t.Fatalf("runtimeconfig.SetWorkflowFilePath(%q) failed: %v", path, err)
	}
}

func joinYAML(values []any) string {
	parts := make([]string, len(values))
	for i, value := range values {
		parts[i] = `"` + value.(string) + `"`
	}
	return strings.Join(parts, ", ")
}

func nestedStringOverride(root map[string]any, path ...string) (string, bool) {
	current := any(root)
	for _, segment := range path {
		node, ok := current.(map[string]any)
		if !ok {
			return "", false
		}
		current, ok = node[segment]
		if !ok {
			return "", false
		}
	}
	raw, ok := current.(string)
	return raw, ok
}

func stringOr(value any, fallback string) string {
	raw, ok := value.(string)
	if !ok || raw == "" {
		return fallback
	}
	return raw
}

func mustMkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("os.MkdirAll(%q) failed: %v", path, err)
	}
}

func intPtr(value int) *int {
	result := value
	return &result
}

func identifiers(issues []domain.Issue) []string {
	values := make([]string, len(issues))
	for i, issue := range issues {
		values[i] = issue.Identifier
	}
	return values
}
