package app

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/openai/symphony/go/internal/agent"
	"github.com/openai/symphony/go/internal/config"
	"github.com/openai/symphony/go/internal/domain"
	"github.com/openai/symphony/go/internal/httpapi"
	"github.com/openai/symphony/go/internal/observability"
	"github.com/openai/symphony/go/internal/orchestrator"
	"github.com/openai/symphony/go/internal/tracker"
	"github.com/openai/symphony/go/internal/workflow"
)

type agentRunFunc func(context.Context, domain.Issue, agent.Options) error

type workerDone struct {
	issueID   string
	err       error
	sessionID string
}

type workerUpdate struct {
	issueID string
	update  orchestrator.CodexUpdate
}

// Options configures the live service.
type Options struct {
	Tracker         tracker.Client
	AgentRun        agentRunFunc
	SnapshotTimeout time.Duration
	Now             func() time.Time
}

// Service owns the live runtime wiring around the pure orchestrator state package.
type Service struct {
	mu              sync.RWMutex
	state           orchestrator.State
	tracker         tracker.Client
	agentRun        agentRunFunc
	snapshotTimeout time.Duration
	now             func() time.Time

	refreshCh    chan struct{}
	workerDoneCh chan workerDone
	workerUpCh   chan workerUpdate
	stopCh       chan struct{}
	doneCh       chan struct{}

	httpServer   *http.Server
	httpListener net.Listener
}

var (
	defaultMu      sync.Mutex
	defaultService *Service
)

// EnsureStarted boots the default live service once.
func EnsureStarted() error {
	defaultMu.Lock()
	defer defaultMu.Unlock()

	if defaultService != nil {
		return nil
	}

	service, err := Start(Options{})
	if err != nil {
		return err
	}
	defaultService = service
	return nil
}

// WaitForShutdown blocks until the default service stops or the process receives a termination signal.
func WaitForShutdown() int {
	defaultMu.Lock()
	service := defaultService
	defaultMu.Unlock()

	if service == nil {
		return 1
	}

	signalCh := make(chan os.Signal, 1)
	signal.Notify(signalCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signalCh)

	select {
	case <-signalCh:
		_ = service.Stop()
		<-service.doneCh
		return 0
	case <-service.doneCh:
		return 0
	}
}

// StopDefault stops and clears the default service. Intended for tests.
func StopDefault() error {
	defaultMu.Lock()
	service := defaultService
	defaultService = nil
	defaultMu.Unlock()

	if service == nil {
		return nil
	}
	return service.Stop()
}

// Start creates and runs one live service.
func Start(opts Options) (*Service, error) {
	if err := validateStartup(); err != nil {
		return nil, err
	}
	if err := workflow.StartDefaultPolling(); err != nil {
		return nil, err
	}

	nowFn := opts.Now
	if nowFn == nil {
		nowFn = time.Now
	}
	trackerClient := opts.Tracker
	if trackerClient == nil {
		trackerClient = tracker.Default()
	}
	runAgent := opts.AgentRun
	if runAgent == nil {
		runAgent = func(ctx context.Context, issue domain.Issue, agentOpts agent.Options) error {
			agentOpts.Context = ctx
			return agent.Run(issue, agentOpts)
		}
	}
	snapshotTimeout := opts.SnapshotTimeout
	if snapshotTimeout <= 0 {
		snapshotTimeout = 15 * time.Second
	}

	service := &Service{
		state:           orchestrator.NewState(nowFn().UTC()),
		tracker:         trackerClient,
		agentRun:        runAgent,
		snapshotTimeout: snapshotTimeout,
		now:             func() time.Time { return nowFn().UTC() },
		refreshCh:       make(chan struct{}, 1),
		workerDoneCh:    make(chan workerDone, 32),
		workerUpCh:      make(chan workerUpdate, 128),
		stopCh:          make(chan struct{}),
		doneCh:          make(chan struct{}),
	}

	if err := service.startHTTPServer(); err != nil {
		workflow.StopDefaultPolling()
		return nil, err
	}

	go service.loop()
	service.queueRefresh()

	return service, nil
}

// Stop stops the live service.
func (s *Service) Stop() error {
	select {
	case <-s.doneCh:
		workflow.StopDefaultPolling()
		return nil
	default:
		s.mu.RLock()
		for _, entry := range s.state.Running {
			if entry.Stop != nil {
				entry.Stop()
			}
		}
		s.mu.RUnlock()
		close(s.stopCh)
		<-s.doneCh
		workflow.StopDefaultPolling()
		return nil
	}
}

// Snapshot returns the current snapshot.
func (s *Service) Snapshot(_ time.Duration) (orchestrator.Snapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return orchestrator.SnapshotState(s.state, s.now()), nil
}

// RequestRefresh queues a refresh request.
func (s *Service) RequestRefresh() (observability.RefreshResult, error) {
	select {
	case <-s.doneCh:
		return observability.RefreshResult{}, observability.ErrRefreshUnavailable
	default:
	}

	coalesced := true
	select {
	case s.refreshCh <- struct{}{}:
		coalesced = false
	default:
	}

	return observability.RefreshResult{
		Queued:      true,
		Coalesced:   coalesced,
		RequestedAt: s.now(),
		Operations:  []string{"poll", "reconcile"},
	}, nil
}

func (s *Service) loop() {
	defer close(s.doneCh)

	timer := time.NewTimer(0)
	defer timer.Stop()

	for {
		select {
		case <-timer.C:
			s.runPollCycle()
			timer.Reset(time.Duration(s.currentPollInterval()) * time.Millisecond)

		case <-s.refreshCh:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(0)

		case update := <-s.workerUpCh:
			s.mu.Lock()
			s.state = orchestrator.ApplyCodexUpdate(s.state, update.issueID, update.update)
			s.mu.Unlock()

		case done := <-s.workerDoneCh:
			s.mu.Lock()
			runningEntry, hasRunningEntry := s.state.Running[done.issueID]
			if hasRunningEntry {
				if strings.TrimSpace(done.sessionID) != "" && strings.TrimSpace(runningEntry.SessionID) == "" {
					runningEntry.SessionID = done.sessionID
					s.state.Running[done.issueID] = runningEntry
				}
				sessionID := resolvedSessionID(runningEntry, done.sessionID)
				if done.err == nil || errors.Is(done.err, orchestrator.ErrWorkerNormal) {
					log.Printf("Agent task completed for issue_id=%s session_id=%s; scheduling active-state continuation check", done.issueID, sessionID)
				} else {
					log.Printf("Agent task exited for issue_id=%s session_id=%s reason=%s; scheduling retry", done.issueID, sessionID, workerReason(done.err))
				}
			}
			s.state = orchestrator.HandleWorkerExit(s.state, done.issueID, done.err, s.now())
			if hasRunningEntry {
				sessionID := resolvedSessionID(runningEntry, done.sessionID)
				log.Printf("Agent task finished for issue_id=%s session_id=%s reason=%s", done.issueID, sessionID, workerReason(done.err))
				if retry, ok := s.state.RetryAttempts[done.issueID]; ok {
					logRetryScheduled(done.issueID, retry, s.now())
				}
			}
			s.mu.Unlock()

		case <-s.stopCh:
			if s.httpServer != nil {
				_ = s.httpServer.Close()
			}
			return
		}
	}
}

func (s *Service) runPollCycle() {
	s.mu.Lock()
	cfg := config.Current()
	s.state.PollIntervalMS = cfg.PollIntervalMS
	s.state.MaxConcurrentAgents = cfg.MaxConcurrentAgents
	s.state.PollCheckInProgress = true
	s.state.NextPollDueAtMS = nil
	s.mu.Unlock()

	if err := validateStartup(); err == nil {
		s.reconcileRunningIssues()
		s.dispatchIssues()
	} else {
		log.Printf("Startup validation failed: %v", err)
	}

	s.mu.Lock()
	pollInterval := s.state.PollIntervalMS
	if pollInterval <= 0 {
		pollInterval = 30_000
	}
	due := s.now().Add(time.Duration(pollInterval) * time.Millisecond).UnixMilli()
	s.state.PollCheckInProgress = false
	s.state.NextPollDueAtMS = &due
	s.mu.Unlock()
}

func (s *Service) reconcileRunningIssues() {
	s.mu.RLock()
	ids := make([]string, 0, len(s.state.Running))
	for issueID := range s.state.Running {
		ids = append(ids, issueID)
	}
	s.mu.RUnlock()

	if len(ids) == 0 {
		return
	}

	issues, err := s.tracker.FetchIssueStatesByIDs(ids)
	if err != nil {
		log.Printf("Failed to refresh running issue states: %v; keeping active workers", err)
		return
	}

	s.mu.Lock()
	for _, issue := range issues {
		logIssueReconcileTransition(s.state, issue)
	}
	s.state = orchestrator.ReconcileIssueStates(issues, s.state)
	s.mu.Unlock()
}

func (s *Service) dispatchIssues() {
	candidates, err := s.tracker.FetchCandidateIssues()
	if err != nil {
		log.Printf("Failed to fetch from Linear: %v", err)
		return
	}
	now := s.now()

	s.mu.Lock()
	defer s.mu.Unlock()

	for _, issue := range orchestrator.SortIssuesForDispatch(candidates) {
		if !orchestrator.ShouldDispatchIssue(issue, s.state) {
			continue
		}

		revalidated, skipped, err := orchestrator.RevalidateIssueForDispatch(issue, s.tracker.FetchIssueStatesByIDs)
		if err != nil {
			log.Printf("Skipping dispatch; issue refresh failed for %s: %v", issueContext(issue), err)
			continue
		}
		if skipped {
			if revalidated.ID == "" {
				log.Printf("Skipping dispatch; issue no longer active or visible: %s", issueContext(issue))
			} else {
				log.Printf("Skipping stale dispatch after issue refresh: %s state=%s blocked_by=%d", issueContext(revalidated), revalidated.State, len(revalidated.BlockedBy))
			}
			continue
		}
		attempt := retryAttemptForIssue(s.state, revalidated.ID)
		log.Printf("Dispatching issue to agent: %s attempt=%s", issueContext(revalidated), attemptString(attempt))

		ctx, cancel := context.WithCancel(context.Background())
		entry := orchestrator.RunningEntry{
			Identifier: revalidated.Identifier,
			Issue:      revalidated,
			StartedAt:  now,
			Stop:       cancel,
		}
		s.state.Running[revalidated.ID] = entry
		s.state.Claimed[revalidated.ID] = struct{}{}
		delete(s.state.RetryAttempts, revalidated.ID)

		go s.runAgent(ctx, revalidated)
	}
}

func (s *Service) runAgent(ctx context.Context, issue domain.Issue) {
	var lastSessionID string
	err := s.agentRun(ctx, issue, agent.Options{
		Context:           ctx,
		IssueStateFetcher: s.tracker.FetchIssueStatesByIDs,
		OnCodexUpdate: func(update map[string]any) {
			if sessionID := stringValue(update["session_id"]); strings.TrimSpace(sessionID) != "" {
				lastSessionID = sessionID
			}
			s.workerUpCh <- workerUpdate{
				issueID: issue.ID,
				update: orchestrator.CodexUpdate{
					Event:             stringValue(update["event"]),
					SessionID:         stringValue(update["session_id"]),
					Payload:           update,
					CodexAppServerPID: stringValue(update["codex_app_server_pid"]),
					Timestamp:         timeValue(update["timestamp"]),
				},
			}
		},
	})
	if err != nil {
		s.workerDoneCh <- workerDone{issueID: issue.ID, err: err, sessionID: lastSessionID}
		return
	}
	s.workerDoneCh <- workerDone{issueID: issue.ID, err: orchestrator.ErrWorkerNormal, sessionID: lastSessionID}
}

func (s *Service) currentPollInterval() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.state.PollIntervalMS <= 0 {
		return 30_000
	}
	return s.state.PollIntervalMS
}

func (s *Service) queueRefresh() {
	select {
	case s.refreshCh <- struct{}{}:
	default:
	}
}

func (s *Service) startHTTPServer() error {
	addr, err := httpapi.ListenAddress()
	if err != nil {
		return nil
	}

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}

	server := &http.Server{
		Handler: httpapi.Handler(s, s.snapshotTimeout),
	}
	s.httpServer = server
	s.httpListener = listener
	go func() {
		_ = server.Serve(listener)
	}()
	return nil
}

func validateStartup() error {
	return config.Validate()
}

func stringValue(value any) string {
	raw, _ := value.(string)
	return raw
}

func timeValue(value any) time.Time {
	if ts, ok := value.(time.Time); ok {
		return ts
	}
	return time.Time{}
}

func logIssueReconcileTransition(state orchestrator.State, issue domain.Issue) {
	if _, ok := state.Running[issue.ID]; !ok {
		return
	}

	switch {
	case terminalIssueState(issue.State):
		log.Printf("Issue moved to terminal state: %s state=%s; stopping active agent", issueContext(issue), issue.State)
	case !issueRoutableToWorker(issue):
		log.Printf("Issue no longer routed to this worker: %s assignee=%q; stopping active agent", issueContext(issue), issue.AssigneeID)
	case !activeIssueState(issue.State):
		log.Printf("Issue moved to non-active state: %s state=%s; stopping active agent", issueContext(issue), issue.State)
	}
}

func activeIssueState(state string) bool {
	normalized := normalizeIssueState(state)
	for _, activeState := range config.Current().LinearActiveStates {
		if normalizeIssueState(activeState) == normalized {
			return true
		}
	}
	return false
}

func terminalIssueState(state string) bool {
	normalized := normalizeIssueState(state)
	for _, terminalState := range config.Current().LinearTerminalStates {
		if normalizeIssueState(terminalState) == normalized {
			return true
		}
	}
	return false
}

func issueRoutableToWorker(issue domain.Issue) bool {
	if issue.AssignedToWorker {
		return true
	}
	return issue.AssigneeID == ""
}

func normalizeIssueState(state string) string {
	return strings.ToLower(strings.TrimSpace(state))
}

func issueContext(issue domain.Issue) string {
	return "issue_id=" + issue.ID + " issue_identifier=" + issue.Identifier
}

func runningEntrySessionID(entry orchestrator.RunningEntry) string {
	if strings.TrimSpace(entry.SessionID) == "" {
		return "n/a"
	}
	return entry.SessionID
}

func resolvedSessionID(entry orchestrator.RunningEntry, fallback string) string {
	if strings.TrimSpace(entry.SessionID) != "" {
		return entry.SessionID
	}
	if strings.TrimSpace(fallback) != "" {
		return fallback
	}
	return "n/a"
}

func workerReason(err error) string {
	if err == nil {
		return "nil"
	}
	return err.Error()
}

func retryAttemptForIssue(state orchestrator.State, issueID string) int {
	if retry, ok := state.RetryAttempts[issueID]; ok {
		return retry.Attempt
	}
	return 0
}

func attemptString(attempt int) string {
	if attempt <= 0 {
		return "<nil>"
	}
	return strconv.Itoa(attempt)
}

func logRetryScheduled(issueID string, retry orchestrator.RetryEntry, now time.Time) {
	errorSuffix := ""
	if strings.TrimSpace(retry.Error) != "" {
		errorSuffix = " error=" + retry.Error
	}
	delayMS := retry.DueAtMS - now.UnixMilli()
	if delayMS < 0 {
		delayMS = 0
	}
	log.Printf("Retrying issue_id=%s issue_identifier=%s in %dms (attempt %d)%s", issueID, retry.Identifier, delayMS, retry.Attempt, errorSuffix)
}
