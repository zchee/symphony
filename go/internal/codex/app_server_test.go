package codex

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/openai/symphony/go/internal/config"
	"github.com/openai/symphony/go/internal/domain"
	"github.com/openai/symphony/go/internal/linear"
	"github.com/openai/symphony/go/internal/runtimeconfig"
)

func TestRunRejectsWorkspaceRootAndOutsideWorkspaceRoot(t *testing.T) {
	testRoot := t.TempDir()
	workspaceRoot := filepath.Join(testRoot, "workspaces")
	outsideWorkspace := filepath.Join(testRoot, "outside")
	mustMkdirAll(t, workspaceRoot)
	mustMkdirAll(t, outsideWorkspace)
	writeCodexWorkflow(t, workspaceRoot, map[string]any{})

	issue := domain.Issue{
		ID:         "issue-workspace-guard",
		Identifier: "MT-999",
		Title:      "Validate workspace guard",
	}

	_, err := Run(workspaceRoot, "guard", issue)
	var invalidErr *InvalidWorkspaceCwdError
	if !errors.As(err, &invalidErr) || invalidErr.Kind != "workspace_root" {
		t.Fatalf("Run(workspaceRoot) error = %v, want workspace_root InvalidWorkspaceCwdError", err)
	}

	_, err = Run(outsideWorkspace, "guard", issue)
	if !errors.As(err, &invalidErr) || invalidErr.Kind != "outside_workspace_root" {
		t.Fatalf("Run(outsideWorkspace) error = %v, want outside_workspace_root InvalidWorkspaceCwdError", err)
	}
}

func TestRunMarksRequestForInputEventsAsHardFailure(t *testing.T) {
	testRoot := t.TempDir()
	workspaceRoot := filepath.Join(testRoot, "workspaces")
	workspace := filepath.Join(workspaceRoot, "MT-88")
	codexBinary := filepath.Join(testRoot, "fake-codex")
	traceFile := filepath.Join(testRoot, "codex-input.trace")
	mustMkdirAll(t, workspace)
	t.Setenv("SYMP_TEST_CODEX_TRACE", traceFile)

	writeExecutable(t, codexBinary, `#!/bin/sh
trace_file="${SYMP_TEST_CODEX_TRACE:-/tmp/codex-input.trace}"
count=0
while IFS= read -r line; do
  count=$((count + 1))
  printf 'JSON:%s\n' "$line" >> "$trace_file"
  case "$count" in
    1) printf '%s\n' '{"id":1,"result":{}}' ;;
    2) printf '%s\n' '{"id":2,"result":{"thread":{"id":"thread-88"}}}' ;;
    3) printf '%s\n' '{"id":3,"result":{"turn":{"id":"turn-88"}}}' ;;
    4) printf '%s\n' '{"method":"turn/input_required","id":"resp-1","params":{"requiresInput":true,"reason":"blocked"}}' ;;
    *) exit 0 ;;
  esac
done
`)

	writeCodexWorkflow(t, workspaceRoot, map[string]any{
		"codex": map[string]any{
			"command": codexBinary + " app-server",
		},
	})

	issue := domain.Issue{
		ID:         "issue-input",
		Identifier: "MT-88",
		Title:      "Input needed",
	}

	_, err := Run(workspace, "Needs input", issue)
	var inputErr *TurnInputRequiredError
	if !errors.As(err, &inputErr) {
		t.Fatalf("Run() error = %v, want TurnInputRequiredError", err)
	}
	if stringValue(inputErr.Payload["method"]) != "turn/input_required" {
		t.Fatalf("inputErr.Payload.method = %#v, want turn/input_required", inputErr.Payload["method"])
	}
}

func TestRunFailsWhenApprovalIsRequiredUnderSaferDefaults(t *testing.T) {
	testRoot := t.TempDir()
	workspaceRoot := filepath.Join(testRoot, "workspaces")
	workspace := filepath.Join(workspaceRoot, "MT-89")
	codexBinary := filepath.Join(testRoot, "fake-codex")
	mustMkdirAll(t, workspace)

	writeExecutable(t, codexBinary, `#!/bin/sh
count=0
while IFS= read -r _line; do
  count=$((count + 1))
  case "$count" in
    1) printf '%s\n' '{"id":1,"result":{}}' ;;
    2) printf '%s\n' '{"id":2,"result":{"thread":{"id":"thread-89"}}}' ;;
    3)
      printf '%s\n' '{"id":3,"result":{"turn":{"id":"turn-89"}}}'
      printf '%s\n' '{"id":99,"method":"item/commandExecution/requestApproval","params":{"command":"gh pr view","cwd":"/tmp","reason":"need approval"}}'
      ;;
    *) sleep 1 ;;
  esac
done
`)

	writeCodexWorkflow(t, workspaceRoot, map[string]any{
		"codex": map[string]any{
			"command": codexBinary + " app-server",
		},
	})

	issue := domain.Issue{
		ID:         "issue-approval-required",
		Identifier: "MT-89",
		Title:      "Approval required",
	}

	_, err := Run(workspace, "Handle approval request", issue)
	var approvalErr *ApprovalRequiredError
	if !errors.As(err, &approvalErr) {
		t.Fatalf("Run() error = %v, want ApprovalRequiredError", err)
	}
	if stringValue(approvalErr.Payload["method"]) != "item/commandExecution/requestApproval" {
		t.Fatalf("approvalErr.Payload.method = %#v, want item/commandExecution/requestApproval", approvalErr.Payload["method"])
	}
}

func TestRunAutoApprovesCommandExecutionRequestsWhenApprovalPolicyIsNever(t *testing.T) {
	testRoot := t.TempDir()
	workspaceRoot := filepath.Join(testRoot, "workspaces")
	workspace := filepath.Join(workspaceRoot, "MT-89")
	codexBinary := filepath.Join(testRoot, "fake-codex")
	traceFile := filepath.Join(testRoot, "codex-auto-approve.trace")
	mustMkdirAll(t, workspace)
	t.Setenv("SYMP_TEST_CODEX_TRACE", traceFile)

	writeExecutable(t, codexBinary, `#!/bin/sh
trace_file="${SYMP_TEST_CODEX_TRACE:-/tmp/codex-auto-approve.trace}"
count=0
while IFS= read -r line; do
  count=$((count + 1))
  printf 'JSON:%s\n' "$line" >> "$trace_file"
  case "$count" in
    1) printf '%s\n' '{"id":1,"result":{}}' ;;
    2) ;;
    3) printf '%s\n' '{"id":2,"result":{"thread":{"id":"thread-89"}}}' ;;
    4)
      printf '%s\n' '{"id":3,"result":{"turn":{"id":"turn-89"}}}'
      printf '%s\n' '{"id":99,"method":"item/commandExecution/requestApproval","params":{"command":"gh pr view","cwd":"/tmp","reason":"need approval"}}'
      ;;
    5)
      printf '%s\n' '{"method":"turn/completed"}'
      exit 0
      ;;
    *) exit 0 ;;
  esac
done
`)

	writeCodexWorkflow(t, workspaceRoot, map[string]any{
		"codex": map[string]any{
			"command":         codexBinary + " app-server",
			"approval_policy": "never",
		},
	})

	issue := domain.Issue{
		ID:         "issue-auto-approve",
		Identifier: "MT-89",
		Title:      "Auto approve request",
	}

	_, err := Run(workspace, "Handle approval request", issue)
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	trace, err := os.ReadFile(traceFile)
	if err != nil {
		t.Fatalf("os.ReadFile(traceFile) failed: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(trace)), "\n")

	assertTraceContainsJSON(t, lines, func(payload map[string]any) bool {
		return intEquals(payload["id"], 1) && nestedBool(payload, "params", "capabilities", "experimentalApi")
	})
	assertTraceContainsJSON(t, lines, func(payload map[string]any) bool {
		if !intEquals(payload["id"], 2) {
			return false
		}
		tools, ok := payload["params"].(map[string]any)["dynamicTools"].([]any)
		if !ok || len(tools) != 1 {
			return false
		}
		tool, ok := tools[0].(map[string]any)
		if !ok {
			return false
		}
		required, ok := tool["inputSchema"].(map[string]any)["required"].([]any)
		return ok && tool["name"] == "linear_graphql" && len(required) == 1 && required[0] == "query"
	})
	assertTraceContainsJSON(t, lines, func(payload map[string]any) bool {
		return intEquals(payload["id"], 99) && lookupString(payload, "result", "decision") == "acceptForSession"
	})
}

func TestRunAutoApprovesMCPToolApprovalPromptsWhenApprovalPolicyIsNever(t *testing.T) {
	testRoot := t.TempDir()
	workspaceRoot := filepath.Join(testRoot, "workspaces")
	workspace := filepath.Join(workspaceRoot, "MT-717")
	codexBinary := filepath.Join(testRoot, "fake-codex")
	traceFile := filepath.Join(testRoot, "codex-tool-user-input-auto-approve.trace")
	mustMkdirAll(t, workspace)
	t.Setenv("SYMP_TEST_CODEX_TRACE", traceFile)

	writeExecutable(t, codexBinary, `#!/bin/sh
trace_file="${SYMP_TEST_CODEX_TRACE:-/tmp/codex-tool-user-input-auto-approve.trace}"
count=0
while IFS= read -r line; do
  count=$((count + 1))
  printf 'JSON:%s\n' "$line" >> "$trace_file"
  case "$count" in
    1) printf '%s\n' '{"id":1,"result":{}}' ;;
    2) ;;
    3) printf '%s\n' '{"id":2,"result":{"thread":{"id":"thread-717"}}}' ;;
    4)
      printf '%s\n' '{"id":3,"result":{"turn":{"id":"turn-717"}}}'
      printf '%s\n' '{"id":110,"method":"item/tool/requestUserInput","params":{"itemId":"call-717","questions":[{"header":"Approve app tool call?","id":"mcp_tool_call_approval_call-717","isOther":false,"isSecret":false,"options":[{"description":"Run the tool and continue.","label":"Approve Once"},{"description":"Run the tool and remember this choice for this session.","label":"Approve this Session"},{"description":"Decline this tool call and continue.","label":"Deny"},{"description":"Cancel this tool call","label":"Cancel"}],"question":"The linear MCP server wants to run the tool \"Save issue\", which may modify or delete data. Allow this action?"}],"threadId":"thread-717","turnId":"turn-717"}}'
      ;;
    5)
      printf '%s\n' '{"method":"turn/completed"}'
      exit 0
      ;;
    *) exit 0 ;;
  esac
done
`)

	writeCodexWorkflow(t, workspaceRoot, map[string]any{
		"codex": map[string]any{
			"command":         codexBinary + " app-server",
			"approval_policy": "never",
		},
	})

	issue := domain.Issue{ID: "issue-tool-user-input-auto-approve", Identifier: "MT-717", Title: "Tool approval"}
	_, err := Run(workspace, "Handle tool approval prompt", issue)
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	trace := string(mustReadFile(t, traceFile))
	lines := strings.Split(strings.TrimSpace(trace), "\n")
	assertTraceContainsJSON(t, lines, func(payload map[string]any) bool {
		return intEquals(payload["id"], 110) && lookupString(payload, "result", "answers", "mcp_tool_call_approval_call-717", "answers", "0") == "Approve this Session"
	})
}

func TestRunSendsGenericNonInteractiveAnswerForFreeformToolInput(t *testing.T) {
	testRoot := t.TempDir()
	workspaceRoot := filepath.Join(testRoot, "workspaces")
	workspace := filepath.Join(workspaceRoot, "MT-718")
	codexBinary := filepath.Join(testRoot, "fake-codex")
	mustMkdirAll(t, workspace)

	writeExecutable(t, codexBinary, `#!/bin/sh
count=0
while IFS= read -r _line; do
  count=$((count + 1))
  case "$count" in
    1) printf '%s\n' '{"id":1,"result":{}}' ;;
    2) ;;
    3) printf '%s\n' '{"id":2,"result":{"thread":{"id":"thread-718"}}}' ;;
    4)
      printf '%s\n' '{"id":3,"result":{"turn":{"id":"turn-718"}}}'
      printf '%s\n' '{"id":111,"method":"item/tool/requestUserInput","params":{"itemId":"call-718","questions":[{"header":"Provide context","id":"freeform-718","isOther":false,"isSecret":false,"options":null,"question":"What comment should I post back to the issue?"}],"threadId":"thread-718","turnId":"turn-718"}}'
      ;;
    5)
      printf '%s\n' '{"method":"turn/completed"}'
      exit 0
      ;;
    *) exit 0 ;;
  esac
done
`)

	writeCodexWorkflow(t, workspaceRoot, map[string]any{
		"codex": map[string]any{
			"command":         codexBinary + " app-server",
			"approval_policy": "never",
		},
	})

	issue := domain.Issue{ID: "issue-tool-user-input-required", Identifier: "MT-718", Title: "Freeform tool input"}
	var gotMessages []map[string]any
	_, err := Run(workspace, "Handle generic tool input", issue, RunOptions{
		OnMessage: func(message map[string]any) {
			gotMessages = append(gotMessages, message)
		},
	})
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}
	for _, message := range gotMessages {
		if message["event"] == "tool_input_auto_answered" && message["answer"] == nonInteractiveToolInputAnswer {
			return
		}
	}
	t.Fatalf("messages = %#v, want tool_input_auto_answered with generic answer", gotMessages)
}

func TestRunSendsGenericNonInteractiveAnswerForOptionBasedToolInput(t *testing.T) {
	testRoot := t.TempDir()
	workspaceRoot := filepath.Join(testRoot, "workspaces")
	workspace := filepath.Join(workspaceRoot, "MT-719")
	codexBinary := filepath.Join(testRoot, "fake-codex")
	traceFile := filepath.Join(testRoot, "codex-tool-user-input-options.trace")
	mustMkdirAll(t, workspace)
	t.Setenv("SYMP_TEST_CODEX_TRACE", traceFile)

	writeExecutable(t, codexBinary, `#!/bin/sh
trace_file="${SYMP_TEST_CODEX_TRACE:-/tmp/codex-tool-user-input-options.trace}"
count=0
while IFS= read -r line; do
  count=$((count + 1))
  printf 'JSON:%s\n' "$line" >> "$trace_file"
  case "$count" in
    1) printf '%s\n' '{"id":1,"result":{}}' ;;
    2) ;;
    3) printf '%s\n' '{"id":2,"result":{"thread":{"id":"thread-719"}}}' ;;
    4)
      printf '%s\n' '{"id":3,"result":{"turn":{"id":"turn-719"}}}'
      printf '%s\n' '{"id":112,"method":"item/tool/requestUserInput","params":{"itemId":"call-719","questions":[{"header":"Choose an action","id":"options-719","isOther":false,"isSecret":false,"options":[{"description":"Use the default behavior.","label":"Use default"},{"description":"Skip this step.","label":"Skip"}],"question":"How should I proceed?"}],"threadId":"thread-719","turnId":"turn-719"}}'
      ;;
    5)
      printf '%s\n' '{"method":"turn/completed"}'
      exit 0
      ;;
    *) exit 0 ;;
  esac
done
`)

	writeCodexWorkflow(t, workspaceRoot, map[string]any{
		"codex": map[string]any{
			"command": codexBinary + " app-server",
		},
	})

	issue := domain.Issue{ID: "issue-tool-user-input-options", Identifier: "MT-719", Title: "Option tool input"}
	_, err := Run(workspace, "Handle option based tool input", issue)
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	trace := string(mustReadFile(t, traceFile))
	lines := strings.Split(strings.TrimSpace(trace), "\n")
	assertTraceContainsJSON(t, lines, func(payload map[string]any) bool {
		return intEquals(payload["id"], 112) && lookupString(payload, "result", "answers", "options-719", "answers", "0") == nonInteractiveToolInputAnswer
	})
}

func TestRunRejectsUnsupportedDynamicToolCallsWithoutStalling(t *testing.T) {
	testRoot := t.TempDir()
	workspaceRoot := filepath.Join(testRoot, "workspaces")
	workspace := filepath.Join(workspaceRoot, "MT-90")
	codexBinary := filepath.Join(testRoot, "fake-codex")
	traceFile := filepath.Join(testRoot, "codex-tool-call.trace")
	mustMkdirAll(t, workspace)
	t.Setenv("SYMP_TEST_CODEX_TRACE", traceFile)

	writeExecutable(t, codexBinary, `#!/bin/sh
trace_file="${SYMP_TEST_CODEX_TRACE:-/tmp/codex-tool-call.trace}"
count=0
while IFS= read -r line; do
  count=$((count + 1))
  printf 'JSON:%s\n' "$line" >> "$trace_file"
  case "$count" in
    1) printf '%s\n' '{"id":1,"result":{}}' ;;
    2) ;;
    3) printf '%s\n' '{"id":2,"result":{"thread":{"id":"thread-90"}}}' ;;
    4)
      printf '%s\n' '{"id":3,"result":{"turn":{"id":"turn-90"}}}'
      printf '%s\n' '{"id":101,"method":"item/tool/call","params":{"tool":"some_tool","callId":"call-90","threadId":"thread-90","turnId":"turn-90","arguments":{}}}'
      ;;
    5)
      printf '%s\n' '{"method":"turn/completed"}'
      exit 0
      ;;
    *) exit 0 ;;
  esac
done
`)

	writeCodexWorkflow(t, workspaceRoot, map[string]any{
		"codex": map[string]any{
			"command": codexBinary + " app-server",
		},
	})

	issue := domain.Issue{ID: "issue-tool-call", Identifier: "MT-90", Title: "Unsupported tool call"}
	_, err := Run(workspace, "Reject unsupported tool calls", issue)
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}
	trace := string(mustReadFile(t, traceFile))
	lines := strings.Split(strings.TrimSpace(trace), "\n")
	assertTraceContainsJSON(t, lines, func(payload map[string]any) bool {
		text := lookupString(payload, "result", "contentItems", "0", "text")
		return intEquals(payload["id"], 101) && payloadAtPathBool(payload, "result", "success") == false && strings.Contains(text, "Unsupported dynamic tool")
	})
}

func TestLinearGraphQLToolErrorPayloadFormatsActionableFailures(t *testing.T) {
	missingToken := linearGraphQLToolErrorPayload(config.ErrMissingLinearAPIToken)
	if got := nestedMapString(t, missingToken, "error", "message"); got != "Symphony is missing Linear auth. Set `linear.api_key` in `WORKFLOW.md` or export `LINEAR_API_KEY`." {
		t.Fatalf("missingToken message = %q", got)
	}

	statusErr := linearGraphQLToolErrorPayload(&linear.StatusError{Status: 503})
	if got := nestedMapString(t, statusErr, "error", "message"); got != "Linear GraphQL request failed with HTTP 503." {
		t.Fatalf("statusErr message = %q", got)
	}
	if got := nestedMapInt(t, statusErr, "error", "status"); got != 503 {
		t.Fatalf("statusErr status = %d, want 503", got)
	}

	requestErr := linearGraphQLToolErrorPayload(&linear.RequestError{Reason: errors.New("timeout")})
	if got := nestedMapString(t, requestErr, "error", "message"); got != "Linear GraphQL request failed before receiving a successful response." {
		t.Fatalf("requestErr message = %q", got)
	}
	if got := nestedMapString(t, requestErr, "error", "reason"); got != "timeout" {
		t.Fatalf("requestErr reason = %q, want timeout", got)
	}

	unexpected := linearGraphQLToolErrorPayload(errors.New("boom"))
	if got := nestedMapString(t, unexpected, "error", "message"); got != "Linear GraphQL tool execution failed." {
		t.Fatalf("unexpected message = %q", got)
	}
	if got := nestedMapString(t, unexpected, "error", "reason"); got != "boom" {
		t.Fatalf("unexpected reason = %q, want boom", got)
	}
}

func TestRunExecutesSupportedDynamicToolCallsAndReturnsToolResult(t *testing.T) {
	testRoot := t.TempDir()
	workspaceRoot := filepath.Join(testRoot, "workspaces")
	workspace := filepath.Join(workspaceRoot, "MT-90A")
	codexBinary := filepath.Join(testRoot, "fake-codex")
	traceFile := filepath.Join(testRoot, "codex-supported-tool-call.trace")
	mustMkdirAll(t, workspace)
	t.Setenv("SYMP_TEST_CODEX_TRACE", traceFile)

	writeExecutable(t, codexBinary, `#!/bin/sh
trace_file="${SYMP_TEST_CODEX_TRACE:-/tmp/codex-supported-tool-call.trace}"
count=0
while IFS= read -r line; do
  count=$((count + 1))
  printf 'JSON:%s\n' "$line" >> "$trace_file"
  case "$count" in
    1) printf '%s\n' '{"id":1,"result":{}}' ;;
    2) ;;
    3) printf '%s\n' '{"id":2,"result":{"thread":{"id":"thread-90a"}}}' ;;
    4)
      printf '%s\n' '{"id":3,"result":{"turn":{"id":"turn-90a"}}}'
      printf '%s\n' '{"id":102,"method":"item/tool/call","params":{"name":"linear_graphql","callId":"call-90a","threadId":"thread-90a","turnId":"turn-90a","arguments":{"query":"query Viewer { viewer { id } }","variables":{"includeTeams":false}}}}'
      ;;
    5)
      printf '%s\n' '{"method":"turn/completed"}'
      exit 0
      ;;
    *) exit 0 ;;
  esac
done
`)

	writeCodexWorkflow(t, workspaceRoot, map[string]any{
		"codex": map[string]any{
			"command": codexBinary + " app-server",
		},
	})

	issue := domain.Issue{ID: "issue-supported-tool-call", Identifier: "MT-90A", Title: "Supported tool call"}
	var toolCall struct {
		Tool string
		Args map[string]any
	}
	_, err := Run(workspace, "Handle supported tool calls", issue, RunOptions{
		ToolExecutor: func(tool string, arguments map[string]any) map[string]any {
			toolCall.Tool = tool
			toolCall.Args = arguments
			return map[string]any{
				"success": true,
				"contentItems": []any{
					map[string]any{"type": "inputText", "text": `{"data":{"viewer":{"id":"usr_123"}}}`},
				},
			}
		},
	})
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}
	if toolCall.Tool != "linear_graphql" || toolCall.Args["query"] != "query Viewer { viewer { id } }" {
		t.Fatalf("tool call = %#v, want linear_graphql query", toolCall)
	}
	trace := string(mustReadFile(t, traceFile))
	lines := strings.Split(strings.TrimSpace(trace), "\n")
	assertTraceContainsJSON(t, lines, func(payload map[string]any) bool {
		return intEquals(payload["id"], 102) && payloadAtPathBool(payload, "result", "success") && lookupString(payload, "result", "contentItems", "0", "text") == `{"data":{"viewer":{"id":"usr_123"}}}`
	})
}

func TestRunEmitsToolCallFailedForSupportedToolFailures(t *testing.T) {
	testRoot := t.TempDir()
	workspaceRoot := filepath.Join(testRoot, "workspaces")
	workspace := filepath.Join(workspaceRoot, "MT-90B")
	codexBinary := filepath.Join(testRoot, "fake-codex")
	mustMkdirAll(t, workspace)

	writeExecutable(t, codexBinary, `#!/bin/sh
count=0
while IFS= read -r _line; do
  count=$((count + 1))
  case "$count" in
    1) printf '%s\n' '{"id":1,"result":{}}' ;;
    2) ;;
    3) printf '%s\n' '{"id":2,"result":{"thread":{"id":"thread-90b"}}}' ;;
    4)
      printf '%s\n' '{"id":3,"result":{"turn":{"id":"turn-90b"}}}'
      printf '%s\n' '{"id":103,"method":"item/tool/call","params":{"tool":"linear_graphql","callId":"call-90b","threadId":"thread-90b","turnId":"turn-90b","arguments":{"query":"query Viewer { viewer { id } }"}}}'
      ;;
    5)
      printf '%s\n' '{"method":"turn/completed"}'
      exit 0
      ;;
    *) exit 0 ;;
  esac
done
`)

	writeCodexWorkflow(t, workspaceRoot, map[string]any{
		"codex": map[string]any{
			"command": codexBinary + " app-server",
		},
	})

	issue := domain.Issue{ID: "issue-tool-call-failed", Identifier: "MT-90B", Title: "Tool call failed"}
	var got []map[string]any
	_, err := Run(workspace, "Handle failed tool calls", issue, RunOptions{
		OnMessage: func(message map[string]any) { got = append(got, message) },
		ToolExecutor: func(tool string, arguments map[string]any) map[string]any {
			return map[string]any{
				"success": false,
				"contentItems": []any{
					map[string]any{"type": "inputText", "text": `{"error":{"message":"boom"}}`},
				},
			}
		},
	})
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}
	for _, message := range got {
		if message["event"] == "tool_call_failed" {
			return
		}
	}
	t.Fatalf("messages = %#v, want tool_call_failed", got)
}

func TestRunEmitsTurnCompletedEvent(t *testing.T) {
	testRoot := t.TempDir()
	workspaceRoot := filepath.Join(testRoot, "workspaces")
	workspace := filepath.Join(workspaceRoot, "MT-91A")
	codexBinary := filepath.Join(testRoot, "fake-codex")
	mustMkdirAll(t, workspace)

	writeExecutable(t, codexBinary, `#!/bin/sh
count=0
while IFS= read -r _line; do
  count=$((count + 1))
  case "$count" in
    1) printf '%s\n' '{"id":1,"result":{}}' ;;
    2) printf '%s\n' '{"id":2,"result":{"thread":{"id":"thread-91a"}}}' ;;
    3) printf '%s\n' '{"id":3,"result":{"turn":{"id":"turn-91a"}}}' ;;
    4)
      printf '%s\n' '{"method":"turn/completed","params":{"turn":{"status":"completed"},"usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12}}}'
      exit 0
      ;;
    *) exit 0 ;;
  esac
done
`)

	writeCodexWorkflow(t, workspaceRoot, map[string]any{
		"codex": map[string]any{
			"command": codexBinary + " app-server",
		},
	})

	issue := domain.Issue{ID: "issue-turn-completed", Identifier: "MT-91A", Title: "Turn completed event"}
	var events []map[string]any
	_, err := Run(workspace, "Emit turn completed", issue, RunOptions{
		OnMessage: func(message map[string]any) {
			events = append(events, message)
		},
	})
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}
	if len(events) < 2 {
		t.Fatalf("events = %#v, want session_started and turn_completed", events)
	}
	if events[0]["event"] != "session_started" || events[0]["session_id"] != "thread-91a-turn-91a" {
		t.Fatalf("events[0] = %#v, want session_started thread-91a-turn-91a", events[0])
	}
	last := events[len(events)-1]
	if last["event"] != "turn_completed" || lookupString(last, "payload", "method") != "turn/completed" {
		t.Fatalf("last event = %#v, want turn_completed payload", last)
	}
}

func TestRunReturnsTurnFailedErrorAndEmitsEvent(t *testing.T) {
	testRoot := t.TempDir()
	workspaceRoot := filepath.Join(testRoot, "workspaces")
	workspace := filepath.Join(workspaceRoot, "MT-91B")
	codexBinary := filepath.Join(testRoot, "fake-codex")
	mustMkdirAll(t, workspace)

	writeExecutable(t, codexBinary, `#!/bin/sh
count=0
while IFS= read -r _line; do
  count=$((count + 1))
  case "$count" in
    1) printf '%s\n' '{"id":1,"result":{}}' ;;
    2) printf '%s\n' '{"id":2,"result":{"thread":{"id":"thread-91b"}}}' ;;
    3) printf '%s\n' '{"id":3,"result":{"turn":{"id":"turn-91b"}}}' ;;
    4)
      printf '%s\n' '{"method":"turn/failed","params":{"error":{"message":"boom"}}}'
      exit 0
      ;;
    *) exit 0 ;;
  esac
done
`)

	writeCodexWorkflow(t, workspaceRoot, map[string]any{
		"codex": map[string]any{
			"command": codexBinary + " app-server",
		},
	})

	issue := domain.Issue{ID: "issue-turn-failed", Identifier: "MT-91B", Title: "Turn failed event"}
	var got []map[string]any
	_, err := Run(workspace, "Emit turn failed", issue, RunOptions{
		OnMessage: func(message map[string]any) {
			got = append(got, message)
		},
	})
	var failedErr *TurnFailedError
	if !errors.As(err, &failedErr) {
		t.Fatalf("Run() error = %v, want TurnFailedError", err)
	}
	var sawSessionStarted bool
	var sawTurnFailed bool
	var sawTurnEndedWithError bool
	for _, message := range got {
		if message["event"] == "session_started" && message["session_id"] == "thread-91b-turn-91b" {
			sawSessionStarted = true
		}
		if message["event"] == "turn_failed" && lookupString(message, "payload", "method") == "turn/failed" {
			sawTurnFailed = true
		}
		if message["event"] == "turn_ended_with_error" && message["session_id"] == "thread-91b-turn-91b" {
			sawTurnEndedWithError = true
		}
	}
	if !sawSessionStarted || !sawTurnFailed || !sawTurnEndedWithError {
		t.Fatalf("events = %#v, want session_started, turn_failed, and turn_ended_with_error", got)
	}
}

func TestRunEmitsStartupFailedWhenTurnStartFails(t *testing.T) {
	testRoot := t.TempDir()
	workspaceRoot := filepath.Join(testRoot, "workspaces")
	workspace := filepath.Join(workspaceRoot, "MT-91D")
	codexBinary := filepath.Join(testRoot, "fake-codex")
	mustMkdirAll(t, workspace)

	writeExecutable(t, codexBinary, `#!/bin/sh
count=0
while IFS= read -r _line; do
  count=$((count + 1))
  case "$count" in
    1) printf '%s\n' '{"id":1,"result":{}}' ;;
    2) printf '%s\n' '{"id":2,"result":{"thread":{"id":"thread-91d"}}}' ;;
    3) exit 7 ;;
    *) exit 0 ;;
  esac
done
`)

	writeCodexWorkflow(t, workspaceRoot, map[string]any{
		"codex": map[string]any{
			"command": codexBinary + " app-server",
		},
	})

	issue := domain.Issue{ID: "issue-startup-failed", Identifier: "MT-91D", Title: "Startup failed"}
	var events []map[string]any
	_, err := Run(workspace, "Emit startup failed", issue, RunOptions{
		OnMessage: func(message map[string]any) {
			events = append(events, message)
		},
	})
	if err == nil {
		t.Fatal("Run() error = nil, want startup failure")
	}
	for _, event := range events {
		if event["event"] == "startup_failed" {
			return
		}
	}
	t.Fatalf("events = %#v, want startup_failed", events)
}

func TestRunBuffersLongJSONLinesUntilNewline(t *testing.T) {
	testRoot := t.TempDir()
	workspaceRoot := filepath.Join(testRoot, "workspaces")
	workspace := filepath.Join(workspaceRoot, "MT-91C")
	codexBinary := filepath.Join(testRoot, "fake-codex")
	mustMkdirAll(t, workspace)

	writeExecutable(t, codexBinary, `#!/bin/sh
count=0
while IFS= read -r _line; do
  count=$((count + 1))
  case "$count" in
    1)
      padding=$(printf '%*s' 1100000 '' | tr ' ' a)
      printf '{"id":1,"result":{},"padding":"%s"}\n' "$padding"
      ;;
    2) printf '%s\n' '{"id":2,"result":{"thread":{"id":"thread-91c"}}}' ;;
    3) printf '%s\n' '{"id":3,"result":{"turn":{"id":"turn-91c"}}}' ;;
    4)
      printf '%s\n' '{"method":"turn/completed"}'
      exit 0
      ;;
    *) exit 0 ;;
  esac
done
`)

	writeCodexWorkflow(t, workspaceRoot, map[string]any{
		"codex": map[string]any{
			"command": codexBinary + " app-server",
		},
	})

	issue := domain.Issue{ID: "issue-long-line", Identifier: "MT-91C", Title: "Long line buffering"}
	if _, err := Run(workspace, "Buffer newline-delimited JSON", issue); err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}
}

func TestRunCapturesNonJSONStderrAsMalformedEvent(t *testing.T) {
	testRoot := t.TempDir()
	workspaceRoot := filepath.Join(testRoot, "workspaces")
	workspace := filepath.Join(workspaceRoot, "MT-92")
	codexBinary := filepath.Join(testRoot, "fake-codex")
	mustMkdirAll(t, workspace)

	writeExecutable(t, codexBinary, `#!/bin/sh
count=0
while IFS= read -r _line; do
  count=$((count + 1))
  case "$count" in
    1) printf '%s\n' '{"id":1,"result":{}}' ;;
    2) printf '%s\n' '{"id":2,"result":{"thread":{"id":"thread-92"}}}' ;;
    3) printf '%s\n' '{"id":3,"result":{"turn":{"id":"turn-92"}}}' ;;
    4)
      printf '%s\n' 'warning: this is stderr noise' >&2
      printf '%s\n' '{"method":"turn/completed"}'
      exit 0
      ;;
    *) exit 0 ;;
  esac
done
`)

	writeCodexWorkflow(t, workspaceRoot, map[string]any{
		"codex": map[string]any{
			"command": codexBinary + " app-server",
		},
	})

	issue := domain.Issue{ID: "issue-stderr", Identifier: "MT-92", Title: "Capture stderr"}
	var events []map[string]any
	_, err := Run(workspace, "Capture stderr line", issue, RunOptions{
		OnMessage: func(message map[string]any) {
			events = append(events, message)
		},
	})
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}
	for _, event := range events {
		if event["event"] == "malformed" && event["stream"] == "stderr" && event["raw"] == "warning: this is stderr noise" {
			return
		}
	}
	t.Fatalf("events = %#v, want malformed stderr event", events)
}

func writeCodexWorkflow(t *testing.T, workspaceRoot string, overrides map[string]any) {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "WORKFLOW.md")

	codexCommand := "codex app-server"
	codexApprovalPolicy := ""
	if codex, ok := overrides["codex"].(map[string]any); ok {
		if command, ok := codex["command"].(string); ok {
			codexCommand = command
		}
		if approval, ok := codex["approval_policy"].(string); ok {
			codexApprovalPolicy = approval
		}
	}

	lines := []string{
		"---",
		"tracker:",
		`  kind: "linear"`,
		`  api_key: "token"`,
		`  project_slug: "project"`,
		"workspace:",
		`  root: "` + workspaceRoot + `"`,
		"codex:",
		`  command: "` + strings.ReplaceAll(codexCommand, `"`, `\"`) + `"`,
	}
	if codexApprovalPolicy != "" {
		lines = append(lines, `  approval_policy: "`+codexApprovalPolicy+`"`)
	}
	lines = append(lines, "---", "Prompt")

	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(%q) failed: %v", path, err)
	}
	if err := runtimeconfig.SetWorkflowFilePath(path); err != nil {
		t.Fatalf("runtimeconfig.SetWorkflowFilePath(%q) failed: %v", path, err)
	}
}

func writeExecutable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
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

func assertTraceContainsJSON(t *testing.T, lines []string, predicate func(map[string]any) bool) {
	t.Helper()
	for _, line := range lines {
		if !strings.HasPrefix(line, "JSON:") {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "JSON:")), &payload); err == nil && predicate(payload) {
			return
		}
	}
	t.Fatal("trace did not contain the expected JSON payload")
}

func intEquals(value any, want int) bool {
	switch typed := value.(type) {
	case float64:
		return int(typed) == want
	case int:
		return typed == want
	default:
		return false
	}
}

func nestedBool(root map[string]any, path ...string) bool {
	current := any(root)
	for _, segment := range path {
		node, ok := current.(map[string]any)
		if !ok {
			return false
		}
		current, ok = node[segment]
		if !ok {
			return false
		}
	}
	value, _ := current.(bool)
	return value
}

func lookupString(root map[string]any, path ...string) string {
	current := any(root)
	for _, segment := range path {
		switch typed := current.(type) {
		case map[string]any:
			next, ok := typed[segment]
			if !ok {
				return ""
			}
			current = next
		case []any:
			index, err := strconv.Atoi(segment)
			if err != nil || index < 0 || index >= len(typed) {
				return ""
			}
			current = typed[index]
		default:
			return ""
		}
	}
	value, _ := current.(string)
	return value
}

func payloadAtPathBool(root map[string]any, path ...string) bool {
	current := any(root)
	for _, segment := range path {
		switch typed := current.(type) {
		case map[string]any:
			next, ok := typed[segment]
			if !ok {
				return false
			}
			current = next
		case []any:
			index, err := strconv.Atoi(segment)
			if err != nil || index < 0 || index >= len(typed) {
				return false
			}
			current = typed[index]
		default:
			return false
		}
	}
	value, _ := current.(bool)
	return value
}

func nestedMapString(t *testing.T, root map[string]any, path ...string) string {
	t.Helper()
	return lookupString(root, path...)
}

func nestedMapInt(t *testing.T, root map[string]any, path ...string) int {
	t.Helper()
	current := any(root)
	for _, segment := range path {
		switch typed := current.(type) {
		case map[string]any:
			next, ok := typed[segment]
			if !ok {
				t.Fatalf("missing path segment %q in %#v", segment, root)
			}
			current = next
		default:
			t.Fatalf("unexpected type %T while resolving path %#v", current, path)
		}
	}
	switch typed := current.(type) {
	case int:
		return typed
	case float64:
		return int(typed)
	default:
		t.Fatalf("unexpected int value type %T at path %#v", current, path)
		return 0
	}
}
