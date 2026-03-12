package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/openai/symphony/go/internal/runtimeconfig"
	"github.com/openai/symphony/go/internal/workflow"
)

func TestCurrentAppliesDefaultsAndWorkflowOverrides(t *testing.T) {
	writeWorkflowFile(t, `---
tracker:
  kind: linear
  project_slug: project
polling:
  interval_ms: invalid
agent:
  max_turns: 0
codex:
  command: ""
---
`)
	t.Setenv("LINEAR_API_KEY", "env-token")

	current := Current()

	if got, want := current.PollIntervalMS, defaultPollIntervalMS; got != want {
		t.Fatalf("Current().PollIntervalMS = %d, want %d", got, want)
	}
	if got, want := current.LinearActiveStates, defaultActiveStates; !equalStrings(got, want) {
		t.Fatalf("Current().LinearActiveStates = %#v, want %#v", got, want)
	}
	if got, want := current.LinearTerminalStates, defaultTerminalStates; !equalStrings(got, want) {
		t.Fatalf("Current().LinearTerminalStates = %#v, want %#v", got, want)
	}
	if got, want := current.AgentMaxTurns, defaultAgentMaxTurns; got != want {
		t.Fatalf("Current().AgentMaxTurns = %d, want %d", got, want)
	}
	if got, want := current.LinearAPIToken, "env-token"; got != want {
		t.Fatalf("Current().LinearAPIToken = %q, want %q", got, want)
	}
	if got, want := current.CodexCommand, defaultCodexCommand; got != want {
		t.Fatalf("Current().CodexCommand = %q, want %q", got, want)
	}
	if got, want := current.WorkflowPrompt, defaultPromptTemplate; got != want {
		t.Fatalf("Current().WorkflowPrompt = %q, want %q", got, want)
	}

	writeWorkflowFile(t, `---
tracker:
  kind: linear
  project_slug: project
  active_states: "Todo,  Review,"
agent:
  max_turns: 5
codex:
  command: "codex app-server --model gpt-5.3-codex"
---
Prompt body
`)

	current = Current()

	if got, want := current.LinearActiveStates, []string{"Todo", "Review"}; !equalStrings(got, want) {
		t.Fatalf("Current().LinearActiveStates = %#v, want %#v", got, want)
	}
	if got, want := current.AgentMaxTurns, 5; got != want {
		t.Fatalf("Current().AgentMaxTurns = %d, want %d", got, want)
	}
	if got, want := current.CodexCommand, "codex app-server --model gpt-5.3-codex"; got != want {
		t.Fatalf("Current().CodexCommand = %q, want %q", got, want)
	}
	if got, want := current.WorkflowPrompt, "Prompt body"; got != want {
		t.Fatalf("Current().WorkflowPrompt = %q, want %q", got, want)
	}
}

func TestCurrentResolvesEnvAndPaths(t *testing.T) {
	workspaceRoot := filepath.Join(t.TempDir(), "workspaces")
	t.Setenv("SYMPHONY_WORKSPACE_ROOT", workspaceRoot)
	t.Setenv("SYMPHONY_LINEAR_TOKEN", "resolved-token")
	t.Setenv("LINEAR_ASSIGNEE", "dev@example.com")

	writeWorkflowFile(t, `---
tracker:
  kind: linear
  api_key: $SYMPHONY_LINEAR_TOKEN
  project_slug: project
workspace:
  root: $SYMPHONY_WORKSPACE_ROOT
agent:
  max_concurrent_agents: 10
  max_concurrent_agents_by_state:
    todo: 1
    "In Progress": 4
---
`)

	current := Current()

	if got, want := current.LinearAPIToken, "resolved-token"; got != want {
		t.Fatalf("Current().LinearAPIToken = %q, want %q", got, want)
	}
	if got, want := current.LinearAssignee, "dev@example.com"; got != want {
		t.Fatalf("Current().LinearAssignee = %q, want %q", got, want)
	}
	if got, want := current.WorkspaceRoot, filepath.Clean(workspaceRoot); got != want {
		t.Fatalf("Current().WorkspaceRoot = %q, want %q", got, want)
	}
	if got, want := current.MaxConcurrentAgentsForState("Todo"), 1; got != want {
		t.Fatalf("Current().MaxConcurrentAgentsForState(Todo) = %d, want %d", got, want)
	}
	if got, want := current.MaxConcurrentAgentsForState("In Progress"), 4; got != want {
		t.Fatalf("Current().MaxConcurrentAgentsForState(In Progress) = %d, want %d", got, want)
	}
	if got, want := current.MaxConcurrentAgentsForState("Closed"), 10; got != want {
		t.Fatalf("Current().MaxConcurrentAgentsForState(Closed) = %d, want %d", got, want)
	}
}

func TestCurrentReturnsCodexDefaultsAndOverrides(t *testing.T) {
	writeWorkflowFile(t, `---
tracker:
  kind: linear
  project_slug: project
codex:
  approval_policy: on-request
  thread_sandbox: workspace-write
  turn_sandbox_policy:
    type: workspaceWrite
    writableRoots:
      - /tmp/workspace
      - /tmp/cache
---
`)
	t.Setenv("LINEAR_API_KEY", "env-token")

	current := Current()

	if got, want := current.CodexApprovalPolicy, "on-request"; got != want {
		t.Fatalf("Current().CodexApprovalPolicy = %#v, want %#v", got, want)
	}
	if got, want := current.CodexThreadSandbox, "workspace-write"; got != want {
		t.Fatalf("Current().CodexThreadSandbox = %q, want %q", got, want)
	}

	writableRoots, ok := current.CodexTurnSandboxPolicy["writableRoots"].([]any)
	if !ok {
		t.Fatalf("Current().CodexTurnSandboxPolicy[writableRoots] = %#v, want []any", current.CodexTurnSandboxPolicy["writableRoots"])
	}
	if len(writableRoots) != 2 || writableRoots[0] != "/tmp/workspace" || writableRoots[1] != "/tmp/cache" {
		t.Fatalf("writable roots = %#v, want [/tmp/workspace /tmp/cache]", writableRoots)
	}

	writeWorkflowFile(t, `---
tracker:
  kind: linear
  project_slug: project
---
`)

	current = Current()

	defaultPolicy, ok := current.CodexApprovalPolicy.(map[string]any)
	if !ok {
		t.Fatalf("Current().CodexApprovalPolicy = %#v, want map[string]any", current.CodexApprovalPolicy)
	}
	if _, ok := defaultPolicy["reject"]; !ok {
		t.Fatalf("default approval policy = %#v, want reject key", defaultPolicy)
	}
	if got, want := current.CodexThreadSandbox, defaultCodexThreadSandbox; got != want {
		t.Fatalf("Current().CodexThreadSandbox = %q, want %q", got, want)
	}
	if got, want := current.CodexTurnTimeoutMS, defaultCodexTurnTimeoutMS; got != want {
		t.Fatalf("Current().CodexTurnTimeoutMS = %d, want %d", got, want)
	}
	if got, want := current.CodexReadTimeoutMS, defaultCodexReadTimeoutMS; got != want {
		t.Fatalf("Current().CodexReadTimeoutMS = %d, want %d", got, want)
	}
	if got, want := current.CodexStallTimeoutMS, defaultCodexStallTimeoutMS; got != want {
		t.Fatalf("Current().CodexStallTimeoutMS = %d, want %d", got, want)
	}
}

func TestValidateChecksTrackerAndCodexValues(t *testing.T) {
	t.Setenv("LINEAR_API_KEY", "env-token")

	writeWorkflowFile(t, `---
tracker:
  kind: linear
---
`)
	if err := Validate(); !errors.Is(err, ErrMissingLinearProjectSlug) {
		t.Fatalf("Validate() error = %v, want %v", err, ErrMissingLinearProjectSlug)
	}

	writeWorkflowFile(t, `---
tracker:
  kind: linear
  project_slug: project
codex:
  approval_policy: 123
---
`)
	var invalidApproval *InvalidCodexApprovalPolicyError
	if err := Validate(); !errors.As(err, &invalidApproval) {
		t.Fatalf("Validate() error = %v, want InvalidCodexApprovalPolicyError", err)
	}

	writeWorkflowFile(t, `---
tracker:
  kind: linear
  project_slug: project
codex:
  thread_sandbox: 123
---
`)
	var invalidSandbox *InvalidCodexThreadSandboxError
	if err := Validate(); !errors.As(err, &invalidSandbox) {
		t.Fatalf("Validate() error = %v, want InvalidCodexThreadSandboxError", err)
	}

	writeWorkflowFile(t, `---
tracker:
  kind: linear
  project_slug: project
codex:
  turn_sandbox_policy: bad
---
`)
	var invalidTurnPolicy *InvalidCodexTurnSandboxPolicyError
	if err := Validate(); !errors.As(err, &invalidTurnPolicy) {
		t.Fatalf("Validate() error = %v, want InvalidCodexTurnSandboxPolicyError", err)
	}

	writeWorkflowFile(t, `---
tracker:
  kind: 123
---
`)
	var unsupportedTracker *UnsupportedTrackerKindError
	if err := Validate(); !errors.As(err, &unsupportedTracker) {
		t.Fatalf("Validate() error = %v, want UnsupportedTrackerKindError", err)
	}
	if unsupportedTracker.Kind != "123" {
		t.Fatalf("unsupported tracker kind = %q, want %q", unsupportedTracker.Kind, "123")
	}
}

func TestCurrentKeepsLastKnownGoodWorkflowOnReloadError(t *testing.T) {
	t.Setenv("LINEAR_API_KEY", "env-token")

	dir := t.TempDir()
	path := filepath.Join(dir, "WORKFLOW.md")
	if err := os.WriteFile(path, []byte(`---
tracker:
  kind: linear
  project_slug: project
---
Prompt one
`), 0o644); err != nil {
		t.Fatalf("os.WriteFile(%q) failed: %v", path, err)
	}
	if err := runtimeconfig.SetWorkflowFilePath(path); err != nil {
		t.Fatalf("runtimeconfig.SetWorkflowFilePath(%q) failed: %v", path, err)
	}

	current := Current()
	if got, want := current.WorkflowPrompt, "Prompt one"; got != want {
		t.Fatalf("Current().WorkflowPrompt = %q, want %q", got, want)
	}
	if err := Validate(); err != nil {
		t.Fatalf("Validate() on valid workflow failed: %v", err)
	}

	if err := os.WriteFile(path, []byte(`---
tracker: [
---
Broken
`), 0o644); err != nil {
		t.Fatalf("os.WriteFile(%q) invalid rewrite failed: %v", path, err)
	}

	current = Current()
	if got, want := current.WorkflowPrompt, "Prompt one"; got != want {
		t.Fatalf("Current().WorkflowPrompt after broken reload = %q, want last known good %q", got, want)
	}
	if err := Validate(); err != nil {
		t.Fatalf("Validate() after broken reload failed: %v, want last known good config", err)
	}
}

func writeWorkflowFile(t *testing.T, content string) {
	t.Helper()
	workflow.ResetDefaultStoreForTest()

	dir := t.TempDir()
	path := filepath.Join(dir, "WORKFLOW.md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("os.WriteFile(%q) failed: %v", path, err)
	}

	if err := runtimeconfig.SetWorkflowFilePath(path); err != nil {
		t.Fatalf("runtimeconfig.SetWorkflowFilePath(%q) failed: %v", path, err)
	}
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}

	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}

	return true
}
