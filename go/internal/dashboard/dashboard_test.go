package dashboard

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openai/symphony/go/internal/orchestrator"
	"github.com/openai/symphony/go/internal/runtimeconfig"
)

func TestFormatSnapshotIdleFixture(t *testing.T) {
	writeDashboardWorkflow(t, baseWorkflow())

	snapshot := orchestrator.Snapshot{
		Running:     nil,
		Retrying:    nil,
		CodexTotals: orchestrator.TokenTotals{},
		RateLimits:  nil,
	}

	got := FormatSnapshot(&snapshot, 0.0, 115)
	want := readEvidence(t, "idle.evidence.md")
	if got != want {
		t.Fatalf("FormatSnapshot(idle)\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestFormatSnapshotIdleWithDashboardURLFixture(t *testing.T) {
	workflow := baseWorkflow()
	workflow["server"] = map[string]any{"port": 4000}
	writeDashboardWorkflow(t, workflow)

	snapshot := orchestrator.Snapshot{CodexTotals: orchestrator.TokenTotals{}}
	got := FormatSnapshot(&snapshot, 0.0, 115)
	want := readEvidence(t, "idle_with_dashboard_url.evidence.md")
	if got != want {
		t.Fatalf("FormatSnapshot(idle_with_dashboard_url)\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestFormatSnapshotBackoffQueueFixture(t *testing.T) {
	writeDashboardWorkflow(t, baseWorkflow())

	snapshot := orchestrator.Snapshot{
		Running: []orchestrator.RunningSnapshot{
			{
				Identifier:        "MT-638",
				State:             "retrying",
				SessionID:         "thread-1234567890",
				CodexAppServerPID: "4242",
				CodexTotalTokens:  14200,
				RuntimeSeconds:    1225,
				TurnCount:         7,
				LastCodexMessage: map[string]any{
					"message": map[string]any{
						"method": "codex/event/agent_message_delta",
						"params": map[string]any{
							"msg": map[string]any{
								"payload": map[string]any{"delta": "waiting on rate-limit backoff window"},
							},
						},
					},
				},
			},
		},
		Retrying: []orchestrator.RetrySnapshot{
			{Identifier: "MT-450", Attempt: 4, DueInMS: 1250, Error: "rate limit exhausted"},
			{Identifier: "MT-451", Attempt: 2, DueInMS: 3900, Error: "retrying after API timeout with jitter"},
			{Identifier: "MT-452", Attempt: 6, DueInMS: 8100, Error: "worker crashed\nrestarting cleanly"},
			{Identifier: "MT-453", Attempt: 1, DueInMS: 11000, Error: "fourth queued retry should also render after removing the top-three limit"},
		},
		CodexTotals: orchestrator.TokenTotals{InputTokens: 18000, OutputTokens: 2200, TotalTokens: 20200, SecondsRunning: 2700},
		RateLimits: map[string]any{
			"limit_id":  "gpt-5",
			"primary":   map[string]any{"remaining": 0, "limit": 20000, "reset_in_seconds": 95},
			"secondary": map[string]any{"remaining": 0, "limit": 60, "reset_in_seconds": 45},
			"credits":   map[string]any{"has_credits": false},
		},
	}

	got := FormatSnapshot(&snapshot, 15.4, 115)
	want := readEvidence(t, "backoff_queue.evidence.md")
	if got != want {
		t.Fatalf("FormatSnapshot(backoff_queue)\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestFormatSnapshotSuperBusyFixture(t *testing.T) {
	writeDashboardWorkflow(t, baseWorkflow())

	snapshot := orchestrator.Snapshot{
		Running: []orchestrator.RunningSnapshot{
			{
				Identifier:        "MT-101",
				State:             "running",
				SessionID:         "thread-1234567890",
				CodexAppServerPID: "4242",
				CodexTotalTokens:  120450,
				RuntimeSeconds:    785,
				TurnCount:         11,
				LastCodexMessage: map[string]any{
					"message": map[string]any{
						"method": "turn/completed",
						"params": map[string]any{"turn": map[string]any{"status": "completed"}},
					},
				},
			},
			{
				Identifier:        "MT-102",
				State:             "running",
				SessionID:         "thread-abcdef1234567890",
				CodexAppServerPID: "5252",
				CodexTotalTokens:  89200,
				RuntimeSeconds:    412,
				TurnCount:         4,
				LastCodexMessage: map[string]any{
					"message": map[string]any{
						"method": "codex/event/exec_command_begin",
						"params": map[string]any{"msg": map[string]any{"command": "mix test --cover"}},
					},
				},
			},
		},
		CodexTotals: orchestrator.TokenTotals{InputTokens: 250000, OutputTokens: 18500, TotalTokens: 268500, SecondsRunning: 4321},
		RateLimits: map[string]any{
			"limit_id":  "gpt-5",
			"primary":   map[string]any{"remaining": 12345, "limit": 20000, "reset_in_seconds": 30},
			"secondary": map[string]any{"remaining": 45, "limit": 60, "reset_in_seconds": 12},
			"credits":   map[string]any{"has_credits": true, "balance": 9876.5},
		},
	}

	got := FormatSnapshot(&snapshot, 1842.7, 115)
	want := readEvidence(t, "super_busy.evidence.md")
	if got != want {
		t.Fatalf("FormatSnapshot(super_busy)\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestFormatSnapshotCreditsUnlimitedFixture(t *testing.T) {
	writeDashboardWorkflow(t, baseWorkflow())

	snapshot := orchestrator.Snapshot{
		Running: []orchestrator.RunningSnapshot{
			{
				Identifier:        "MT-777",
				State:             "running",
				SessionID:         "thread-1234567890",
				CodexAppServerPID: "4242",
				CodexTotalTokens:  3200,
				RuntimeSeconds:    75,
				TurnCount:         7,
				LastCodexMessage: map[string]any{
					"message": map[string]any{
						"method": "thread/tokenUsage/updated",
						"params": map[string]any{
							"tokenUsage": map[string]any{"total": map[string]any{"inputTokens": 90, "outputTokens": 12, "totalTokens": 102}},
						},
					},
				},
			},
		},
		CodexTotals: orchestrator.TokenTotals{InputTokens: 90, OutputTokens: 12, TotalTokens: 102, SecondsRunning: 75},
		RateLimits: map[string]any{
			"limit_id":  "priority-tier",
			"primary":   map[string]any{"remaining": 100, "limit": 100, "reset_in_seconds": 1},
			"secondary": map[string]any{"remaining": 500, "limit": 500, "reset_in_seconds": 1},
			"credits":   map[string]any{"unlimited": true},
		},
	}

	got := FormatSnapshot(&snapshot, 42.0, 115)
	want := readEvidence(t, "credits_unlimited.evidence.md")
	if got != want {
		t.Fatalf("FormatSnapshot(credits_unlimited)\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestHumanizeCodexMessageFullEventSet(t *testing.T) {
	eventCases := []struct {
		method  string
		payload map[string]any
		want    string
	}{
		{"turn/started", map[string]any{"params": map[string]any{"turn": map[string]any{"id": "turn-1"}}}, "turn started"},
		{"turn/completed", map[string]any{"params": map[string]any{"turn": map[string]any{"status": "completed"}}}, "turn completed"},
		{"turn/diff/updated", map[string]any{"params": map[string]any{"diff": "line1\nline2"}}, "turn diff updated"},
		{"turn/plan/updated", map[string]any{"params": map[string]any{"plan": []any{map[string]any{"step": "a"}, map[string]any{"step": "b"}}}}, "plan updated"},
		{"thread/tokenUsage/updated", map[string]any{"params": map[string]any{"tokenUsage": map[string]any{"total": map[string]any{"inputTokens": 8, "outputTokens": 3, "totalTokens": 11}}}}, "thread token usage updated"},
		{"item/started", map[string]any{"params": map[string]any{"item": map[string]any{"id": "item-123", "type": "commandExecution", "status": "running"}}}, "item started: command execution"},
		{"item/completed", map[string]any{"params": map[string]any{"item": map[string]any{"type": "fileChange", "status": "completed"}}}, "item completed: file change"},
		{"item/agentMessage/delta", map[string]any{"params": map[string]any{"delta": "hello"}}, "agent message streaming"},
		{"item/plan/delta", map[string]any{"params": map[string]any{"delta": "step"}}, "plan streaming"},
		{"item/reasoning/summaryTextDelta", map[string]any{"params": map[string]any{"summaryText": "thinking"}}, "reasoning summary streaming"},
		{"item/reasoning/summaryPartAdded", map[string]any{"params": map[string]any{"summaryText": "section"}}, "reasoning summary section added"},
		{"item/reasoning/textDelta", map[string]any{"params": map[string]any{"textDelta": "reason"}}, "reasoning text streaming"},
		{"item/commandExecution/outputDelta", map[string]any{"params": map[string]any{"outputDelta": "ok"}}, "command output streaming"},
		{"item/fileChange/outputDelta", map[string]any{"params": map[string]any{"outputDelta": "changed"}}, "file change output streaming"},
		{"item/commandExecution/requestApproval", map[string]any{"params": map[string]any{"parsedCmd": "git status"}}, "command approval requested (git status)"},
		{"item/fileChange/requestApproval", map[string]any{"params": map[string]any{"fileChangeCount": 2}}, "file change approval requested (2 files)"},
		{"item/tool/call", map[string]any{"params": map[string]any{"tool": "linear_graphql"}}, "dynamic tool call requested (linear_graphql)"},
		{"item/tool/requestUserInput", map[string]any{"params": map[string]any{"question": "Continue?"}}, "tool requires user input: Continue?"},
	}

	for _, tc := range eventCases {
		message := map[string]any{
			"event":   "notification",
			"message": map[string]any{"method": tc.method, "params": tc.payload["params"]},
		}
		got := humanizeCodexMessage(message)
		if !strings.Contains(got, tc.want) {
			t.Fatalf("humanizeCodexMessage(%s) = %q, want fragment %q", tc.method, got, tc.want)
		}
	}
}

func TestHumanizeCodexMessageWrapperEvents(t *testing.T) {
	completed := map[string]any{
		"event": "tool_call_completed",
		"payload": map[string]any{
			"method": "item/tool/call",
			"params": map[string]any{"name": "linear_graphql"},
		},
	}
	failed := map[string]any{
		"event": "tool_call_failed",
		"payload": map[string]any{
			"method": "item/tool/call",
			"params": map[string]any{"tool": "linear_graphql"},
		},
	}
	unsupported := map[string]any{
		"event": "unsupported_tool_call",
		"payload": map[string]any{
			"method": "item/tool/call",
			"params": map[string]any{"tool": "unknown_tool"},
		},
	}
	autoApproved := map[string]any{
		"event":    "approval_auto_approved",
		"decision": "acceptForSession",
		"payload": map[string]any{
			"method": "item/commandExecution/requestApproval",
			"params": map[string]any{"parsedCmd": "mix test"},
		},
	}
	autoAnswered := map[string]any{
		"event":  "tool_input_auto_answered",
		"answer": "This is a non-interactive session. Operator input is unavailable.",
		"payload": map[string]any{
			"method": "item/tool/requestUserInput",
			"params": map[string]any{"question": "Continue?"},
		},
	}
	startupFailed := map[string]any{
		"event":  "startup_failed",
		"reason": "port_exit: 7",
	}
	turnEndedWithError := map[string]any{
		"event":      "turn_ended_with_error",
		"session_id": "thread-1-turn-1",
		"reason": map[string]any{
			"method": "turn/failed",
			"params": map[string]any{"error": map[string]any{"message": "boom"}},
		},
	}
	reasoning := map[string]any{
		"event": "notification",
		"message": map[string]any{
			"method": "codex/event/agent_reasoning",
			"params": map[string]any{
				"msg": map[string]any{
					"payload": map[string]any{"summaryText": "compare retry paths for Linear polling"},
				},
			},
		},
	}
	messageDelta := map[string]any{
		"event": "notification",
		"message": map[string]any{
			"method": "codex/event/agent_message_delta",
			"params": map[string]any{
				"msg": map[string]any{
					"payload": map[string]any{"delta": "writing workpad reconciliation update"},
				},
			},
		},
	}
	malformed := map[string]any{
		"event":  "malformed",
		"raw":    "warning: this is stderr noise",
		"stream": "stderr",
	}

	if got := humanizeCodexMessage(completed); !strings.Contains(got, "dynamic tool call completed (linear_graphql)") {
		t.Fatalf("completed humanization = %q", got)
	}
	if got := humanizeCodexMessage(failed); !strings.Contains(got, "dynamic tool call failed (linear_graphql)") {
		t.Fatalf("failed humanization = %q", got)
	}
	if got := humanizeCodexMessage(unsupported); !strings.Contains(got, "unsupported dynamic tool call rejected (unknown_tool)") {
		t.Fatalf("unsupported humanization = %q", got)
	}
	if got := humanizeCodexMessage(autoApproved); !strings.Contains(got, "auto-approved") {
		t.Fatalf("autoApproved humanization = %q", got)
	}
	if got := humanizeCodexMessage(autoAnswered); !strings.Contains(got, "auto-answered") {
		t.Fatalf("autoAnswered humanization = %q", got)
	}
	if got := humanizeCodexMessage(startupFailed); !strings.Contains(got, "startup failed: port_exit: 7") {
		t.Fatalf("startupFailed humanization = %q", got)
	}
	if got := humanizeCodexMessage(turnEndedWithError); !strings.Contains(got, "turn ended with error: turn failed: boom") {
		t.Fatalf("turnEndedWithError humanization = %q", got)
	}
	if got := humanizeCodexMessage(reasoning); !strings.Contains(got, "reasoning update: compare retry paths for Linear polling") {
		t.Fatalf("reasoning humanization = %q", got)
	}
	if got := humanizeCodexMessage(messageDelta); !strings.Contains(got, "agent message streaming: writing workpad reconciliation update") {
		t.Fatalf("messageDelta humanization = %q", got)
	}
	if got := humanizeCodexMessage(malformed); !strings.Contains(got, "malformed JSON event from codex") {
		t.Fatalf("malformed humanization = %q", got)
	}
}

func baseWorkflow() map[string]any {
	return map[string]any{
		"tracker": map[string]any{
			"kind":         "linear",
			"api_key":      "token",
			"project_slug": "project",
		},
	}
}

func writeDashboardWorkflow(t *testing.T, configMap map[string]any) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "WORKFLOW.md")
	lines := []string{"---", "tracker:", `  kind: "linear"`, `  api_key: "token"`, `  project_slug: "project"`}
	if server, ok := configMap["server"].(map[string]any); ok {
		lines = append(lines, "server:")
		if port, ok := server["port"].(int); ok {
			lines = append(lines, fmt.Sprintf("  port: %d", port))
		}
	}
	lines = append(lines, "---", "Prompt")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(%q) failed: %v", path, err)
	}
	if err := runtimeconfig.SetWorkflowFilePath(path); err != nil {
		t.Fatalf("runtimeconfig.SetWorkflowFilePath(%q) failed: %v", path, err)
	}
}

func readEvidence(t *testing.T, name string) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatalf("filepath.Abs(repo root) failed: %v", err)
	}
	path := filepath.Join(root, "elixir", "test", "fixtures", "status_dashboard_snapshots", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("os.ReadFile(%q) failed: %v", path, err)
	}
	text := strings.TrimSpace(string(data))
	text = strings.TrimPrefix(text, "```text\n")
	text = strings.TrimSuffix(text, "\n```")
	return text
}
