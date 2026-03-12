package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openai/symphony/go/internal/agent"
	"github.com/openai/symphony/go/internal/domain"
	"github.com/openai/symphony/go/internal/observability"
	"github.com/openai/symphony/go/internal/orchestrator"
	"github.com/openai/symphony/go/internal/runtimeconfig"
)

type fakeTracker struct {
	candidates []domain.Issue
	byID       map[string]domain.Issue
	fetchIDs   func([]string) ([]domain.Issue, error)
}

func (f *fakeTracker) FetchCandidateIssues() ([]domain.Issue, error) {
	return append([]domain.Issue(nil), f.candidates...), nil
}

func (f *fakeTracker) FetchIssuesByStates(_ []string) ([]domain.Issue, error) {
	return append([]domain.Issue(nil), f.candidates...), nil
}

func (f *fakeTracker) FetchIssueStatesByIDs(ids []string) ([]domain.Issue, error) {
	if f.fetchIDs != nil {
		return f.fetchIDs(ids)
	}
	issues := make([]domain.Issue, 0, len(ids))
	for _, id := range ids {
		if issue, ok := f.byID[id]; ok {
			issues = append(issues, issue)
		}
	}
	return issues, nil
}

func (f *fakeTracker) CreateComment(_ string, _ string) error    { return nil }
func (f *fakeTracker) UpdateIssueState(_ string, _ string) error { return nil }

func TestServiceStartProvidesSnapshotAndRefresh(t *testing.T) {
	writeAppWorkflow(t, `---
tracker:
  kind: "memory"
---
Prompt
`)

	service, err := Start(Options{
		Tracker: &fakeTracker{},
	})
	if err != nil {
		t.Fatalf("Start() returned error: %v", err)
	}
	t.Cleanup(func() { _ = service.Stop() })

	snapshot, err := service.Snapshot(50 * time.Millisecond)
	if err != nil {
		t.Fatalf("Snapshot() returned error: %v", err)
	}
	if len(snapshot.Running) != 0 || len(snapshot.Retrying) != 0 {
		t.Fatalf("snapshot = %#v, want empty running/retrying", snapshot)
	}

	refresh, err := service.RequestRefresh()
	if err != nil {
		t.Fatalf("RequestRefresh() returned error: %v", err)
	}
	if !refresh.Queued {
		t.Fatalf("refresh = %#v, want queued=true", refresh)
	}
}

func TestServiceDispatchesCandidateAndSchedulesRetry(t *testing.T) {
	writeAppWorkflow(t, `---
tracker:
  kind: "memory"
---
Prompt
`)

	issue := domain.Issue{
		ID:               "issue-1",
		Identifier:       "MT-1",
		Title:            "Candidate",
		State:            "Todo",
		AssignedToWorker: true,
	}

	trackerClient := &fakeTracker{
		candidates: []domain.Issue{issue},
		byID:       map[string]domain.Issue{issue.ID: issue},
	}

	logBuffer := captureServiceLogs(t)
	service, err := Start(Options{
		Tracker: trackerClient,
		AgentRun: func(_ context.Context, issue domain.Issue, opts agent.Options) error {
			opts.OnCodexUpdate(map[string]any{
				"event":      "session_started",
				"session_id": "thread-1-turn-1",
				"timestamp":  time.Now().UTC(),
			})
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Start() returned error: %v", err)
	}
	t.Cleanup(func() { _ = service.Stop() })

	waitUntil(t, 500*time.Millisecond, func() bool {
		snapshot, err := service.Snapshot(50 * time.Millisecond)
		if err != nil {
			return false
		}
		return len(snapshot.Retrying) == 1 && snapshot.Retrying[0].Identifier == "MT-1" && snapshot.Retrying[0].Attempt == 1
	})
	logText := logBuffer.String()
	if !strings.Contains(logText, "Dispatching issue to agent: issue_id=issue-1 issue_identifier=MT-1 attempt=<nil>") {
		t.Fatalf("log missing dispatch message: %q", logText)
	}
	if !strings.Contains(logText, "Agent task completed for issue_id=issue-1 session_id=thread-1-turn-1; scheduling active-state continuation check") {
		t.Fatalf("log missing completion message: %q", logText)
	}
	if !strings.Contains(logText, "Retrying issue_id=issue-1 issue_identifier=MT-1 in ") {
		t.Fatalf("log missing retry message: %q", logText)
	}
	if !strings.Contains(logText, "Agent task finished for issue_id=issue-1 session_id=thread-1-turn-1 reason=worker_normal") {
		t.Fatalf("log missing finished message: %q", logText)
	}
}

func TestStopCancelsRunningWorkers(t *testing.T) {
	writeAppWorkflow(t, `---
tracker:
  kind: "memory"
---
Prompt
`)

	issue := domain.Issue{
		ID:               "issue-2",
		Identifier:       "MT-2",
		Title:            "Long running",
		State:            "Todo",
		AssignedToWorker: true,
	}

	trackerClient := &fakeTracker{
		candidates: []domain.Issue{issue},
		byID:       map[string]domain.Issue{issue.ID: issue},
	}

	canceled := make(chan struct{}, 1)
	service, err := Start(Options{
		Tracker: trackerClient,
		AgentRun: func(ctx context.Context, _ domain.Issue, _ agent.Options) error {
			<-ctx.Done()
			canceled <- struct{}{}
			return ctx.Err()
		},
	})
	if err != nil {
		t.Fatalf("Start() returned error: %v", err)
	}

	waitUntil(t, 500*time.Millisecond, func() bool {
		snapshot, err := service.Snapshot(50 * time.Millisecond)
		if err != nil {
			return false
		}
		return len(snapshot.Running) == 1
	})

	if err := service.Stop(); err != nil {
		t.Fatalf("Stop() returned error: %v", err)
	}

	select {
	case <-canceled:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("worker context was not canceled on Stop()")
	}
}

func TestServiceLogsWorkerFailureAndRetry(t *testing.T) {
	writeAppWorkflow(t, `---
tracker:
  kind: "memory"
---
Prompt
`)

	issue := domain.Issue{
		ID:               "issue-3",
		Identifier:       "MT-3",
		Title:            "Failure",
		State:            "Todo",
		AssignedToWorker: true,
	}

	trackerClient := &fakeTracker{
		candidates: []domain.Issue{issue},
		byID:       map[string]domain.Issue{issue.ID: issue},
	}

	logBuffer := captureServiceLogs(t)
	service, err := Start(Options{
		Tracker: trackerClient,
		AgentRun: func(_ context.Context, issue domain.Issue, opts agent.Options) error {
			opts.OnCodexUpdate(map[string]any{
				"event":      "session_started",
				"session_id": "thread-3-turn-1",
				"timestamp":  time.Now().UTC(),
			})
			return errors.New("boom")
		},
	})
	if err != nil {
		t.Fatalf("Start() returned error: %v", err)
	}
	t.Cleanup(func() { _ = service.Stop() })

	waitUntil(t, 500*time.Millisecond, func() bool {
		snapshot, err := service.Snapshot(50 * time.Millisecond)
		if err != nil {
			return false
		}
		return len(snapshot.Retrying) == 1 && snapshot.Retrying[0].Identifier == "MT-3"
	})

	logText := logBuffer.String()
	if !strings.Contains(logText, "Agent task exited for issue_id=issue-3 session_id=thread-3-turn-1 reason=boom; scheduling retry") {
		t.Fatalf("log missing worker-exit message: %q", logText)
	}
	if !strings.Contains(logText, "Agent task finished for issue_id=issue-3 session_id=thread-3-turn-1 reason=boom") {
		t.Fatalf("log missing worker-finished message: %q", logText)
	}
	if !strings.Contains(logText, "Retrying issue_id=issue-3 issue_identifier=MT-3 in ") {
		t.Fatalf("log missing retry scheduling message: %q", logText)
	}
}

func TestServiceReconcileRunningIssuesLogsNonActiveTransition(t *testing.T) {
	writeAppWorkflow(t, `---
tracker:
  kind: "memory"
  active_states: ["Todo", "In Progress"]
  terminal_states: ["Closed", "Cancelled", "Canceled", "Duplicate"]
---
Prompt
`)

	issue := domain.Issue{
		ID:         "issue-4",
		Identifier: "MT-4",
		Title:      "Reconcile",
		State:      "Backlog",
	}

	service := &Service{
		state: orchestrator.NewState(time.Now().UTC()),
		tracker: &fakeTracker{
			byID: map[string]domain.Issue{issue.ID: issue},
		},
		now: func() time.Time { return time.Now().UTC() },
	}
	service.state.Running[issue.ID] = orchestrator.RunningEntry{
		Identifier: issue.Identifier,
		Issue:      domain.Issue{ID: issue.ID, Identifier: issue.Identifier, Title: issue.Title, State: "Todo"},
		StartedAt:  time.Now().UTC(),
	}
	service.state.Claimed[issue.ID] = struct{}{}

	logBuffer := captureServiceLogs(t)
	service.reconcileRunningIssues()

	if _, ok := service.state.Running[issue.ID]; ok {
		t.Fatalf("issue still running after reconcile: %#v", service.state.Running[issue.ID])
	}
	if !strings.Contains(logBuffer.String(), "Issue moved to non-active state: issue_id=issue-4 issue_identifier=MT-4 state=Backlog; stopping active agent") {
		t.Fatalf("log missing non-active transition message: %q", logBuffer.String())
	}
}

func TestServiceSmokeWithRealAgentAndHTTP(t *testing.T) {
	testRoot := t.TempDir()
	workspaceRoot := filepath.Join(testRoot, "workspaces")
	codexBinary := filepath.Join(testRoot, "fake-codex")

	if err := os.MkdirAll(workspaceRoot, 0o755); err != nil {
		t.Fatalf("os.MkdirAll(%q) failed: %v", workspaceRoot, err)
	}
	if err := os.WriteFile(codexBinary, []byte(`#!/bin/sh
count=0
while IFS= read -r _line; do
  count=$((count + 1))
  case "$count" in
    1) printf '%s\n' '{"id":1,"result":{}}' ;;
    2) ;;
    3) printf '%s\n' '{"id":2,"result":{"thread":{"id":"thread-smoke"}}}' ;;
    4)
      printf '%s\n' '{"id":3,"result":{"turn":{"id":"turn-smoke"}}}'
      printf '%s\n' '{"method":"turn/completed"}'
      exit 0
      ;;
    *) exit 0 ;;
  esac
done
`), 0o755); err != nil {
		t.Fatalf("os.WriteFile(%q) failed: %v", codexBinary, err)
	}

	writeAppWorkflow(t, `---
tracker:
  kind: "memory"
workspace:
  root: "`+workspaceRoot+`"
codex:
  command: "`+strings.ReplaceAll(codexBinary, `"`, `\"`)+` app-server"
server:
  host: "127.0.0.1"
  port: 0
---
Prompt
`)

	issue := domain.Issue{
		ID:               "issue-smoke",
		Identifier:       "MT-500",
		Title:            "Smoke",
		State:            "Todo",
		AssignedToWorker: true,
	}

	var fetchMu sync.Mutex
	fetchCalls := 0
	trackerClient := &fakeTracker{
		candidates: []domain.Issue{issue},
		fetchIDs: func(ids []string) ([]domain.Issue, error) {
			fetchMu.Lock()
			defer fetchMu.Unlock()
			fetchCalls++
			if fetchCalls <= 3 {
				return []domain.Issue{issue}, nil
			}
			done := issue
			done.State = "Done"
			return []domain.Issue{done}, nil
		},
	}

	service, err := Start(Options{Tracker: trackerClient})
	if err != nil {
		t.Fatalf("Start() returned error: %v", err)
	}
	t.Cleanup(func() { _ = service.Stop() })

	waitUntil(t, 2*time.Second, func() bool {
		snapshot, err := service.Snapshot(50 * time.Millisecond)
		if err != nil {
			return false
		}
		return len(snapshot.Retrying) == 1 && snapshot.Retrying[0].Identifier == "MT-500"
	})

	if service.httpListener == nil {
		t.Fatal("service.httpListener = nil, want active HTTP listener")
	}
	baseURL := "http://" + service.httpListener.Addr().String()

	rootResp, err := http.Get(baseURL + "/")
	if err != nil {
		t.Fatalf("http.Get(root) failed: %v", err)
	}
	defer rootResp.Body.Close()
	rootBody, err := io.ReadAll(rootResp.Body)
	if err != nil {
		t.Fatalf("io.ReadAll(root) failed: %v", err)
	}
	if rootResp.StatusCode != http.StatusOK {
		t.Fatalf("root status = %d, want 200", rootResp.StatusCode)
	}
	if !strings.Contains(string(rootBody), "Symphony Dashboard") ||
		!strings.Contains(string(rootBody), "MT-500") ||
		!strings.Contains(string(rootBody), "/api/v1/MT-500") {
		t.Fatalf("root body = %q, want richer dashboard page with MT-500", string(rootBody))
	}

	stateResp, err := http.Get(baseURL + "/api/v1/state")
	if err != nil {
		t.Fatalf("http.Get(state) failed: %v", err)
	}
	defer stateResp.Body.Close()
	stateBody, err := io.ReadAll(stateResp.Body)
	if err != nil {
		t.Fatalf("io.ReadAll(state) failed: %v", err)
	}
	if stateResp.StatusCode != http.StatusOK {
		t.Fatalf("/api/v1/state status = %d, want 200", stateResp.StatusCode)
	}
	if !strings.Contains(string(stateBody), "MT-500") {
		t.Fatalf("/api/v1/state body = %q, want MT-500", string(stateBody))
	}
}

func TestStopDefaultClearsUnavailableRefresh(t *testing.T) {
	writeAppWorkflow(t, `---
tracker:
  kind: "memory"
---
Prompt
`)

	if err := EnsureStarted(); err != nil {
		t.Fatalf("EnsureStarted() returned error: %v", err)
	}
	if err := StopDefault(); err != nil {
		t.Fatalf("StopDefault() returned error: %v", err)
	}

	defaultMu.Lock()
	service := defaultService
	defaultMu.Unlock()
	if service != nil {
		t.Fatal("defaultService still set after StopDefault()")
	}
}

func writeAppWorkflow(t *testing.T, content string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "WORKFLOW.md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("os.WriteFile(%q) failed: %v", path, err)
	}
	if err := runtimeconfig.SetWorkflowFilePath(path); err != nil {
		t.Fatalf("runtimeconfig.SetWorkflowFilePath(%q) failed: %v", path, err)
	}
}

func waitUntil(t *testing.T, timeout time.Duration, predicate func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if predicate() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for condition")
}

var _ observability.SnapshotSource = (*Service)(nil)
var _ observability.RefreshSource = (*Service)(nil)

var serviceLogCaptureMu sync.Mutex

func captureServiceLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	serviceLogCaptureMu.Lock()
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
		serviceLogCaptureMu.Unlock()
	})
	return &buf
}
