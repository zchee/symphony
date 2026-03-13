package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/openai/symphony/go/internal/pathsafety"
	"github.com/openai/symphony/go/internal/runtimeconfig"
	"github.com/openai/symphony/go/internal/workflow"
)

var (
	defaultActiveStates                 = []string{"Todo", "In Progress"}
	defaultTerminalStates               = []string{"Closed", "Cancelled", "Canceled", "Duplicate", "Done"}
	defaultLinearEndpoint               = "https://api.linear.app/graphql"
	defaultPromptTemplate               = "You are working on a Linear issue.\n\nIdentifier: {{ issue.identifier }}\nTitle: {{ issue.title }}\n\nBody:\n{% if issue.description %}\n{{ issue.description }}\n{% else %}\nNo description provided.\n{% endif %}"
	defaultPollIntervalMS               = 30_000
	defaultWorkspaceRoot                = filepath.Join(os.TempDir(), "symphony_workspaces")
	defaultHookTimeoutMS                = 60_000
	defaultMaxConcurrentAgents          = 10
	defaultAgentMaxTurns                = 20
	defaultMaxRetryBackoffMS            = 300_000
	defaultCodexCommand                 = "codex app-server"
	defaultCodexTurnTimeoutMS           = 3_600_000
	defaultCodexReadTimeoutMS           = 5_000
	defaultCodexStallTimeoutMS          = 300_000
	defaultCodexThreadSandbox           = "workspace-write"
	defaultObservabilityEnabled         = true
	defaultObservabilityRefreshMS       = 1_000
	defaultObservabilityRenderMS        = 16
	defaultServerHost                   = "127.0.0.1"
	defaultCodexApprovalPolicy    any   = map[string]any{"reject": map[string]any{"sandbox_approval": true, "rules": true, "mcp_elicitations": true}}
	ErrMissingTrackerKind         error = errors.New("missing_tracker_kind")
	ErrMissingLinearAPIToken      error = errors.New("missing_linear_api_token")
	ErrMissingLinearProjectSlug   error = errors.New("missing_linear_project_slug")
	ErrMissingCodexCommand        error = errors.New("missing_codex_command")
)

// UnsupportedTrackerKindError reports a tracker kind that the current port does not support.
type UnsupportedTrackerKindError struct {
	Kind string
}

func (e *UnsupportedTrackerKindError) Error() string {
	return fmt.Sprintf("unsupported_tracker_kind: %s", e.Kind)
}

// InvalidCodexApprovalPolicyError reports a non-string/non-map approval policy value.
type InvalidCodexApprovalPolicyError struct {
	Value any
}

func (e *InvalidCodexApprovalPolicyError) Error() string {
	return fmt.Sprintf("invalid_codex_approval_policy: %v", e.Value)
}

// InvalidCodexThreadSandboxError reports a non-string thread sandbox value.
type InvalidCodexThreadSandboxError struct {
	Value any
}

func (e *InvalidCodexThreadSandboxError) Error() string {
	return fmt.Sprintf("invalid_codex_thread_sandbox: %v", e.Value)
}

// InvalidCodexTurnSandboxPolicyError reports an unsupported turn sandbox payload.
type InvalidCodexTurnSandboxPolicyError struct {
	Reason any
}

func (e *InvalidCodexTurnSandboxPolicyError) Error() string {
	return fmt.Sprintf("invalid_codex_turn_sandbox_policy: %v", e.Reason)
}

// Hooks holds the configured workspace lifecycle commands.
type Hooks struct {
	AfterCreate  string
	BeforeRun    string
	AfterRun     string
	BeforeRemove string
	TimeoutMS    int
}

// CodexRuntimeSettings is the turn-time Codex session configuration resolved for a concrete workspace.
type CodexRuntimeSettings struct {
	ApprovalPolicy    any
	ThreadSandbox     string
	TurnSandboxPolicy map[string]any
}

// Effective is the typed workflow-backed runtime configuration view.
type Effective struct {
	TrackerKind                      string
	LinearEndpoint                   string
	LinearAPIToken                   string
	LinearProjectSlug                string
	LinearAssignee                   string
	LinearActiveStates               []string
	LinearTerminalStates             []string
	PollIntervalMS                   int
	WorkspaceRoot                    string
	WorkerSSHHosts                   []string
	WorkerMaxConcurrentAgentsPerHost *int
	Hooks                            Hooks
	MaxConcurrentAgents              int
	MaxRetryBackoffMS                int
	AgentMaxTurns                    int
	MaxConcurrentAgentsByState       map[string]int
	CodexCommand                     string
	CodexTurnTimeoutMS               int
	CodexApprovalPolicy              any
	CodexThreadSandbox               string
	CodexTurnSandboxPolicy           map[string]any
	CodexReadTimeoutMS               int
	CodexStallTimeoutMS              int
	WorkflowPrompt                   string
	ObservabilityEnabled             bool
	ObservabilityRefreshMS           int
	ObservabilityRenderIntervalMS    int
	ServerPort                       *int
	ServerHost                       string
}

// MaxConcurrentAgentsForState returns the state-specific concurrency override when present.
func (e Effective) MaxConcurrentAgentsForState(state string) int {
	if state == "" {
		return e.MaxConcurrentAgents
	}

	if limit, ok := e.MaxConcurrentAgentsByState[normalizeIssueState(state)]; ok {
		return limit
	}

	return e.MaxConcurrentAgents
}

// Current returns the current effective configuration, falling back to defaults on workflow load failures.
func Current() Effective {
	loaded, err := loadCurrentWorkflow()
	if err != nil {
		return effectiveFromLoaded(workflow.Loaded{})
	}

	return effectiveFromLoaded(loaded)
}

// Validate checks that the current workflow is loadable and that required runtime fields are coherent.
func Validate() error {
	loaded, err := loadCurrentWorkflow()
	if err != nil {
		return err
	}

	cfg := normalizeMap(loaded.Config)
	current := effectiveFromLoaded(loaded)

	switch current.TrackerKind {
	case "":
		return ErrMissingTrackerKind
	case "linear":
		if current.LinearAPIToken == "" {
			return ErrMissingLinearAPIToken
		}
		if current.LinearProjectSlug == "" {
			return ErrMissingLinearProjectSlug
		}
	case "memory":
	default:
		return &UnsupportedTrackerKindError{Kind: current.TrackerKind}
	}

	if strings.TrimSpace(current.CodexCommand) == "" {
		return ErrMissingCodexCommand
	}

	if err := validateCodexApprovalPolicy(rawNestedValue(cfg, "codex", "approval_policy")); err != nil {
		return err
	}

	if err := validateCodexThreadSandbox(rawNestedValue(cfg, "codex", "thread_sandbox")); err != nil {
		return err
	}

	if err := validateCodexTurnSandboxPolicy(rawNestedValue(cfg, "codex", "turn_sandbox_policy")); err != nil {
		return err
	}
	if err := validateStringList(rawNestedValue(cfg, "tracker", "active_states"), "tracker.active_states"); err != nil {
		return err
	}
	if err := validateStringList(rawNestedValue(cfg, "tracker", "terminal_states"), "tracker.terminal_states"); err != nil {
		return err
	}
	if err := validatePositiveOptionalInt(rawNestedValue(cfg, "worker", "max_concurrent_agents_per_host"), "worker.max_concurrent_agents_per_host"); err != nil {
		return err
	}

	return nil
}

func loadCurrentWorkflow() (workflow.Loaded, error) {
	return workflow.Current()
}

func effectiveFromLoaded(loaded workflow.Loaded) Effective {
	cfg := normalizeMap(loaded.Config)

	effective := Effective{
		TrackerKind:                      normalizeTrackerKind(scalarString(rawNestedValue(cfg, "tracker", "kind"))),
		LinearEndpoint:                   firstNonEmptyString(scalarString(rawNestedValue(cfg, "tracker", "endpoint")), defaultLinearEndpoint),
		LinearAPIToken:                   resolveSecret(rawNestedValue(cfg, "tracker", "api_key"), "LINEAR_API_KEY"),
		LinearProjectSlug:                normalizeSecretValue(scalarString(rawNestedValue(cfg, "tracker", "project_slug"))),
		LinearAssignee:                   resolveSecret(rawNestedValue(cfg, "tracker", "assignee"), "LINEAR_ASSIGNEE"),
		LinearActiveStates:               valueOrDefaultStringSlice(csvValue(rawNestedValue(cfg, "tracker", "active_states")), defaultActiveStates),
		LinearTerminalStates:             valueOrDefaultStringSlice(csvValue(rawNestedValue(cfg, "tracker", "terminal_states")), defaultTerminalStates),
		PollIntervalMS:                   positiveIntOrDefault(rawNestedValue(cfg, "polling", "interval_ms"), defaultPollIntervalMS),
		WorkspaceRoot:                    resolvePathValue(rawNestedValue(cfg, "workspace", "root"), defaultWorkspaceRoot),
		WorkerSSHHosts:                   workerSSHHosts(rawNestedValue(cfg, "worker", "ssh_hosts")),
		WorkerMaxConcurrentAgentsPerHost: optionalPositiveInt(rawNestedValue(cfg, "worker", "max_concurrent_agents_per_host")),
		Hooks:                            hooksFromConfig(cfg),
		MaxConcurrentAgents:              positiveIntOrDefault(rawNestedValue(cfg, "agent", "max_concurrent_agents"), defaultMaxConcurrentAgents),
		MaxRetryBackoffMS:                positiveIntOrDefault(rawNestedValue(cfg, "agent", "max_retry_backoff_ms"), defaultMaxRetryBackoffMS),
		AgentMaxTurns:                    positiveIntOrDefault(rawNestedValue(cfg, "agent", "max_turns"), defaultAgentMaxTurns),
		MaxConcurrentAgentsByState:       stateLimits(rawNestedValue(cfg, "agent", "max_concurrent_agents_by_state")),
		CodexCommand:                     commandOrDefault(rawNestedValue(cfg, "codex", "command"), defaultCodexCommand),
		CodexTurnTimeoutMS:               intOrDefault(rawNestedValue(cfg, "codex", "turn_timeout_ms"), defaultCodexTurnTimeoutMS),
		CodexApprovalPolicy:              codexApprovalPolicy(rawNestedValue(cfg, "codex", "approval_policy")),
		CodexThreadSandbox:               codexThreadSandbox(rawNestedValue(cfg, "codex", "thread_sandbox")),
		CodexReadTimeoutMS:               intOrDefault(rawNestedValue(cfg, "codex", "read_timeout_ms"), defaultCodexReadTimeoutMS),
		CodexStallTimeoutMS:              nonNegativeIntOrDefault(rawNestedValue(cfg, "codex", "stall_timeout_ms"), defaultCodexStallTimeoutMS),
		WorkflowPrompt:                   workflowPrompt(loaded.PromptTemplate),
		ObservabilityEnabled:             boolOrDefault(rawNestedValue(cfg, "observability", "dashboard_enabled"), defaultObservabilityEnabled),
		ObservabilityRefreshMS:           intOrDefault(rawNestedValue(cfg, "observability", "refresh_ms"), defaultObservabilityRefreshMS),
		ObservabilityRenderIntervalMS:    intOrDefault(rawNestedValue(cfg, "observability", "render_interval_ms"), defaultObservabilityRenderMS),
		ServerPort:                       serverPortValue(rawNestedValue(cfg, "server", "port")),
		ServerHost:                       serverHost(rawNestedValue(cfg, "server", "host")),
	}

	if overridePort, ok := runtimeconfig.ServerPortOverride(); ok {
		effective.ServerPort = intPointer(overridePort)
	}

	effective.CodexTurnSandboxPolicy = codexTurnSandboxPolicy(rawNestedValue(cfg, "codex", "turn_sandbox_policy"), effective.WorkspaceRoot)

	return effective
}

// RuntimeCodexSettings resolves the current Codex runtime settings for one workspace path.
func RuntimeCodexSettings(workspace string, remote bool) (CodexRuntimeSettings, error) {
	current := Current()
	policy, err := resolveRuntimeTurnSandboxPolicy(current, workspace, remote)
	if err != nil {
		return CodexRuntimeSettings{}, err
	}

	return CodexRuntimeSettings{
		ApprovalPolicy:    current.CodexApprovalPolicy,
		ThreadSandbox:     current.CodexThreadSandbox,
		TurnSandboxPolicy: policy,
	}, nil
}

// LocalWorkspaceRoot returns the current workspace root expanded for local filesystem use.
func LocalWorkspaceRoot() string {
	return expandedLocalWorkspaceRoot(Current().WorkspaceRoot)
}

func hooksFromConfig(cfg map[string]any) Hooks {
	return Hooks{
		AfterCreate:  hookCommand(rawNestedValue(cfg, "hooks", "after_create")),
		BeforeRun:    hookCommand(rawNestedValue(cfg, "hooks", "before_run")),
		AfterRun:     hookCommand(rawNestedValue(cfg, "hooks", "after_run")),
		BeforeRemove: hookCommand(rawNestedValue(cfg, "hooks", "before_remove")),
		TimeoutMS:    positiveIntOrDefault(rawNestedValue(cfg, "hooks", "timeout_ms"), defaultHookTimeoutMS),
	}
}

func validateCodexApprovalPolicy(value any) error {
	switch normalizeAny(value).(type) {
	case nil:
		return nil
	case string:
		return nil
	case map[string]any:
		return nil
	default:
		return &InvalidCodexApprovalPolicyError{Value: value}
	}
}

func validateCodexThreadSandbox(value any) error {
	switch normalizeAny(value).(type) {
	case nil:
		return nil
	case string:
		return nil
	default:
		return &InvalidCodexThreadSandboxError{Value: value}
	}
}

func validateCodexTurnSandboxPolicy(value any) error {
	switch normalizeAny(value).(type) {
	case nil, map[string]any:
		return nil
	default:
		return &InvalidCodexTurnSandboxPolicyError{Reason: value}
	}
}

func commandOrDefault(value any, fallback string) string {
	raw := scalarString(value)
	if strings.TrimSpace(raw) == "" {
		return fallback
	}

	return strings.TrimSpace(raw)
}

func hookCommand(value any) string {
	raw, ok := value.(string)
	if !ok {
		return ""
	}

	if strings.TrimSpace(raw) == "" {
		return ""
	}

	return strings.TrimRight(raw, "\n")
}

func codexApprovalPolicy(value any) any {
	switch typed := normalizeAny(value).(type) {
	case nil:
		return cloneAny(defaultCodexApprovalPolicy)
	case string:
		return strings.TrimSpace(typed)
	case map[string]any:
		return cloneAny(typed)
	default:
		return cloneAny(defaultCodexApprovalPolicy)
	}
}

func codexThreadSandbox(value any) string {
	if value == nil {
		return defaultCodexThreadSandbox
	}

	return strings.TrimSpace(scalarString(value))
}

func codexTurnSandboxPolicy(value any, _ string) map[string]any {
	if normalized, ok := normalizeAny(value).(map[string]any); ok {
		return cloneAny(normalized).(map[string]any)
	}
	return nil
}

func defaultTurnSandboxPolicy(workspaceRoot string) map[string]any {
	return map[string]any{
		"type":                "workspaceWrite",
		"writableRoots":       []any{workspaceRoot},
		"readOnlyAccess":      map[string]any{"type": "fullAccess"},
		"networkAccess":       false,
		"excludeTmpdirEnvVar": false,
		"excludeSlashTmp":     false,
	}
}

func workflowPrompt(prompt string) string {
	if strings.TrimSpace(prompt) == "" {
		return defaultPromptTemplate
	}

	return prompt
}

func serverPortValue(value any) *int {
	parsed, ok := parseNonNegativeInt(value)
	if !ok {
		return nil
	}

	return intPointer(parsed)
}

func serverHost(value any) string {
	raw := scalarString(value)
	if raw == "" {
		return defaultServerHost
	}

	return raw
}

func stateLimits(value any) map[string]int {
	normalized, ok := normalizeAny(value).(map[string]any)
	if !ok {
		return map[string]int{}
	}

	result := make(map[string]int, len(normalized))
	for key, raw := range normalized {
		if parsed, ok := parsePositiveInt(raw); ok {
			result[normalizeIssueState(key)] = parsed
		}
	}

	return result
}

func optionalPositiveInt(value any) *int {
	parsed, ok := parsePositiveInt(value)
	if !ok {
		return nil
	}

	return intPointer(parsed)
}

func resolveSecret(value any, fallbackEnv string) string {
	raw := scalarString(value)
	fallback := normalizeSecretValue(os.Getenv(fallbackEnv))

	if raw == "" {
		return fallback
	}

	if envName, ok := envRefName(raw); ok {
		resolved := normalizeSecretValue(os.Getenv(envName))
		if resolved == "" {
			return fallback
		}
		return resolved
	}

	return normalizeSecretValue(raw)
}

func resolvePathValue(value any, fallback string) string {
	raw, ok := value.(string)
	if !ok {
		return fallback
	}

	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback
	}

	if envName, ok := envRefName(raw); ok {
		resolved := os.Getenv(envName)
		if strings.TrimSpace(resolved) == "" {
			return fallback
		}
		raw = strings.TrimSpace(resolved)
	}

	if strings.HasPrefix(raw, "~") {
		return raw
	}

	return raw
}

func rawNestedValue(config map[string]any, path ...string) any {
	current := any(config)
	for _, segment := range path {
		section, ok := current.(map[string]any)
		if !ok {
			return nil
		}

		next, ok := section[segment]
		if !ok {
			return nil
		}

		current = next
	}

	return current
}

func normalizeAny(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		normalized := make(map[string]any, len(typed))
		for key, nested := range typed {
			normalized[key] = normalizeAny(nested)
		}
		return normalized
	case map[any]any:
		normalized := make(map[string]any, len(typed))
		for key, nested := range typed {
			normalized[fmt.Sprint(key)] = normalizeAny(nested)
		}
		return normalized
	case []any:
		normalized := make([]any, len(typed))
		for index, nested := range typed {
			normalized[index] = normalizeAny(nested)
		}
		return normalized
	case []string:
		normalized := make([]any, len(typed))
		for index, nested := range typed {
			normalized[index] = nested
		}
		return normalized
	default:
		return typed
	}
}

func normalizeMap(value map[string]any) map[string]any {
	normalized, ok := normalizeAny(value).(map[string]any)
	if !ok {
		return map[string]any{}
	}

	return normalized
}

func cloneAny(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		cloned := make(map[string]any, len(typed))
		for key, nested := range typed {
			cloned[key] = cloneAny(nested)
		}
		return cloned
	case []any:
		cloned := make([]any, len(typed))
		for index, nested := range typed {
			cloned[index] = cloneAny(nested)
		}
		return cloned
	default:
		return typed
	}
}

func scalarString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(typed)
	case bool:
		return fmt.Sprintf("%t", typed)
	case int:
		return fmt.Sprintf("%d", typed)
	case int64:
		return fmt.Sprintf("%d", typed)
	case float64:
		return strings.TrimSpace(fmt.Sprintf("%v", typed))
	default:
		return ""
	}
}

func normalizeTrackerKind(value string) string {
	return normalizeIssueState(value)
}

func normalizeIssueState(value string) string {
	return strings.ToLower(value)
}

func csvValue(value any) []string {
	switch typed := value.(type) {
	case []any:
		var values []string
		for _, raw := range typed {
			switch entry := raw.(type) {
			case string:
				values = append(values, entry)
			case fmt.Stringer:
				values = append(values, entry.String())
			case nil:
			default:
				values = append(values, fmt.Sprint(entry))
			}
		}
		return values
	case []string:
		return append([]string(nil), typed...)
	default:
		return nil
	}
}

func workerSSHHosts(value any) []string {
	switch typed := value.(type) {
	case []any:
		hosts := make([]string, 0, len(typed))
		seen := map[string]struct{}{}
		for _, raw := range typed {
			host, ok := raw.(string)
			if !ok {
				continue
			}
			trimmed := strings.TrimSpace(host)
			if trimmed == "" {
				continue
			}
			if _, exists := seen[trimmed]; exists {
				continue
			}
			seen[trimmed] = struct{}{}
			hosts = append(hosts, trimmed)
		}
		return hosts
	case []string:
		return workerSSHHosts(anySlice(typed))
	default:
		return nil
	}
}

func valueOrDefaultStringSlice(value []string, fallback []string) []string {
	if len(value) == 0 {
		return append([]string(nil), fallback...)
	}

	return append([]string(nil), value...)
}

func positiveIntOrDefault(value any, fallback int) int {
	if parsed, ok := parsePositiveInt(value); ok {
		return parsed
	}

	return fallback
}

func intOrDefault(value any, fallback int) int {
	if parsed, ok := parseInt(value); ok {
		return parsed
	}

	return fallback
}

func nonNegativeIntOrDefault(value any, fallback int) int {
	if parsed, ok := parseNonNegativeInt(value); ok {
		return parsed
	}

	return fallback
}

func boolOrDefault(value any, fallback bool) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "true":
			return true
		case "false":
			return false
		}
	}

	return fallback
}

func parseInt(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	case string:
		if parsed, err := strconv.Atoi(strings.TrimSpace(typed)); err == nil {
			return parsed, true
		}
	}

	return 0, false
}

func parsePositiveInt(value any) (int, bool) {
	parsed, ok := parseInt(value)
	return parsed, ok && parsed > 0
}

func parseNonNegativeInt(value any) (int, bool) {
	parsed, ok := parseInt(value)
	return parsed, ok && parsed >= 0
}

func envRefName(value string) (string, bool) {
	if !strings.HasPrefix(value, "$") {
		return "", false
	}

	name := strings.TrimPrefix(value, "$")
	if name == "" {
		return "", false
	}

	for index, r := range name {
		switch {
		case index == 0 && ((r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || r == '_'):
		case index > 0 && ((r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_'):
		default:
			return "", false
		}
	}

	return name, true
}

func normalizeSecretValue(value string) string {
	return strings.TrimSpace(value)
}

func firstNonEmptyString(value, fallback string) string {
	if value == "" {
		return fallback
	}

	return value
}

func intPointer(value int) *int {
	result := value
	return &result
}

func isURI(value string) bool {
	return strings.Contains(value, "://")
}

func resolveRuntimeTurnSandboxPolicy(current Effective, workspace string, remote bool) (map[string]any, error) {
	if current.CodexTurnSandboxPolicy != nil {
		return cloneAny(current.CodexTurnSandboxPolicy).(map[string]any), nil
	}

	if remote {
		root := current.WorkspaceRoot
		if strings.TrimSpace(workspace) != "" {
			root = workspace
		}
		if strings.TrimSpace(root) == "" {
			root = defaultWorkspaceRoot
		}
		return defaultTurnSandboxPolicy(root), nil
	}

	root := current.WorkspaceRoot
	if strings.TrimSpace(workspace) != "" {
		root = workspace
	}
	return defaultTurnSandboxPolicy(expandedLocalWorkspaceRoot(root)), nil
}

func expandedLocalWorkspaceRoot(root string) string {
	if strings.TrimSpace(root) == "" {
		root = defaultWorkspaceRoot
	}

	if strings.HasPrefix(root, "~") {
		if home, err := os.UserHomeDir(); err == nil {
			switch {
			case root == "~":
				root = home
			case strings.HasPrefix(root, "~/"):
				root = filepath.Join(home, strings.TrimPrefix(root, "~/"))
			}
		}
	}

	if !isURI(root) && (strings.Contains(root, "/") || strings.Contains(root, string(os.PathSeparator)) || strings.Contains(root, "\\")) {
		if expanded, err := filepath.Abs(root); err == nil {
			root = expanded
		}
	}

	if canonical, err := pathsafety.Canonicalize(root); err == nil {
		return canonical
	}

	return root
}

func validateStringList(value any, field string) error {
	if value == nil {
		return nil
	}

	switch value.(type) {
	case []any, []string:
		return nil
	default:
		return fmt.Errorf("%s must be a YAML list of strings", field)
	}
}

func validatePositiveOptionalInt(value any, field string) error {
	if value == nil {
		return nil
	}
	if _, ok := parsePositiveInt(value); ok {
		return nil
	}
	return fmt.Errorf("%s must be a positive integer", field)
}

func anySlice(values []string) []any {
	result := make([]any, len(values))
	for index, value := range values {
		result[index] = value
	}
	return result
}
