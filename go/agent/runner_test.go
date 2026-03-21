package agent

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/openai/symphony/go/domain"
	"github.com/openai/symphony/go/runtimeconfig"
	"github.com/openai/symphony/go/workspace"
)

func TestRunEmitsSessionStartedUpdate(t *testing.T) {
	testRoot := t.TempDir()
	workspaceRoot := filepath.Join(testRoot, "workspaces")
	workspace := filepath.Join(workspaceRoot, "MT-88")
	codexBinary := filepath.Join(testRoot, "fake-codex")
	mustMkdirAll(t, workspace)

	writeExecutable(t, codexBinary, `#!/bin/sh
count=0
while IFS= read -r _line; do
  count=$((count + 1))
  case "$count" in
    1) printf '%s\n' '{"id":1,"result":{}}' ;;
    2) ;;
    3) printf '%s\n' '{"id":2,"result":{"thread":{"id":"thread-live"}}}' ;;
    4)
      printf '%s\n' '{"id":3,"result":{"turn":{"id":"turn-live"}}}'
      printf '%s\n' '{"method":"turn/completed"}'
      exit 0
      ;;
  esac
done
`)

	writeAgentWorkflow(t, workspaceRoot, map[string]any{
		"codex": map[string]any{
			"command": codexBinary + " app-server",
		},
	})

	issue := domain.Issue{
		ID:         "issue-live-updates",
		Identifier: "MT-88",
		Title:      "Session update",
		State:      "In Progress",
	}

	var updates []map[string]any
	logBuffer := captureAgentLogs(t)
	err := Run(issue, Options{
		OnCodexUpdate: func(update map[string]any) {
			updates = append(updates, update)
		},
		IssueStateFetcher: func(_ []string) ([]domain.Issue, error) {
			return []domain.Issue{{ID: issue.ID, Identifier: issue.Identifier, Title: issue.Title, State: "Done"}}, nil
		},
	})
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	if len(updates) != 2 {
		t.Fatalf("len(updates) = %d, want exactly session_started and turn_completed", len(updates))
	}
	if updates[0]["event"] != "session_started" || updates[0]["session_id"] != "thread-live-turn-live" {
		t.Fatalf("updates[0] = %#v, want session_started thread-live-turn-live", updates[0])
	}
	if updates[1]["event"] != "turn_completed" {
		t.Fatalf("updates[1] = %#v, want turn_completed", updates[1])
	}
	logText := logBuffer.String()
	if !strings.Contains(logText, "Starting agent run for issue_id=issue-live-updates issue_identifier=MT-88") {
		t.Fatalf("log missing start message: %q", logText)
	}
	if !strings.Contains(logText, "Completed agent run for issue_id=issue-live-updates issue_identifier=MT-88 session_id=thread-live-turn-live") {
		t.Fatalf("log missing completed message: %q", logText)
	}
}

func TestRunContinuesWhileIssueRemainsActive(t *testing.T) {
	testRoot := t.TempDir()
	templateRepo := filepath.Join(testRoot, "source")
	workspaceRoot := filepath.Join(testRoot, "workspaces")
	codexBinary := filepath.Join(testRoot, "fake-codex")
	traceFile := filepath.Join(testRoot, "codex.trace")

	mustMkdirAll(t, templateRepo)
	mustWriteFile(t, filepath.Join(templateRepo, "README.md"), "# test")
	t.Setenv("SYMP_TEST_CODEX_TRACE", traceFile)

	writeExecutable(t, codexBinary, `#!/bin/sh
trace_file="${SYMP_TEST_CODEX_TRACE:-/tmp/codex.trace}"
printf 'RUN\n' >> "$trace_file"
count=0
while IFS= read -r line; do
  count=$((count + 1))
  printf 'JSON:%s\n' "$line" >> "$trace_file"
  case "$count" in
    1) printf '%s\n' '{"id":1,"result":{}}' ;;
    2) ;;
    3) printf '%s\n' '{"id":2,"result":{"thread":{"id":"thread-cont"}}}' ;;
    4)
      printf '%s\n' '{"id":3,"result":{"turn":{"id":"turn-cont-1"}}}'
      printf '%s\n' '{"method":"turn/completed"}'
      ;;
    5)
      printf '%s\n' '{"id":3,"result":{"turn":{"id":"turn-cont-2"}}}'
      printf '%s\n' '{"method":"turn/completed"}'
      exit 0
      ;;
  esac
done
`)

	writeAgentWorkflow(t, workspaceRoot, map[string]any{
		"hooks": map[string]any{
			"after_create": "cp " + shellQuote(filepath.Join(templateRepo, "README.md")) + " README.md",
		},
		"codex": map[string]any{
			"command": codexBinary + " app-server",
		},
		"agent": map[string]any{
			"max_turns": 3,
		},
		"prompt": "You are an agent for this repository.",
	})

	issue := domain.Issue{
		ID:         "issue-continue",
		Identifier: "MT-247",
		Title:      "Continue until done",
		State:      "In Progress",
	}

	fetchCalls := 0
	logBuffer := captureAgentLogs(t)
	err := Run(issue, Options{
		IssueStateFetcher: func(_ []string) ([]domain.Issue, error) {
			fetchCalls++
			state := "Done"
			if fetchCalls == 1 {
				state = "In Progress"
			}
			return []domain.Issue{{ID: issue.ID, Identifier: issue.Identifier, Title: issue.Title, State: state}}, nil
		},
	})
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}
	if fetchCalls != 2 {
		t.Fatalf("fetchCalls = %d, want 2", fetchCalls)
	}

	trace := string(mustReadFile(t, traceFile))
	if count := strings.Count(trace, `"method":"thread/start"`); count != 1 {
		t.Fatalf("thread/start count = %d, want 1 trace=%q", count, trace)
	}
	if count := strings.Count(trace, `"method":"turn/start"`); count != 2 {
		t.Fatalf("turn/start count = %d, want 2 trace=%q", count, trace)
	}
	if !strings.Contains(trace, "You are an agent for this repository.") {
		t.Fatalf("trace missing first-turn prompt: %q", trace)
	}
	if !strings.Contains(trace, "Continuation guidance:") || !strings.Contains(trace, "continuation turn #2 of 3") {
		t.Fatalf("trace missing continuation prompt: %q", trace)
	}
	if !strings.Contains(logBuffer.String(), "Continuing agent run for issue_id=issue-continue issue_identifier=MT-247 after normal turn completion turn=1/3") {
		t.Fatalf("log missing continuation message: %q", logBuffer.String())
	}
}

func TestRunStopsAtMaxTurns(t *testing.T) {
	testRoot := t.TempDir()
	templateRepo := filepath.Join(testRoot, "source")
	workspaceRoot := filepath.Join(testRoot, "workspaces")
	codexBinary := filepath.Join(testRoot, "fake-codex")
	traceFile := filepath.Join(testRoot, "codex.trace")

	mustMkdirAll(t, templateRepo)
	mustWriteFile(t, filepath.Join(templateRepo, "README.md"), "# test")
	t.Setenv("SYMP_TEST_CODEX_TRACE", traceFile)

	writeExecutable(t, codexBinary, `#!/bin/sh
trace_file="${SYMP_TEST_CODEX_TRACE:-/tmp/codex.trace}"
printf 'RUN\n' >> "$trace_file"
count=0
while IFS= read -r line; do
  count=$((count + 1))
  printf 'JSON:%s\n' "$line" >> "$trace_file"
  case "$count" in
    1) printf '%s\n' '{"id":1,"result":{}}' ;;
    2) ;;
    3) printf '%s\n' '{"id":2,"result":{"thread":{"id":"thread-max"}}}' ;;
    4)
      printf '%s\n' '{"id":3,"result":{"turn":{"id":"turn-max-1"}}}'
      printf '%s\n' '{"method":"turn/completed"}'
      ;;
    5)
      printf '%s\n' '{"id":3,"result":{"turn":{"id":"turn-max-2"}}}'
      printf '%s\n' '{"method":"turn/completed"}'
      exit 0
      ;;
  esac
done
`)

	writeAgentWorkflow(t, workspaceRoot, map[string]any{
		"hooks": map[string]any{
			"after_create": "cp " + shellQuote(filepath.Join(templateRepo, "README.md")) + " README.md",
		},
		"codex": map[string]any{
			"command": codexBinary + " app-server",
		},
		"agent": map[string]any{
			"max_turns": 2,
		},
		"prompt": "You are an agent for this repository.",
	})

	issue := domain.Issue{
		ID:         "issue-max-turns",
		Identifier: "MT-248",
		Title:      "Stop at max turns",
		State:      "In Progress",
	}

	logBuffer := captureAgentLogs(t)
	err := Run(issue, Options{
		IssueStateFetcher: func(_ []string) ([]domain.Issue, error) {
			return []domain.Issue{{ID: issue.ID, Identifier: issue.Identifier, Title: issue.Title, State: "In Progress"}}, nil
		},
	})
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	trace := string(mustReadFile(t, traceFile))
	if count := strings.Count(trace, `"method":"turn/start"`); count != 2 {
		t.Fatalf("turn/start count = %d, want 2 trace=%q", count, trace)
	}
	if !strings.Contains(logBuffer.String(), "Reached agent.max_turns for issue_id=issue-max-turns issue_identifier=MT-248 with issue still active; returning control to orchestrator") {
		t.Fatalf("log missing max-turns message: %q", logBuffer.String())
	}
}

func TestSelectedWorkerHostPrefersExplicitHostThenFirstConfiguredHost(t *testing.T) {
	testCases := []struct {
		name       string
		preferred  string
		configured []string
		want       string
	}{
		{
			name: "local when no host is configured",
			want: "",
		},
		{
			name:       "preferred host wins",
			preferred:  "worker-b",
			configured: []string{"worker-a", "worker-c"},
			want:       "worker-b",
		},
		{
			name:       "first configured non-empty host is selected",
			configured: []string{"", " worker-a ", "worker-b"},
			want:       "worker-a",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := selectedWorkerHost(testCase.preferred, testCase.configured); got != testCase.want {
				t.Fatalf("selectedWorkerHost(%q, %#v) = %q, want %q", testCase.preferred, testCase.configured, got, testCase.want)
			}
		})
	}
}

func TestRunSurfacesSSHStartupFailuresWithoutFailingOverHosts(t *testing.T) {
	testRoot := t.TempDir()
	traceFile := filepath.Join(testRoot, "ssh.trace")
	fakeSSH := filepath.Join(testRoot, "ssh")
	t.Setenv("PATH", testRoot+":"+os.Getenv("PATH"))
	t.Setenv("SYMP_TEST_SSH_TRACE", traceFile)

	writeExecutable(t, fakeSSH, `#!/bin/sh
trace_file="${SYMP_TEST_SSH_TRACE:-/tmp/symphony-fake-ssh.trace}"
printf 'ARGV:%s\n' "$*" >> "$trace_file"

case "$*" in
  *worker-a*"__SYMPHONY_WORKSPACE__"*)
    printf '%s\n' 'worker-a prepare failed' >&2
    exit 75
    ;;
  *worker-b*"__SYMPHONY_WORKSPACE__"*)
    printf '%s\t%s\t%s\n' '__SYMPHONY_WORKSPACE__' '1' '/remote/home/.symphony-remote-workspaces/MT-SSH-FAILOVER'
    exit 0
    ;;
  *)
    exit 0
    ;;
esac
`)

	writeAgentWorkflow(t, "~/.symphony-remote-workspaces", map[string]any{
		"worker": map[string]any{
			"ssh_hosts": []string{"worker-a", "worker-b"},
		},
	})

	issue := domain.Issue{
		ID:         "issue-ssh-failover",
		Identifier: "MT-SSH-FAILOVER",
		Title:      "Do not fail over within one worker run",
		State:      "In Progress",
	}

	err := Run(issue, Options{WorkerHost: "worker-a"})
	if err == nil {
		t.Fatal("Run() error = nil, want remote startup failure")
	}

	var hookErr *workspace.WorkspaceHookFailedError
	if !errors.As(err, &hookErr) {
		t.Fatalf("Run() error = %v, want WorkspaceHookFailedError", err)
	}
	if hookErr.Hook != "remote_prepare" || hookErr.Status != 75 {
		t.Fatalf("workspace hook error = %#v, want remote_prepare status 75", hookErr)
	}

	trace := string(mustReadFile(t, traceFile))
	if !strings.Contains(trace, "worker-a bash -lc") {
		t.Fatalf("trace missing worker-a prepare command: %q", trace)
	}
	if strings.Contains(trace, "worker-b bash -lc") {
		t.Fatalf("trace unexpectedly retried worker-b: %q", trace)
	}
}

func TestRunLogsFailure(t *testing.T) {
	testRoot := t.TempDir()
	workspaceRoot := filepath.Join(testRoot, "workspaces")
	workspace := filepath.Join(workspaceRoot, "MT-249")
	codexBinary := filepath.Join(testRoot, "fake-codex")
	mustMkdirAll(t, workspace)

	writeExecutable(t, codexBinary, `#!/bin/sh
count=0
while IFS= read -r _line; do
  count=$((count + 1))
  case "$count" in
    1) printf '%s\n' '{"id":1,"result":{}}' ;;
    2) ;;
    3) printf '%s\n' '{"id":2,"result":{"thread":{"id":"thread-fail"}}}' ;;
    4)
      printf '%s\n' '{"id":3,"result":{"turn":{"id":"turn-fail"}}}'
      printf '%s\n' '{"method":"turn/completed"}'
      exit 0
      ;;
  esac
done
`)

	writeAgentWorkflow(t, workspaceRoot, map[string]any{
		"codex": map[string]any{
			"command": codexBinary + " app-server",
		},
	})

	issue := domain.Issue{
		ID:         "issue-run-fail",
		Identifier: "MT-249",
		Title:      "Log failure",
		State:      "In Progress",
	}

	logBuffer := captureAgentLogs(t)
	err := Run(issue, Options{
		IssueStateFetcher: func(_ []string) ([]domain.Issue, error) {
			return nil, fmt.Errorf("boom")
		},
	})
	if err == nil {
		t.Fatal("Run() error = nil, want failure")
	}
	if !strings.Contains(logBuffer.String(), "Agent run failed for issue_id=issue-run-fail issue_identifier=MT-249: issue_state_refresh_failed: boom") {
		t.Fatalf("log missing failure message: %q", logBuffer.String())
	}
}

func writeAgentWorkflow(t *testing.T, workspaceRoot string, overrides map[string]any) {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "WORKFLOW.md")

	promptText := "Prompt"
	if prompt, ok := overrides["prompt"].(string); ok {
		promptText = prompt
	}

	lines := []string{
		"---",
		"tracker:",
		`  kind: "linear"`,
		`  api_key: "token"`,
		`  project_slug: "project"`,
		"workspace:",
		`  root: "` + workspaceRoot + `"`,
	}

	if hooks, ok := overrides["hooks"].(map[string]any); ok {
		lines = append(lines, "hooks:")
		if afterCreate, ok := hooks["after_create"].(string); ok {
			lines = append(lines, "  after_create: |", indentLines(afterCreate))
		}
	}

	if agentConfig, ok := overrides["agent"].(map[string]any); ok {
		lines = append(lines, "agent:")
		if maxTurns, ok := agentConfig["max_turns"]; ok {
			lines = append(lines, fmt.Sprintf("  max_turns: %v", maxTurns))
		}
	}

	if workerConfig, ok := overrides["worker"].(map[string]any); ok {
		lines = append(lines, "worker:")
		switch sshHosts := workerConfig["ssh_hosts"].(type) {
		case []string:
			lines = append(lines, "  ssh_hosts:")
			for _, host := range sshHosts {
				lines = append(lines, `    - "`+strings.ReplaceAll(host, `"`, `\"`)+`"`)
			}
		case []any:
			lines = append(lines, "  ssh_hosts:")
			for _, host := range sshHosts {
				lines = append(lines, fmt.Sprintf(`    - "%v"`, host))
			}
		}
	}

	lines = append(lines, "codex:")
	if codexConfig, ok := overrides["codex"].(map[string]any); ok {
		if command, ok := codexConfig["command"].(string); ok {
			lines = append(lines, `  command: "`+strings.ReplaceAll(command, `"`, `\"`)+`"`)
		}
	} else {
		lines = append(lines, `  command: "codex app-server"`)
	}

	lines = append(lines, "---", promptText)

	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(%q) failed: %v", path, err)
	}
	if err := runtimeconfig.SetWorkflowFilePath(path); err != nil {
		t.Fatalf("runtimeconfig.SetWorkflowFilePath(%q) failed: %v", path, err)
	}
}

func indentLines(value string) string {
	parts := strings.Split(value, "\n")
	for index := range parts {
		parts[index] = "    " + parts[index]
	}
	return strings.Join(parts, "\n")
}

func writeExecutable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("os.WriteFile(%q) failed: %v", path, err)
	}
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("os.WriteFile(%q) failed: %v", path, err)
	}
}

func mustMkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("os.MkdirAll(%q) failed: %v", path, err)
	}
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("os.ReadFile(%q) failed: %v", path, err)
	}
	return data
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

var agentLogCaptureMu sync.Mutex

func captureAgentLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	agentLogCaptureMu.Lock()
	var buf bytes.Buffer
	previousWriter := log.Writer()
	previousFlags := log.Flags()
	previousPrefix := log.Prefix()
	log.SetOutput(&buf)
	log.SetFlags(0)
	log.SetPrefix("")
	t.Cleanup(func() {
		log.SetOutput(previousWriter)
		log.SetFlags(previousFlags)
		log.SetPrefix(previousPrefix)
		agentLogCaptureMu.Unlock()
	})
	return &buf
}
