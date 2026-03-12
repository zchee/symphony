package orchestrator

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/openai/symphony/go/internal/config"
	"github.com/openai/symphony/go/internal/domain"
	"github.com/openai/symphony/go/internal/workspace"
)

const (
	continuationRetryDelayMS = int64(1_000)
	failureRetryBaseMS       = int64(10_000)
)

var ErrWorkerNormal = errors.New("worker_normal")

// TokenTotals tracks aggregate Codex token usage and runtime.
type TokenTotals struct {
	InputTokens    int
	OutputTokens   int
	TotalTokens    int
	SecondsRunning int
}

// RetryEntry describes one queued retry attempt.
type RetryEntry struct {
	Attempt    int
	DueAtMS    int64
	Identifier string
	Error      string
}

// RunningEntry is the active-runtime metadata for one claimed issue.
type RunningEntry struct {
	Identifier                    string
	Issue                         domain.Issue
	SessionID                     string
	CodexAppServerPID             string
	LastCodexMessage              any
	LastCodexTimestamp            *time.Time
	LastCodexEvent                string
	CodexInputTokens              int
	CodexOutputTokens             int
	CodexTotalTokens              int
	CodexLastReportedInputTokens  int
	CodexLastReportedOutputTokens int
	CodexLastReportedTotalTokens  int
	TurnCount                     int
	RetryAttempt                  int
	StartedAt                     time.Time
	Stop                          func()
}

// State is the authoritative in-memory orchestrator state.
type State struct {
	PollIntervalMS      int
	MaxConcurrentAgents int
	NextPollDueAtMS     *int64
	PollCheckInProgress bool
	Running             map[string]RunningEntry
	Completed           map[string]struct{}
	Claimed             map[string]struct{}
	RetryAttempts       map[string]RetryEntry
	CodexTotals         TokenTotals
	CodexRateLimits     map[string]any
}

// CodexUpdate is the normalized update payload emitted by the Codex layer.
type CodexUpdate struct {
	Event             string
	SessionID         string
	CodexAppServerPID string
	Payload           map[string]any
	Timestamp         time.Time
}

// Snapshot is the presentation-friendly runtime snapshot.
type Snapshot struct {
	Running     []RunningSnapshot
	Retrying    []RetrySnapshot
	CodexTotals TokenTotals
	RateLimits  map[string]any
	Polling     PollingSnapshot
}

// RunningSnapshot is one running-row projection.
type RunningSnapshot struct {
	IssueID            string
	Identifier         string
	State              string
	SessionID          string
	CodexAppServerPID  string
	CodexInputTokens   int
	CodexOutputTokens  int
	CodexTotalTokens   int
	TurnCount          int
	StartedAt          time.Time
	LastCodexTimestamp *time.Time
	LastCodexMessage   any
	LastCodexEvent     string
	RuntimeSeconds     int
}

// RetrySnapshot is one retry-row projection.
type RetrySnapshot struct {
	IssueID    string
	Attempt    int
	DueInMS    int64
	Identifier string
	Error      string
}

// PollingSnapshot contains the current poll status.
type PollingSnapshot struct {
	Checking       bool
	NextPollInMS   *int64
	PollIntervalMS int
}

// NewState returns a default state derived from the current config.
func NewState(now time.Time) State {
	cfg := config.Current()
	dueAt := now.UnixMilli()
	return State{
		PollIntervalMS:      cfg.PollIntervalMS,
		MaxConcurrentAgents: cfg.MaxConcurrentAgents,
		NextPollDueAtMS:     &dueAt,
		Running:             map[string]RunningEntry{},
		Completed:           map[string]struct{}{},
		Claimed:             map[string]struct{}{},
		RetryAttempts:       map[string]RetryEntry{},
		CodexTotals:         TokenTotals{},
		CodexRateLimits:     nil,
	}
}

// SortIssuesForDispatch orders issues by priority, created_at, and identifier.
func SortIssuesForDispatch(issues []domain.Issue) []domain.Issue {
	sorted := append([]domain.Issue(nil), issues...)
	sort.SliceStable(sorted, func(i, j int) bool {
		left, right := sorted[i], sorted[j]
		if priorityRank(left.Priority) != priorityRank(right.Priority) {
			return priorityRank(left.Priority) < priorityRank(right.Priority)
		}
		if createdAtRank(left.CreatedAt) != createdAtRank(right.CreatedAt) {
			return createdAtRank(left.CreatedAt) < createdAtRank(right.CreatedAt)
		}
		return fallbackIdentifier(left) < fallbackIdentifier(right)
	})
	return sorted
}

// ShouldDispatchIssue reports whether an issue is currently eligible for dispatch.
func ShouldDispatchIssue(issue domain.Issue, state State) bool {
	activeStates := activeStateSet()
	terminalStates := terminalStateSet()

	if !candidateIssue(issue, activeStates, terminalStates) {
		return false
	}
	if todoIssueBlockedByNonTerminal(issue, terminalStates) {
		return false
	}
	if _, ok := state.Claimed[issue.ID]; ok {
		return false
	}
	if _, ok := state.Running[issue.ID]; ok {
		return false
	}
	if availableSlots(state) <= 0 {
		return false
	}
	return stateSlotsAvailable(issue, state.Running)
}

// RevalidateIssueForDispatch refreshes one issue just before dispatch.
func RevalidateIssueForDispatch(issue domain.Issue, issueFetcher func([]string) ([]domain.Issue, error)) (domain.Issue, bool, error) {
	if issue.ID == "" {
		return issue, false, nil
	}
	refreshed, err := issueFetcher([]string{issue.ID})
	if err != nil {
		return domain.Issue{}, false, err
	}
	if len(refreshed) == 0 {
		return domain.Issue{}, true, nil
	}
	current := refreshed[0]
	if !candidateIssue(current, activeStateSet(), terminalStateSet()) || todoIssueBlockedByNonTerminal(current, terminalStateSet()) {
		return current, true, nil
	}
	return current, false, nil
}

// ReconcileIssueStates updates running entries against refreshed tracker state.
func ReconcileIssueStates(issues []domain.Issue, state State) State {
	activeStates := activeStateSet()
	terminalStates := terminalStateSet()

	for _, issue := range issues {
		if _, ok := state.Running[issue.ID]; !ok {
			continue
		}

		switch {
		case terminalIssueState(issue.State, terminalStates):
			state = terminateRunningIssue(state, issue.ID, true)
		case !issueRoutableToWorker(issue):
			state = terminateRunningIssue(state, issue.ID, false)
		case activeIssueState(issue.State, activeStates):
			entry := state.Running[issue.ID]
			entry.Issue = issue
			state.Running[issue.ID] = entry
		default:
			state = terminateRunningIssue(state, issue.ID, false)
		}
	}

	return state
}

// HandleWorkerExit updates retry state after one worker exits.
func HandleWorkerExit(state State, issueID string, reason error, now time.Time) State {
	entry, ok := state.Running[issueID]
	if !ok {
		return state
	}

	state = recordSessionCompletionTotals(state, entry, now)
	delete(state.Running, issueID)

	if reason == nil || errors.Is(reason, ErrWorkerNormal) {
		state.Completed[issueID] = struct{}{}
		state = scheduleIssueRetry(state, issueID, 1, RetryEntry{
			Attempt:    1,
			Identifier: entry.Identifier,
		}, continuationRetryDelayMS, now)
		return state
	}

	nextAttempt := nextRetryAttempt(entry)
	state = scheduleIssueRetry(state, issueID, nextAttempt, RetryEntry{
		Attempt:    nextAttempt,
		Identifier: entry.Identifier,
		Error:      "agent exited: " + reason.Error(),
	}, backoffDelay(nextAttempt), now)
	return state
}

// ApplyCodexUpdate folds one Codex update into the running-entry and aggregate totals.
func ApplyCodexUpdate(state State, issueID string, update CodexUpdate) State {
	entry, ok := state.Running[issueID]
	if !ok {
		return state
	}

	tokenDelta := extractTokenDelta(entry, update)

	if !update.Timestamp.IsZero() {
		entry.LastCodexTimestamp = timePointer(update.Timestamp)
	}
	entry.LastCodexMessage = summarizeCodexUpdate(update)
	entry.LastCodexEvent = update.Event
	if update.SessionID != "" {
		if update.Event == "session_started" && update.SessionID != entry.SessionID {
			entry.TurnCount++
		}
		entry.SessionID = update.SessionID
	}
	if update.CodexAppServerPID != "" {
		entry.CodexAppServerPID = update.CodexAppServerPID
	}

	entry.CodexInputTokens += tokenDelta.InputTokens
	entry.CodexOutputTokens += tokenDelta.OutputTokens
	entry.CodexTotalTokens += tokenDelta.TotalTokens
	entry.CodexLastReportedInputTokens = max(entry.CodexLastReportedInputTokens, tokenDelta.InputReported)
	entry.CodexLastReportedOutputTokens = max(entry.CodexLastReportedOutputTokens, tokenDelta.OutputReported)
	entry.CodexLastReportedTotalTokens = max(entry.CodexLastReportedTotalTokens, tokenDelta.TotalReported)

	state.Running[issueID] = entry
	state.CodexTotals = applyTokenDelta(state.CodexTotals, tokenDelta)
	if rateLimits := extractRateLimits(update.Payload); rateLimits != nil {
		state.CodexRateLimits = rateLimits
	}

	return state
}

// SnapshotState projects the current state into a snapshot.
func SnapshotState(state State, now time.Time) Snapshot {
	nowMS := now.UnixMilli()
	running := make([]RunningSnapshot, 0, len(state.Running))
	for issueID, entry := range state.Running {
		running = append(running, RunningSnapshot{
			IssueID:            issueID,
			Identifier:         entry.Identifier,
			State:              entry.Issue.State,
			SessionID:          entry.SessionID,
			CodexAppServerPID:  entry.CodexAppServerPID,
			CodexInputTokens:   entry.CodexInputTokens,
			CodexOutputTokens:  entry.CodexOutputTokens,
			CodexTotalTokens:   entry.CodexTotalTokens,
			TurnCount:          entry.TurnCount,
			StartedAt:          entry.StartedAt,
			LastCodexTimestamp: entry.LastCodexTimestamp,
			LastCodexMessage:   entry.LastCodexMessage,
			LastCodexEvent:     entry.LastCodexEvent,
			RuntimeSeconds:     runningSeconds(entry.StartedAt, now),
		})
	}
	sort.SliceStable(running, func(i, j int) bool { return running[i].Identifier < running[j].Identifier })

	retrying := make([]RetrySnapshot, 0, len(state.RetryAttempts))
	for issueID, retry := range state.RetryAttempts {
		retrying = append(retrying, RetrySnapshot{
			IssueID:    issueID,
			Attempt:    retry.Attempt,
			DueInMS:    max64(0, retry.DueAtMS-nowMS),
			Identifier: retry.Identifier,
			Error:      retry.Error,
		})
	}
	sort.SliceStable(retrying, func(i, j int) bool { return retrying[i].Identifier < retrying[j].Identifier })

	var nextPollInMS *int64
	if state.NextPollDueAtMS != nil {
		value := max64(0, *state.NextPollDueAtMS-nowMS)
		nextPollInMS = &value
	}

	return Snapshot{
		Running:     running,
		Retrying:    retrying,
		CodexTotals: state.CodexTotals,
		RateLimits:  cloneMap(state.CodexRateLimits),
		Polling: PollingSnapshot{
			Checking:       state.PollCheckInProgress,
			NextPollInMS:   nextPollInMS,
			PollIntervalMS: state.PollIntervalMS,
		},
	}
}

func terminateRunningIssue(state State, issueID string, cleanupWorkspace bool) State {
	entry, ok := state.Running[issueID]
	if !ok {
		delete(state.Claimed, issueID)
		delete(state.RetryAttempts, issueID)
		return state
	}

	if cleanupWorkspace {
		_ = workspace.RemoveIssueWorkspaces(entry.Identifier)
	}
	if entry.Stop != nil {
		entry.Stop()
	}
	delete(state.Running, issueID)
	delete(state.Claimed, issueID)
	delete(state.RetryAttempts, issueID)
	return state
}

func candidateIssue(issue domain.Issue, activeStates, terminalStates map[string]struct{}) bool {
	if issue.ID == "" || issue.Identifier == "" || issue.Title == "" || issue.State == "" {
		return false
	}
	return issueRoutableToWorker(issue) && activeIssueState(issue.State, activeStates) && !terminalIssueState(issue.State, terminalStates)
}

func issueRoutableToWorker(issue domain.Issue) bool {
	if issue.AssignedToWorker {
		return true
	}
	// Go cannot distinguish an omitted bool from explicit false; preserve Elixir's "missing means true"
	// behavior by treating unassigned issues with no assignee metadata as still routable.
	return issue.AssigneeID == ""
}

func todoIssueBlockedByNonTerminal(issue domain.Issue, terminalStates map[string]struct{}) bool {
	if normalizeIssueState(issue.State) != "todo" {
		return false
	}
	for _, blocker := range issue.BlockedBy {
		if blocker.State == "" || !terminalIssueState(blocker.State, terminalStates) {
			return true
		}
	}
	return false
}

func stateSlotsAvailable(issue domain.Issue, running map[string]RunningEntry) bool {
	limit := config.Current().MaxConcurrentAgentsForState(issue.State)
	used := 0
	for _, entry := range running {
		if normalizeIssueState(entry.Issue.State) == normalizeIssueState(issue.State) {
			used++
		}
	}
	return limit > used
}

func availableSlots(state State) int {
	limit := state.MaxConcurrentAgents
	if limit <= 0 {
		limit = config.Current().MaxConcurrentAgents
	}
	remaining := limit - len(state.Running)
	if remaining < 0 {
		return 0
	}
	return remaining
}

func activeIssueState(state string, activeStates map[string]struct{}) bool {
	_, ok := activeStates[normalizeIssueState(state)]
	return ok
}

func terminalIssueState(state string, terminalStates map[string]struct{}) bool {
	_, ok := terminalStates[normalizeIssueState(state)]
	return ok
}

func normalizeIssueState(state string) string {
	return strings.ToLower(strings.TrimSpace(state))
}

func activeStateSet() map[string]struct{} {
	result := make(map[string]struct{}, len(config.Current().LinearActiveStates))
	for _, state := range config.Current().LinearActiveStates {
		normalized := normalizeIssueState(state)
		if normalized != "" {
			result[normalized] = struct{}{}
		}
	}
	return result
}

func terminalStateSet() map[string]struct{} {
	result := make(map[string]struct{}, len(config.Current().LinearTerminalStates))
	for _, state := range config.Current().LinearTerminalStates {
		normalized := normalizeIssueState(state)
		if normalized != "" {
			result[normalized] = struct{}{}
		}
	}
	return result
}

func priorityRank(priority *int) int {
	if priority != nil && *priority >= 1 && *priority <= 4 {
		return *priority
	}
	return 5
}

func createdAtRank(createdAt *time.Time) int64 {
	if createdAt == nil {
		return int64(^uint64(0) >> 1)
	}
	return createdAt.UnixMicro()
}

func fallbackIdentifier(issue domain.Issue) string {
	if issue.Identifier != "" {
		return issue.Identifier
	}
	return issue.ID
}

func nextRetryAttempt(entry RunningEntry) int {
	if entry.RetryAttempt > 0 {
		return entry.RetryAttempt + 1
	}
	return 1
}

func backoffDelay(attempt int) int64 {
	if attempt <= 1 {
		return failureRetryBaseMS
	}
	delay := failureRetryBaseMS << (attempt - 1)
	if maxDelay := int64(config.Current().MaxRetryBackoffMS); maxDelay > 0 && delay > maxDelay {
		return maxDelay
	}
	return delay
}

func scheduleIssueRetry(state State, issueID string, attempt int, entry RetryEntry, delayMS int64, now time.Time) State {
	entry.Attempt = attempt
	entry.DueAtMS = now.UnixMilli() + delayMS
	state.RetryAttempts[issueID] = entry
	state.Claimed[issueID] = struct{}{}
	return state
}

func recordSessionCompletionTotals(state State, entry RunningEntry, now time.Time) State {
	state.CodexTotals.SecondsRunning += runningSeconds(entry.StartedAt, now)
	return state
}

func runningSeconds(startedAt, now time.Time) int {
	if startedAt.IsZero() || now.IsZero() {
		return 0
	}
	if now.Before(startedAt) {
		return 0
	}
	return int(now.Sub(startedAt).Seconds())
}

type tokenDelta struct {
	InputTokens    int
	OutputTokens   int
	TotalTokens    int
	InputReported  int
	OutputReported int
	TotalReported  int
}

func extractTokenDelta(entry RunningEntry, update CodexUpdate) tokenDelta {
	usage := extractTokenUsage(update.Payload)

	inputReported := getTokenUsage(usage, "input")
	outputReported := getTokenUsage(usage, "output")
	totalReported := getTokenUsage(usage, "total")

	return tokenDelta{
		InputTokens:    max(0, inputReported-entry.CodexLastReportedInputTokens),
		OutputTokens:   max(0, outputReported-entry.CodexLastReportedOutputTokens),
		TotalTokens:    max(0, totalReported-entry.CodexLastReportedTotalTokens),
		InputReported:  max(entry.CodexLastReportedInputTokens, inputReported),
		OutputReported: max(entry.CodexLastReportedOutputTokens, outputReported),
		TotalReported:  max(entry.CodexLastReportedTotalTokens, totalReported),
	}
}

func applyTokenDelta(current TokenTotals, delta tokenDelta) TokenTotals {
	current.InputTokens += delta.InputTokens
	current.OutputTokens += delta.OutputTokens
	current.TotalTokens += delta.TotalTokens
	return current
}

func summarizeCodexUpdate(update CodexUpdate) map[string]any {
	return map[string]any{
		"event":     update.Event,
		"message":   update.Payload,
		"timestamp": update.Timestamp,
	}
}

func extractRateLimits(payload map[string]any) map[string]any {
	if payload == nil {
		return nil
	}
	return rateLimitsFromPayload(payload)
}

func rateLimitsFromPayload(payload any) map[string]any {
	switch typed := payload.(type) {
	case map[string]any:
		if direct, ok := typed["rate_limits"].(map[string]any); ok {
			return cloneMap(direct)
		}
		if isRateLimitsMap(typed) {
			return cloneMap(typed)
		}
		for _, value := range typed {
			if nested := rateLimitsFromPayload(value); nested != nil {
				return nested
			}
		}
	case []any:
		for _, value := range typed {
			if nested := rateLimitsFromPayload(value); nested != nil {
				return nested
			}
		}
	}
	return nil
}

func isRateLimitsMap(payload map[string]any) bool {
	if payload == nil {
		return false
	}
	_, hasLimitID := payload["limit_id"]
	_, hasPrimary := payload["primary"]
	_, hasSecondary := payload["secondary"]
	_, hasCredits := payload["credits"]
	return hasLimitID && (hasPrimary || hasSecondary || hasCredits)
}

func extractTokenUsage(payload map[string]any) map[string]any {
	if payload == nil {
		return map[string]any{}
	}

	for _, path := range [][]string{
		{"params", "msg", "payload", "info", "total_token_usage"},
		{"params", "msg", "info", "total_token_usage"},
		{"params", "tokenUsage", "total"},
		{"tokenUsage", "total"},
	} {
		if value := mapAtPath(payload, path...); value != nil {
			if usage, ok := value.(map[string]any); ok {
				return usage
			}
		}
	}

	if stringValue(payload["method"]) == "turn/completed" {
		if usage, ok := payload["usage"].(map[string]any); ok {
			return usage
		}
		if usage, ok := mapAtPath(payload, "params", "usage").(map[string]any); ok {
			return usage
		}
	}

	return map[string]any{}
}

func mapAtPath(payload map[string]any, path ...string) any {
	current := any(payload)
	for _, segment := range path {
		node, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		next, ok := node[segment]
		if !ok {
			return nil
		}
		current = next
	}
	return current
}

func getTokenUsage(usage map[string]any, tokenKind string) int {
	switch tokenKind {
	case "input":
		return firstInt(usage, "input_tokens", "prompt_tokens", "inputTokens", "promptTokens")
	case "output":
		return firstInt(usage, "output_tokens", "completion_tokens", "outputTokens", "completionTokens")
	default:
		return firstInt(usage, "total_tokens", "total", "totalTokens")
	}
}

func stringValue(value any) string {
	raw, _ := value.(string)
	return raw
}

func firstInt(payload map[string]any, keys ...string) int {
	for _, key := range keys {
		switch value := payload[key].(type) {
		case int:
			return value
		case float64:
			return int(value)
		case string:
			var parsed int
			if _, err := fmt.Sscanf(strings.TrimSpace(value), "%d", &parsed); err == nil {
				return parsed
			}
		}
	}
	return 0
}

func cloneMap(value map[string]any) map[string]any {
	if value == nil {
		return nil
	}
	cloned := make(map[string]any, len(value))
	for key, nested := range value {
		if child, ok := nested.(map[string]any); ok {
			cloned[key] = cloneMap(child)
		} else {
			cloned[key] = nested
		}
	}
	return cloned
}

func timePointer(value time.Time) *time.Time {
	result := value
	return &result
}

func max64(left, right int64) int64 {
	if left > right {
		return left
	}
	return right
}
