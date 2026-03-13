package observability

import (
	"errors"
	"path/filepath"
	"time"

	"github.com/openai/symphony/go/config"
	"github.com/openai/symphony/go/orchestrator"
)

var (
	ErrSnapshotTimeout     = errors.New("snapshot_timeout")
	ErrSnapshotUnavailable = errors.New("snapshot_unavailable")
	ErrIssueNotFound       = errors.New("issue_not_found")
	ErrRefreshUnavailable  = errors.New("orchestrator_unavailable")
)

// SnapshotSource provides snapshot access for observability payloads.
type SnapshotSource interface {
	Snapshot(timeout time.Duration) (orchestrator.Snapshot, error)
}

// RefreshSource provides refresh triggering for observability payloads.
type RefreshSource interface {
	RequestRefresh() (RefreshResult, error)
}

// RefreshResult mirrors the orchestrator refresh acknowledgement.
type RefreshResult struct {
	Queued      bool
	Coalesced   bool
	RequestedAt time.Time
	Operations  []string
}

// StatePayload returns the shared `/api/v1/state` body.
func StatePayload(source SnapshotSource, timeout time.Duration, now time.Time) map[string]any {
	generatedAt := iso8601(now)

	snapshot, err := source.Snapshot(timeout)
	if err != nil {
		switch {
		case errors.Is(err, ErrSnapshotTimeout):
			return map[string]any{
				"generated_at": generatedAt,
				"error": map[string]any{
					"code":    "snapshot_timeout",
					"message": "Snapshot timed out",
				},
			}
		default:
			return map[string]any{
				"generated_at": generatedAt,
				"error": map[string]any{
					"code":    "snapshot_unavailable",
					"message": "Snapshot unavailable",
				},
			}
		}
	}

	running := make([]map[string]any, 0, len(snapshot.Running))
	for _, entry := range snapshot.Running {
		running = append(running, map[string]any{
			"issue_id":         entry.IssueID,
			"issue_identifier": entry.Identifier,
			"state":            entry.State,
			"worker_host":      emptyToNil(entry.WorkerHost),
			"workspace_path":   entry.WorkspacePath,
			"session_id":       entry.SessionID,
			"turn_count":       entry.TurnCount,
			"last_event":       entry.LastCodexEvent,
			"last_message":     summarizeMessage(entry.LastCodexMessage),
			"started_at":       iso8601(entry.StartedAt),
			"last_event_at":    iso8601Ptr(entry.LastCodexTimestamp),
			"tokens": map[string]any{
				"input_tokens":  entry.CodexInputTokens,
				"output_tokens": entry.CodexOutputTokens,
				"total_tokens":  entry.CodexTotalTokens,
			},
		})
	}

	retrying := make([]map[string]any, 0, len(snapshot.Retrying))
	for _, entry := range snapshot.Retrying {
		retrying = append(retrying, map[string]any{
			"issue_id":         entry.IssueID,
			"issue_identifier": entry.Identifier,
			"attempt":          entry.Attempt,
			"due_at":           dueAtISO8601(now, entry.DueInMS),
			"error":            entry.Error,
			"worker_host":      emptyToNil(entry.WorkerHost),
			"workspace_path":   entry.WorkspacePath,
		})
	}

	return map[string]any{
		"generated_at": generatedAt,
		"counts": map[string]any{
			"running":  len(snapshot.Running),
			"retrying": len(snapshot.Retrying),
		},
		"running":  running,
		"retrying": retrying,
		"codex_totals": map[string]any{
			"input_tokens":    snapshot.CodexTotals.InputTokens,
			"output_tokens":   snapshot.CodexTotals.OutputTokens,
			"total_tokens":    snapshot.CodexTotals.TotalTokens,
			"seconds_running": snapshot.CodexTotals.SecondsRunning,
		},
		"rate_limits": snapshot.RateLimits,
	}
}

// IssuePayload returns the shared `/api/v1/:issue_identifier` body.
func IssuePayload(issueIdentifier string, source SnapshotSource, timeout time.Duration, now time.Time) (map[string]any, error) {
	snapshot, err := source.Snapshot(timeout)
	if err != nil {
		return nil, ErrIssueNotFound
	}

	var running *orchestrator.RunningSnapshot
	for _, entry := range snapshot.Running {
		if entry.Identifier == issueIdentifier {
			entryCopy := entry
			running = &entryCopy
			break
		}
	}

	var retry *orchestrator.RetrySnapshot
	for _, entry := range snapshot.Retrying {
		if entry.Identifier == issueIdentifier {
			entryCopy := entry
			retry = &entryCopy
			break
		}
	}

	if running == nil && retry == nil {
		return nil, ErrIssueNotFound
	}

	issueID := ""
	if running != nil {
		issueID = running.IssueID
	} else {
		issueID = retry.IssueID
	}

	status := "running"
	if running == nil && retry != nil {
		status = "retrying"
	}

	payload := map[string]any{
		"issue_identifier": issueIdentifier,
		"issue_id":         issueID,
		"status":           status,
		"workspace": map[string]any{
			"path": workspacePath(issueIdentifier, running, retry),
			"host": emptyToNil(workspaceHost(running, retry)),
		},
		"attempts": map[string]any{
			"restart_count":         restartCount(retry),
			"current_retry_attempt": retryAttempt(retry),
		},
		"running": nil,
		"retry":   nil,
		"logs": map[string]any{
			"codex_session_logs": []any{},
		},
		"recent_events": []any{},
		"last_error":    nil,
		"tracked":       map[string]any{},
	}

	if running != nil {
		payload["running"] = map[string]any{
			"worker_host":    emptyToNil(running.WorkerHost),
			"workspace_path": running.WorkspacePath,
			"session_id":     running.SessionID,
			"turn_count":     running.TurnCount,
			"state":          running.State,
			"started_at":     iso8601(running.StartedAt),
			"last_event":     running.LastCodexEvent,
			"last_message":   summarizeMessage(running.LastCodexMessage),
			"last_event_at":  iso8601Ptr(running.LastCodexTimestamp),
			"tokens": map[string]any{
				"input_tokens":  running.CodexInputTokens,
				"output_tokens": running.CodexOutputTokens,
				"total_tokens":  running.CodexTotalTokens,
			},
		}
		if running.LastCodexTimestamp != nil {
			payload["recent_events"] = []any{
				map[string]any{
					"at":      iso8601Ptr(running.LastCodexTimestamp),
					"event":   running.LastCodexEvent,
					"message": summarizeMessage(running.LastCodexMessage),
				},
			}
		}
	}

	if retry != nil {
		payload["retry"] = map[string]any{
			"attempt":        retry.Attempt,
			"due_at":         dueAtISO8601(now, retry.DueInMS),
			"error":          retry.Error,
			"worker_host":    emptyToNil(retry.WorkerHost),
			"workspace_path": retry.WorkspacePath,
		}
		payload["last_error"] = retry.Error
	}

	return payload, nil
}

// RefreshPayload returns the shared `/api/v1/refresh` body.
func RefreshPayload(source RefreshSource) (map[string]any, error) {
	result, err := source.RequestRefresh()
	if err != nil {
		return nil, ErrRefreshUnavailable
	}

	return map[string]any{
		"queued":       result.Queued,
		"coalesced":    result.Coalesced,
		"requested_at": iso8601(result.RequestedAt),
		"operations":   append([]string(nil), result.Operations...),
	}, nil
}

func restartCount(retry *orchestrator.RetrySnapshot) int {
	attempt := retryAttempt(retry)
	if attempt <= 0 {
		return 0
	}
	return attempt - 1
}

func retryAttempt(retry *orchestrator.RetrySnapshot) int {
	if retry == nil {
		return 0
	}
	return retry.Attempt
}

func workspacePath(issueIdentifier string, running *orchestrator.RunningSnapshot, retry *orchestrator.RetrySnapshot) string {
	if running != nil && running.WorkspacePath != "" {
		return running.WorkspacePath
	}
	if retry != nil && retry.WorkspacePath != "" {
		return retry.WorkspacePath
	}
	return filepath.Join(config.Current().WorkspaceRoot, issueIdentifier)
}

func workspaceHost(running *orchestrator.RunningSnapshot, retry *orchestrator.RetrySnapshot) string {
	if running != nil && running.WorkerHost != "" {
		return running.WorkerHost
	}
	if retry != nil {
		return retry.WorkerHost
	}
	return ""
}

func emptyToNil(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func dueAtISO8601(now time.Time, dueInMS int64) string {
	if dueInMS < 0 {
		dueInMS = 0
	}
	return now.Add(time.Duration(dueInMS/1000) * time.Second).Truncate(time.Second).UTC().Format(time.RFC3339)
}

func iso8601(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Truncate(time.Second).Format(time.RFC3339)
}

func iso8601Ptr(value *time.Time) any {
	if value == nil || value.IsZero() {
		return nil
	}
	return value.UTC().Truncate(time.Second).Format(time.RFC3339)
}

func summarizeMessage(message any) any {
	switch typed := message.(type) {
	case nil:
		return nil
	case string:
		return typed
	case map[string]any:
		if text, ok := typed["message"].(string); ok {
			return text
		}
		if event, ok := typed["event"].(string); ok {
			return event
		}
		return typed
	default:
		return typed
	}
}
