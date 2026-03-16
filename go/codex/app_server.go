package codex

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/openai/symphony/go/config"
	"github.com/openai/symphony/go/domain"
	"github.com/openai/symphony/go/linear"
	"github.com/openai/symphony/go/pathsafety"
	"github.com/openai/symphony/go/ssh"
)

const (
	initializeID                  = 1
	threadStartID                 = 2
	turnStartID                   = 3
	maxLineBytes                  = 1 << 20
	maxStreamLogBytes             = 1000
	nonInteractiveToolInputAnswer = "This is a non-interactive session. Operator input is unavailable."
	turnTailDrainQuiet            = 10 * time.Millisecond
)

// InvalidWorkspaceCwdError reports an invalid Codex cwd target.
type InvalidWorkspaceCwdError struct {
	Kind string
	Path string
	Root string
}

func (e *InvalidWorkspaceCwdError) Error() string {
	switch e.Kind {
	case "workspace_root":
		return fmt.Sprintf("invalid_workspace_cwd: workspace_root path=%s", e.Path)
	case "outside_workspace_root":
		return fmt.Sprintf("invalid_workspace_cwd: outside_workspace_root path=%s root=%s", e.Path, e.Root)
	default:
		return fmt.Sprintf("invalid_workspace_cwd: %s path=%s root=%s", e.Kind, e.Path, e.Root)
	}
}

// TurnInputRequiredError reports a hard failure due to required user input.
type TurnInputRequiredError struct {
	Payload map[string]any
}

func (e *TurnInputRequiredError) Error() string {
	return "turn_input_required"
}

// ApprovalRequiredError reports a request that was not auto-approved.
type ApprovalRequiredError struct {
	Payload map[string]any
}

func (e *ApprovalRequiredError) Error() string {
	return "approval_required"
}

// PortExitError reports the Codex subprocess exit status.
type PortExitError struct {
	Status int
}

func (e *PortExitError) Error() string {
	return fmt.Sprintf("port_exit: %d", e.Status)
}

// ResponseTimeoutError reports a timeout waiting for a request/response pair.
type ResponseTimeoutError struct{}

func (e *ResponseTimeoutError) Error() string {
	return "response_timeout"
}

// TurnTimeoutError reports a timed-out turn stream.
type TurnTimeoutError struct{}

func (e *TurnTimeoutError) Error() string {
	return "turn_timeout"
}

// TurnFailedError reports a failed turn notification from Codex.
type TurnFailedError struct {
	Payload map[string]any
}

func (e *TurnFailedError) Error() string {
	return "turn_failed"
}

// TurnCancelledError reports a cancelled turn notification from Codex.
type TurnCancelledError struct {
	Payload map[string]any
}

func (e *TurnCancelledError) Error() string {
	return "turn_cancelled"
}

// Session holds one live app-server thread context.
type Session struct {
	cmd               *exec.Cmd
	stdin             io.WriteCloser
	messages          chan incomingMessage
	waitErr           chan error
	ApprovalPolicy    any
	AutoApprove       bool
	ThreadSandbox     string
	TurnSandboxPolicy map[string]any
	ThreadID          string
	Workspace         string
	WorkerHost        string
	Metadata          map[string]any
}

// RunResult captures the terminal turn/session identifiers.
type RunResult struct {
	SessionID string
	ThreadID  string
	TurnID    string
}

type incomingMessage struct {
	payload map[string]any
	raw     string
	stream  string
	isJSON  bool
}

// ToolExecutor executes a dynamic tool request.
type ToolExecutor func(tool string, arguments map[string]any) map[string]any

// MessageHandler receives normalized Codex event updates.
type MessageHandler func(map[string]any)

// RunOptions configures one run/turn execution.
type RunOptions struct {
	OnMessage    MessageHandler
	ToolExecutor ToolExecutor
	WorkerHost   string
}

// Run starts a session, runs one turn, and always stops the session.
func Run(workspace, prompt string, issue domain.Issue, opts ...RunOptions) (RunResult, error) {
	runOpts := resolveRunOptions(opts...)
	session, err := StartSessionWithHost(workspace, runOpts.WorkerHost)
	if err != nil {
		return RunResult{}, err
	}
	defer StopSession(session)

	return RunTurn(session, prompt, issue, runOpts)
}

// StartSession launches Codex, performs initialize/initialized/thread-start, and returns the live session.
func StartSession(workspace string) (*Session, error) {
	return StartSessionWithHost(workspace, "")
}

// StartSessionWithHost launches Codex locally or over SSH, performs initialize/initialized/thread-start, and returns the live session.
func StartSessionWithHost(workspace, workerHost string) (*Session, error) {
	resolvedWorkspace, err := validateWorkspaceCWD(workspace, workerHost)
	if err != nil {
		return nil, err
	}

	cmd, metadata, runtimeSettings, err := startCommand(resolvedWorkspace, workerHost)
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	if metadata == nil {
		metadata = map[string]any{}
	}
	if cmd.Process != nil {
		metadata["codex_app_server_pid"] = cmd.Process.Pid
	}

	session := &Session{
		cmd:               cmd,
		stdin:             stdin,
		messages:          make(chan incomingMessage, 128),
		waitErr:           make(chan error, 1),
		ApprovalPolicy:    runtimeSettings.ApprovalPolicy,
		AutoApprove:       approvalPolicyIsNever(runtimeSettings.ApprovalPolicy),
		ThreadSandbox:     runtimeSettings.ThreadSandbox,
		TurnSandboxPolicy: runtimeSettings.TurnSandboxPolicy,
		Workspace:         resolvedWorkspace,
		WorkerHost:        workerHost,
		Metadata:          metadata,
	}

	go readStreamLines(stdout, "stdout", session.messages)
	go readStreamLines(stderr, "stderr", session.messages)
	go func() {
		session.waitErr <- cmd.Wait()
	}()

	if err := session.sendJSON(map[string]any{
		"method": "initialize",
		"id":     initializeID,
		"params": map[string]any{
			"capabilities": map[string]any{
				"experimentalApi": true,
			},
			"clientInfo": map[string]any{
				"name":    "symphony-orchestrator",
				"title":   "Symphony Orchestrator",
				"version": "0.1.0",
			},
		},
	}); err != nil {
		return nil, err
	}
	if _, err := session.awaitResponse(initializeID, time.Duration(config.Current().CodexReadTimeoutMS)*time.Millisecond); err != nil {
		return nil, err
	}
	if err := session.sendJSON(map[string]any{
		"method": "initialized",
		"params": map[string]any{},
	}); err != nil {
		return nil, err
	}

	threadPayload, err := session.startThread()
	if err != nil {
		return nil, err
	}
	threadID := nestedString(threadPayload, "thread", "id")
	if threadID == "" {
		return nil, fmt.Errorf("invalid_thread_payload")
	}
	session.ThreadID = threadID

	return session, nil
}

func startCommand(workspace, workerHost string) (*exec.Cmd, map[string]any, config.CodexRuntimeSettings, error) {
	runtimeSettings, err := config.RuntimeCodexSettings(workspace, workerHost != "")
	if err != nil {
		return nil, nil, config.CodexRuntimeSettings{}, err
	}

	if workerHost == "" {
		bashPath, err := exec.LookPath("bash")
		if err != nil {
			return nil, nil, config.CodexRuntimeSettings{}, err
		}

		cmd := exec.Command(bashPath, "-lc", config.Current().CodexCommand)
		cmd.Dir = workspace
		return cmd, map[string]any{}, runtimeSettings, nil
	}

	cmd, err := ssh.StartCommand(workerHost, ssh.FormatRemoteChdirExec(workspace, config.Current().CodexCommand))
	if err != nil {
		return nil, nil, config.CodexRuntimeSettings{}, err
	}

	return cmd, map[string]any{"worker_host": workerHost}, runtimeSettings, nil
}

// StopSession closes the underlying process and pipes.
func StopSession(session *Session) {
	if session == nil {
		return
	}
	if session.stdin != nil {
		_ = session.stdin.Close()
	}
	if session.cmd != nil && session.cmd.Process != nil {
		_ = session.cmd.Process.Kill()
	}
}

// RunTurn starts one turn and waits for completion or a hard failure.
func RunTurn(session *Session, prompt string, issue domain.Issue, opts ...RunOptions) (RunResult, error) {
	cfg := config.Current()
	runOpts := resolveRunOptions(opts...)
	if err := session.sendJSON(map[string]any{
		"method": "turn/start",
		"id":     turnStartID,
		"params": map[string]any{
			"threadId": session.ThreadID,
			"input": []any{
				map[string]any{
					"type": "text",
					"text": prompt,
				},
			},
			"cwd":            session.Workspace,
			"title":          issue.Identifier + ": " + issue.Title,
			"approvalPolicy": session.ApprovalPolicy,
			"sandboxPolicy":  session.TurnSandboxPolicy,
		},
	}); err != nil {
		session.emitStartupFailed(issue, err, runOpts.OnMessage)
		return RunResult{}, err
	}

	turnPayload, err := session.awaitResponse(turnStartID, time.Duration(cfg.CodexReadTimeoutMS)*time.Millisecond)
	if err != nil {
		session.emitStartupFailed(issue, err, runOpts.OnMessage)
		return RunResult{}, err
	}
	turnID := nestedString(turnPayload, "turn", "id")
	if turnID == "" {
		err := fmt.Errorf("invalid_turn_payload")
		session.emitStartupFailed(issue, err, runOpts.OnMessage)
		return RunResult{}, err
	}

	sessionID := session.ThreadID + "-" + turnID
	log.Printf("Codex session started for issue_id=%s issue_identifier=%s session_id=%s", issue.ID, issue.Identifier, sessionID)
	emitMessage(runOpts.OnMessage, map[string]any{
		"event":                "session_started",
		"session_id":           sessionID,
		"thread_id":            session.ThreadID,
		"turn_id":              turnID,
		"codex_app_server_pid": session.Metadata["codex_app_server_pid"],
		"timestamp":            time.Now().UTC(),
	})

	timeout := time.NewTimer(time.Duration(cfg.CodexTurnTimeoutMS) * time.Millisecond)
	defer timeout.Stop()

	for {
		select {
		case message := <-session.messages:
			if !message.isJSON || message.payload == nil {
				logNonJSONStreamLine(message.raw, "turn stream")
				session.emitStreamMessage(message, runOpts.OnMessage)
				continue
			}
			method := stringValue(message.payload["method"])
			switch method {
			case "turn/completed":
				log.Printf("Codex session completed for issue_id=%s issue_identifier=%s session_id=%s", issue.ID, issue.Identifier, sessionID)
				emitMessage(runOpts.OnMessage, map[string]any{
					"event":                "turn_completed",
					"payload":              message.payload,
					"raw":                  message.raw,
					"codex_app_server_pid": session.Metadata["codex_app_server_pid"],
					"timestamp":            time.Now().UTC(),
				})
				session.drainResidualMessages(runOpts.OnMessage)
				return RunResult{SessionID: sessionID, ThreadID: session.ThreadID, TurnID: turnID}, nil
			case "turn/failed":
				emitMessage(runOpts.OnMessage, map[string]any{
					"event":                "turn_failed",
					"payload":              message.payload,
					"raw":                  message.raw,
					"codex_app_server_pid": session.Metadata["codex_app_server_pid"],
					"timestamp":            time.Now().UTC(),
				})
				failedErr := &TurnFailedError{Payload: message.payload}
				session.emitTurnEndedWithError(issue, sessionID, turnErrorReason(failedErr), runOpts.OnMessage)
				session.drainResidualMessages(runOpts.OnMessage)
				return RunResult{}, failedErr
			case "turn/cancelled":
				emitMessage(runOpts.OnMessage, map[string]any{
					"event":                "turn_cancelled",
					"payload":              message.payload,
					"raw":                  message.raw,
					"codex_app_server_pid": session.Metadata["codex_app_server_pid"],
					"timestamp":            time.Now().UTC(),
				})
				cancelledErr := &TurnCancelledError{Payload: message.payload}
				session.emitTurnEndedWithError(issue, sessionID, turnErrorReason(cancelledErr), runOpts.OnMessage)
				session.drainResidualMessages(runOpts.OnMessage)
				return RunResult{}, cancelledErr
			case "turn/input_required", "turn/needs_input", "turn/request_input":
				inputErr := &TurnInputRequiredError{Payload: message.payload}
				session.emitTurnEndedWithError(issue, sessionID, turnErrorReason(inputErr), runOpts.OnMessage)
				return RunResult{}, inputErr
			case "item/tool/requestUserInput":
				outcome, err := session.handleToolRequestUserInput(message.payload, runOpts.OnMessage)
				if err != nil {
					session.emitTurnEndedWithError(issue, sessionID, err.Error(), runOpts.OnMessage)
					return RunResult{}, err
				}
				if outcome == "approval_auto_approved" || outcome == "tool_input_auto_answered" {
					continue
				}
				inputErr := &TurnInputRequiredError{Payload: message.payload}
				session.emitTurnEndedWithError(issue, sessionID, turnErrorReason(inputErr), runOpts.OnMessage)
				return RunResult{}, inputErr
			case "item/tool/call":
				if err := session.handleToolCall(message.payload, runOpts.ToolExecutor, runOpts.OnMessage); err != nil {
					session.emitTurnEndedWithError(issue, sessionID, err.Error(), runOpts.OnMessage)
					return RunResult{}, err
				}
				continue
			case "item/commandExecution/requestApproval", "item/fileChange/requestApproval", "execCommandApproval", "applyPatchApproval":
				if session.AutoApprove {
					if err := session.sendApproval(message.payload, approvalDecision(method)); err != nil {
						return RunResult{}, err
					}
					emitMessage(runOpts.OnMessage, map[string]any{
						"event":                "approval_auto_approved",
						"payload":              message.payload,
						"decision":             approvalDecision(method),
						"codex_app_server_pid": session.Metadata["codex_app_server_pid"],
						"timestamp":            time.Now().UTC(),
					})
					continue
				}
				approvalErr := &ApprovalRequiredError{Payload: message.payload}
				session.emitTurnEndedWithError(issue, sessionID, turnErrorReason(approvalErr), runOpts.OnMessage)
				return RunResult{}, approvalErr
			default:
				session.emitStreamMessage(message, runOpts.OnMessage)
				continue
			}
		case err := <-session.waitErr:
			if err == nil {
				portErr := &PortExitError{Status: 0}
				session.emitTurnEndedWithError(issue, sessionID, portErr.Error(), runOpts.OnMessage)
				return RunResult{}, portErr
			}
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				portErr := &PortExitError{Status: exitErr.ExitCode()}
				session.emitTurnEndedWithError(issue, sessionID, portErr.Error(), runOpts.OnMessage)
				return RunResult{}, portErr
			}
			session.emitTurnEndedWithError(issue, sessionID, err.Error(), runOpts.OnMessage)
			return RunResult{}, err
		case <-timeout.C:
			timeoutErr := &TurnTimeoutError{}
			session.emitTurnEndedWithError(issue, sessionID, timeoutErr.Error(), runOpts.OnMessage)
			return RunResult{}, timeoutErr
		}
	}
}

func (s *Session) startThread() (map[string]any, error) {
	if err := s.sendJSON(map[string]any{
		"method": "thread/start",
		"id":     threadStartID,
		"params": map[string]any{
			"approvalPolicy": s.ApprovalPolicy,
			"sandbox":        s.ThreadSandbox,
			"cwd":            s.Workspace,
			"dynamicTools":   dynamicToolSpecs(),
		},
	}); err != nil {
		return nil, err
	}

	return s.awaitResponse(threadStartID, time.Duration(config.Current().CodexReadTimeoutMS)*time.Millisecond)
}

func (s *Session) awaitResponse(requestID int, timeout time.Duration) (map[string]any, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	for {
		select {
		case message := <-s.messages:
			if !message.isJSON || message.payload == nil {
				logNonJSONStreamLine(message.raw, "response stream")
				continue
			}
			id, ok := intValue(message.payload["id"])
			if !ok || id != requestID {
				continue
			}
			if result, ok := message.payload["result"].(map[string]any); ok {
				return result, nil
			}
			if errorPayload, ok := message.payload["error"].(map[string]any); ok {
				return nil, fmt.Errorf("response_error: %v", errorPayload)
			}
			return nil, fmt.Errorf("response_error")
		case err := <-s.waitErr:
			if err == nil {
				return nil, &PortExitError{Status: 0}
			}
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				return nil, &PortExitError{Status: exitErr.ExitCode()}
			}
			return nil, err
		case <-timer.C:
			return nil, &ResponseTimeoutError{}
		}
	}
}

func (s *Session) sendApproval(payload map[string]any, decision string) error {
	id, ok := payload["id"]
	if !ok {
		return nil
	}
	return s.sendJSON(map[string]any{
		"id": id,
		"result": map[string]any{
			"decision": decision,
		},
	})
}

func (s *Session) handleToolCall(payload map[string]any, executor ToolExecutor, onMessage MessageHandler) error {
	id, ok := payload["id"]
	if !ok {
		return nil
	}
	params, _ := payload["params"].(map[string]any)
	tool := toolCallName(params)
	arguments := toolCallArguments(params)

	result := executor(tool, arguments)
	if err := s.sendJSON(map[string]any{
		"id":     id,
		"result": result,
	}); err != nil {
		return err
	}

	event := "tool_call_failed"
	switch {
	case tool == "":
		event = "unsupported_tool_call"
	case boolValue(result["success"]):
		event = "tool_call_completed"
	}
	emitMessage(onMessage, map[string]any{
		"event":                event,
		"payload":              payload,
		"raw":                  payload,
		"codex_app_server_pid": s.Metadata["codex_app_server_pid"],
		"timestamp":            time.Now().UTC(),
	})
	return nil
}

func (s *Session) handleToolRequestUserInput(payload map[string]any, onMessage MessageHandler) (string, error) {
	id, ok := payload["id"]
	if !ok {
		return "", nil
	}
	params, _ := payload["params"].(map[string]any)

	if s.AutoApprove {
		if answers, decision, ok := approvalAnswers(params); ok {
			if err := s.sendJSON(map[string]any{
				"id": id,
				"result": map[string]any{
					"answers": answers,
				},
			}); err != nil {
				return "", err
			}
			emitMessage(onMessage, map[string]any{
				"event":                "approval_auto_approved",
				"payload":              payload,
				"decision":             decision,
				"codex_app_server_pid": s.Metadata["codex_app_server_pid"],
				"timestamp":            time.Now().UTC(),
			})
			return "approval_auto_approved", nil
		}
	}

	if answers, ok := genericAnswers(params); ok {
		if err := s.sendJSON(map[string]any{
			"id": id,
			"result": map[string]any{
				"answers": answers,
			},
		}); err != nil {
			return "", err
		}
		emitMessage(onMessage, map[string]any{
			"event":                "tool_input_auto_answered",
			"payload":              payload,
			"answer":               nonInteractiveToolInputAnswer,
			"codex_app_server_pid": s.Metadata["codex_app_server_pid"],
			"timestamp":            time.Now().UTC(),
		})
		return "tool_input_auto_answered", nil
	}

	return "", nil
}

func (s *Session) sendJSON(payload map[string]any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = s.stdin.Write(append(encoded, '\n'))
	return err
}

func validateWorkspaceCWD(workspace, workerHost string) (string, error) {
	if workerHost != "" {
		switch {
		case strings.TrimSpace(workspace) == "":
			return "", &InvalidWorkspaceCwdError{Kind: "empty_remote_workspace", Path: workspace, Root: workerHost}
		case strings.Contains(workspace, "\n"), strings.Contains(workspace, "\r"), strings.ContainsRune(workspace, rune(0)):
			return "", &InvalidWorkspaceCwdError{Kind: "invalid_remote_workspace", Path: workspace, Root: workerHost}
		default:
			return workspace, nil
		}
	}

	expandedWorkspace, err := filepath.Abs(workspace)
	if err != nil {
		return "", err
	}
	expandedRoot, err := filepath.Abs(config.LocalWorkspaceRoot())
	if err != nil {
		return "", err
	}

	canonicalWorkspace, err := pathsafety.Canonicalize(expandedWorkspace)
	if err != nil {
		return "", &InvalidWorkspaceCwdError{Kind: "path_unreadable", Path: expandedWorkspace, Root: err.Error()}
	}
	canonicalRoot, err := pathsafety.Canonicalize(expandedRoot)
	if err != nil {
		return "", &InvalidWorkspaceCwdError{Kind: "path_unreadable", Path: expandedRoot, Root: err.Error()}
	}

	if canonicalWorkspace == canonicalRoot {
		return "", &InvalidWorkspaceCwdError{Kind: "workspace_root", Path: canonicalWorkspace}
	}
	if relative, err := filepath.Rel(canonicalRoot, canonicalWorkspace); err == nil {
		if relative != "." && relative != "" && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return canonicalWorkspace, nil
		}
	}

	if relative, err := filepath.Rel(expandedRoot, expandedWorkspace); err == nil {
		if relative != "." && relative != "" && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return "", &InvalidWorkspaceCwdError{Kind: "symlink_escape", Path: expandedWorkspace, Root: canonicalRoot}
		}
	}

	return "", &InvalidWorkspaceCwdError{Kind: "outside_workspace_root", Path: canonicalWorkspace, Root: canonicalRoot}
}

func approvalPolicyIsNever(value any) bool {
	raw, ok := value.(string)
	return ok && strings.TrimSpace(raw) == "never"
}

func approvalDecision(method string) string {
	switch method {
	case "execCommandApproval", "applyPatchApproval":
		return "approved_for_session"
	default:
		return "acceptForSession"
	}
}

func dynamicToolSpecs() []any {
	return []any{
		map[string]any{
			"name":        "linear_graphql",
			"description": "Execute a raw GraphQL query or mutation against Linear using Symphony's configured auth.\n",
			"inputSchema": map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []any{"query"},
				"properties": map[string]any{
					"query": map[string]any{
						"type":        "string",
						"description": "GraphQL query or mutation document to execute against Linear.",
					},
					"variables": map[string]any{
						"type":                 []any{"object", "null"},
						"description":          "Optional GraphQL variables object.",
						"additionalProperties": true,
					},
				},
			},
		},
	}
}

func resolveRunOptions(opts ...RunOptions) RunOptions {
	var resolved RunOptions
	if len(opts) > 0 {
		resolved = opts[0]
	}
	if resolved.ToolExecutor == nil {
		resolved.ToolExecutor = defaultToolExecutor
	}
	return resolved
}

func defaultToolExecutor(tool string, arguments map[string]any) map[string]any {
	if tool != "linear_graphql" {
		return toolFailureResponse(map[string]any{
			"error": map[string]any{
				"message":        fmt.Sprintf("Unsupported dynamic tool: %q.", tool),
				"supportedTools": []any{"linear_graphql"},
			},
		})
	}

	query, variables, err := normalizeLinearGraphQLArguments(arguments)
	if err != nil {
		return toolFailureResponse(map[string]any{
			"error": map[string]any{
				"message": err.Error(),
			},
		})
	}

	client := linear.NewClient()
	response, gqlErr := client.GraphQL(query, variables, "")
	if gqlErr != nil {
		return toolFailureResponse(linearGraphQLToolErrorPayload(gqlErr))
	}
	success := true
	if errorsPayload, ok := response["errors"].([]any); ok && len(errorsPayload) > 0 {
		success = false
	}
	return map[string]any{
		"success": success,
		"contentItems": []any{
			map[string]any{
				"type": "inputText",
				"text": prettyJSON(response),
			},
		},
	}
}

func toolFailureResponse(payload map[string]any) map[string]any {
	return map[string]any{
		"success": false,
		"contentItems": []any{
			map[string]any{
				"type": "inputText",
				"text": prettyJSON(payload),
			},
		},
	}
}

func linearGraphQLToolErrorPayload(reason error) map[string]any {
	switch typed := reason.(type) {
	case nil:
		return map[string]any{
			"error": map[string]any{
				"message": "Linear GraphQL tool execution failed.",
			},
		}
	case *linear.StatusError:
		return map[string]any{
			"error": map[string]any{
				"message": fmt.Sprintf("Linear GraphQL request failed with HTTP %d.", typed.Status),
				"status":  typed.Status,
			},
		}
	case *linear.RequestError:
		return map[string]any{
			"error": map[string]any{
				"message": "Linear GraphQL request failed before receiving a successful response.",
				"reason":  fmt.Sprintf("%v", typed.Reason),
			},
		}
	default:
		if errors.Is(reason, config.ErrMissingLinearAPIToken) {
			return map[string]any{
				"error": map[string]any{
					"message": "Symphony is missing Linear auth. Set `linear.api_key` in `WORKFLOW.md` or export `LINEAR_API_KEY`.",
				},
			}
		}
		return map[string]any{
			"error": map[string]any{
				"message": "Linear GraphQL tool execution failed.",
				"reason":  fmt.Sprintf("%v", reason),
			},
		}
	}
}

func prettyJSON(payload map[string]any) string {
	encoded, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return fmt.Sprint(payload)
	}
	return string(encoded)
}

func normalizeLinearGraphQLArguments(arguments map[string]any) (string, map[string]any, error) {
	query := strings.TrimSpace(stringValue(arguments["query"]))
	if query == "" {
		return "", nil, errors.New("`linear_graphql` requires a non-empty `query` string.")
	}
	rawVariables, ok := arguments["variables"]
	if !ok || rawVariables == nil {
		return query, map[string]any{}, nil
	}
	variables, ok := rawVariables.(map[string]any)
	if !ok {
		return "", nil, errors.New("`linear_graphql.variables` must be a JSON object when provided.")
	}
	return query, variables, nil
}

func approvalAnswers(params map[string]any) (map[string]any, string, bool) {
	questions, ok := params["questions"].([]any)
	if !ok || len(questions) == 0 {
		return nil, "", false
	}
	answers := map[string]any{}
	for _, rawQuestion := range questions {
		question, ok := rawQuestion.(map[string]any)
		if !ok {
			return nil, "", false
		}
		questionID := stringValue(question["id"])
		options, _ := question["options"].([]any)
		label := approvalOptionLabel(options)
		if questionID == "" || label == "" {
			return nil, "", false
		}
		answers[questionID] = map[string]any{"answers": []any{label}}
	}
	return answers, "Approve this Session", true
}

func approvalOptionLabel(options []any) string {
	var labels []string
	for _, rawOption := range options {
		option, _ := rawOption.(map[string]any)
		label := stringValue(option["label"])
		if label != "" {
			labels = append(labels, label)
		}
	}
	for _, preferred := range []string{"Approve this Session", "Approve Once"} {
		for _, label := range labels {
			if label == preferred {
				return label
			}
		}
	}
	for _, label := range labels {
		normalized := strings.ToLower(strings.TrimSpace(label))
		if strings.HasPrefix(normalized, "approve") || strings.HasPrefix(normalized, "allow") {
			return label
		}
	}
	return ""
}

func genericAnswers(params map[string]any) (map[string]any, bool) {
	questions, ok := params["questions"].([]any)
	if !ok || len(questions) == 0 {
		return nil, false
	}
	answers := map[string]any{}
	for _, rawQuestion := range questions {
		question, ok := rawQuestion.(map[string]any)
		if !ok {
			return nil, false
		}
		questionID := stringValue(question["id"])
		if questionID == "" {
			return nil, false
		}
		answers[questionID] = map[string]any{
			"answers": []any{nonInteractiveToolInputAnswer},
		}
	}
	return answers, true
}

func toolCallName(params map[string]any) string {
	for _, key := range []string{"tool", "name"} {
		if value := strings.TrimSpace(stringValue(params[key])); value != "" {
			return value
		}
	}
	return ""
}

func toolCallArguments(params map[string]any) map[string]any {
	arguments, _ := params["arguments"].(map[string]any)
	if arguments == nil {
		return map[string]any{}
	}
	return arguments
}

func emitMessage(handler MessageHandler, payload map[string]any) {
	if handler != nil {
		handler(payload)
	}
}

func (s *Session) emitStartupFailed(issue domain.Issue, reason error, onMessage MessageHandler) {
	log.Printf("Codex session failed for issue_id=%s issue_identifier=%s: %v", issue.ID, issue.Identifier, reason)
	emitMessage(onMessage, map[string]any{
		"event":                "startup_failed",
		"reason":               reason.Error(),
		"codex_app_server_pid": s.Metadata["codex_app_server_pid"],
		"timestamp":            time.Now().UTC(),
	})
}

func (s *Session) emitTurnEndedWithError(issue domain.Issue, sessionID string, reason any, onMessage MessageHandler) {
	log.Printf("Codex session ended with error for issue_id=%s issue_identifier=%s session_id=%s: %v", issue.ID, issue.Identifier, sessionID, reason)
	emitMessage(onMessage, map[string]any{
		"event":                "turn_ended_with_error",
		"session_id":           sessionID,
		"reason":               reason,
		"codex_app_server_pid": s.Metadata["codex_app_server_pid"],
		"timestamp":            time.Now().UTC(),
	})
}

func (s *Session) emitStreamMessage(message incomingMessage, onMessage MessageHandler) {
	if !message.isJSON || message.payload == nil {
		if !protocolMessageCandidate(message.raw) {
			return
		}
		emitMessage(onMessage, map[string]any{
			"event":                "malformed",
			"payload":              message.raw,
			"raw":                  message.raw,
			"stream":               message.stream,
			"codex_app_server_pid": s.Metadata["codex_app_server_pid"],
			"timestamp":            time.Now().UTC(),
		})
		return
	}

	event := "notification"
	if stringValue(message.payload["method"]) == "" {
		event = "other_message"
	}
	emitMessage(onMessage, map[string]any{
		"event":                event,
		"payload":              message.payload,
		"raw":                  message.raw,
		"codex_app_server_pid": s.Metadata["codex_app_server_pid"],
		"timestamp":            time.Now().UTC(),
	})
}

func (s *Session) drainResidualMessages(onMessage MessageHandler) {
	if onMessage == nil {
		return
	}

	timer := time.NewTimer(turnTailDrainQuiet)
	defer timer.Stop()

	for {
		select {
		case message := <-s.messages:
			if !message.isJSON || message.payload == nil {
				logNonJSONStreamLine(message.raw, "turn stream")
			}
			s.emitStreamMessage(message, onMessage)
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(turnTailDrainQuiet)
		case <-timer.C:
			return
		}
	}
}

func protocolMessageCandidate(data string) bool {
	return strings.HasPrefix(strings.TrimLeftFunc(data, unicode.IsSpace), "{")
}

func readStreamLines(reader io.Reader, stream string, messages chan<- incomingMessage) {
	buffered := bufio.NewReader(reader)
	for {
		line, err := buffered.ReadString('\n')
		line = strings.TrimRight(line, "\r\n")
		if line != "" {
			message := incomingMessage{raw: line, stream: stream}
			var payload map[string]any
			if err := json.Unmarshal([]byte(line), &payload); err == nil {
				message.payload = payload
				message.isJSON = true
			}
			messages <- message
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return
			}
			return
		}
	}
}

func turnErrorReason(err error) any {
	switch typed := err.(type) {
	case *TurnFailedError:
		return typed.Payload
	case *TurnCancelledError:
		return typed.Payload
	case *TurnInputRequiredError:
		return typed.Payload
	case *ApprovalRequiredError:
		return typed.Payload
	default:
		if err != nil {
			return err.Error()
		}
		return nil
	}
}

func logNonJSONStreamLine(line, streamLabel string) {
	text := strings.TrimSpace(line)
	if text == "" {
		return
	}
	if len(text) > maxStreamLogBytes {
		text = text[:maxStreamLogBytes] + "..."
	}
	log.Printf("Codex %s output: %s", streamLabel, text)
}

func nestedString(root map[string]any, path ...string) string {
	current := any(root)
	for _, segment := range path {
		node, ok := current.(map[string]any)
		if !ok {
			return ""
		}
		current, ok = node[segment]
		if !ok {
			return ""
		}
	}
	return stringValue(current)
}

func stringValue(value any) string {
	raw, _ := value.(string)
	return raw
}

func intValue(value any) (int, bool) {
	switch typed := value.(type) {
	case float64:
		return int(typed), true
	case int:
		return typed, true
	default:
		return 0, false
	}
}

func boolValue(value any) bool {
	raw, _ := value.(bool)
	return raw
}
