package agent

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/openai/symphony/go/internal/codex"
	"github.com/openai/symphony/go/internal/config"
	"github.com/openai/symphony/go/internal/domain"
	"github.com/openai/symphony/go/internal/prompt"
	"github.com/openai/symphony/go/internal/tracker"
	"github.com/openai/symphony/go/internal/workspace"
)

// IssueStateFetcher refreshes issue state by ID.
type IssueStateFetcher func([]string) ([]domain.Issue, error)

// CodexUpdateHandler receives coarse Codex updates from the runner.
type CodexUpdateHandler func(map[string]any)

// RuntimeInfoHandler receives worker-host/workspace metadata after the workspace is prepared.
type RuntimeInfoHandler func(map[string]any)

// Options controls one agent run.
type Options struct {
	MaxTurns          int
	IssueStateFetcher IssueStateFetcher
	OnCodexUpdate     CodexUpdateHandler
	OnRuntimeInfo     RuntimeInfoHandler
	Context           context.Context
	WorkerHost        string
}

// Run executes one issue in a workspace until completion, inactivity, or max turns.
func Run(issue domain.Issue, opts Options) error {
	workerHosts := candidateWorkerHosts(opts.WorkerHost, config.Current().WorkerSSHHosts)
	log.Printf("Starting agent run for %s worker_hosts=%v", issueContext(issue), workerHostsForLog(workerHosts))

	var lastErr error
	for index, workerHost := range workerHosts {
		log.Printf("Starting worker attempt for %s worker_host=%s", issueContext(issue), workerHostForLog(workerHost))
		if err := runOnWorkerHost(issue, opts, workerHost); err != nil {
			lastErr = err
			if index < len(workerHosts)-1 {
				log.Printf("Agent run failed for %s worker_host=%s reason=%v; trying next worker host", issueContext(issue), workerHostForLog(workerHost), err)
				continue
			}
			logAgentFailure(issue, err)
			return fmt.Errorf("agent run failed for %s: %w", issueContext(issue), err)
		}
		return nil
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("no_worker_hosts_available")
	}
	logAgentFailure(issue, lastErr)
	return fmt.Errorf("agent run failed for %s: %w", issueContext(issue), lastErr)
}

func runOnWorkerHost(issue domain.Issue, opts Options, workerHost string) error {
	workspacePath, err := createWorkspace(issue.Identifier, workerHost)
	if err != nil {
		return err
	}

	if opts.OnRuntimeInfo != nil {
		opts.OnRuntimeInfo(map[string]any{
			"worker_host":    workerHost,
			"workspace_path": workspacePath,
		})
	}

	if err := runBeforeRunHook(workspacePath, workerHost); err != nil {
		return err
	}
	defer runAfterRunHook(workspacePath, workerHost)

	maxTurns := opts.MaxTurns
	if maxTurns <= 0 {
		maxTurns = config.Current().AgentMaxTurns
	}
	fetcher := opts.IssueStateFetcher
	if fetcher == nil {
		defaultTracker := tracker.Default()
		fetcher = defaultTracker.FetchIssueStatesByIDs
	}

	session, err := codex.StartSessionWithHost(workspacePath, workerHost)
	if err != nil {
		return err
	}
	defer codex.StopSession(session)

	if opts.Context != nil {
		go func() {
			<-opts.Context.Done()
			codex.StopSession(session)
		}()
	}

	currentIssue := issue
	for turnNumber := 1; turnNumber <= maxTurns; turnNumber++ {
		if opts.Context != nil {
			select {
			case <-opts.Context.Done():
				return opts.Context.Err()
			default:
			}
		}

		turnPrompt, err := buildTurnPrompt(currentIssue, turnNumber, maxTurns)
		if err != nil {
			return err
		}

		runOpts := codex.RunOptions{WorkerHost: workerHost}
		if opts.OnCodexUpdate != nil {
			runOpts.OnMessage = codex.MessageHandler(opts.OnCodexUpdate)
		}
		result, err := codex.RunTurn(session, turnPrompt, currentIssue, runOpts)
		if err != nil {
			return err
		}
		log.Printf("Completed agent run for %s session_id=%s workspace=%s turn=%d/%d", issueContext(currentIssue), result.SessionID, workspacePath, turnNumber, maxTurns)

		nextIssue, done, err := continueWithIssue(currentIssue, fetcher)
		if err != nil {
			return err
		}
		if done {
			return nil
		}
		if turnNumber >= maxTurns {
			log.Printf("Reached agent.max_turns for %s with issue still active; returning control to orchestrator", issueContext(nextIssue))
			return nil
		}
		log.Printf("Continuing agent run for %s after normal turn completion turn=%d/%d", issueContext(nextIssue), turnNumber, maxTurns)
		currentIssue = nextIssue
	}

	return nil
}

func buildTurnPrompt(issue domain.Issue, turnNumber, maxTurns int) (string, error) {
	if turnNumber == 1 {
		return prompt.Build(issue, nil)
	}

	return fmt.Sprintf(`Continuation guidance:

- The previous Codex turn completed normally, but the Linear issue is still in an active state.
- This is continuation turn #%d of %d for the current agent run.
- Resume from the current workspace and workpad state instead of restarting from scratch.
- The original task instructions and prior turn context are already present in this thread, so do not restate them before acting.
- Focus on the remaining ticket work and do not end the turn while the issue stays active unless you are truly blocked.
`, turnNumber, maxTurns), nil
}

func continueWithIssue(issue domain.Issue, fetcher IssueStateFetcher) (domain.Issue, bool, error) {
	if issue.ID == "" {
		return issue, true, nil
	}

	refreshed, err := fetcher([]string{issue.ID})
	if err != nil {
		return domain.Issue{}, false, fmt.Errorf("issue_state_refresh_failed: %w", err)
	}
	if len(refreshed) == 0 {
		return issue, true, nil
	}

	nextIssue := refreshed[0]
	if activeIssueState(nextIssue.State) {
		return nextIssue, false, nil
	}
	return nextIssue, true, nil
}

func activeIssueState(state string) bool {
	normalized := strings.ToLower(state)
	for _, activeState := range config.Current().LinearActiveStates {
		if strings.ToLower(activeState) == normalized {
			return true
		}
	}
	return false
}

func createWorkspace(issueIdentifier, workerHost string) (string, error) {
	if workerHost == "" {
		return workspace.CreateForIssue(issueIdentifier)
	}
	return workspace.CreateForIssueOnHost(issueIdentifier, workerHost)
}

func runBeforeRunHook(workspacePath, workerHost string) error {
	if workerHost == "" {
		return workspace.RunBeforeRunHook(workspacePath)
	}
	return workspace.RunBeforeRunHookOnHost(workspacePath, workerHost)
}

func runAfterRunHook(workspacePath, workerHost string) {
	if workerHost == "" {
		workspace.RunAfterRunHook(workspacePath)
		return
	}
	workspace.RunAfterRunHookOnHost(workspacePath, workerHost)
}

func candidateWorkerHosts(preferredHost string, configuredHosts []string) []string {
	trimmedPreferred := strings.TrimSpace(preferredHost)
	if len(configuredHosts) == 0 {
		if trimmedPreferred == "" {
			return []string{""}
		}
		return []string{trimmedPreferred}
	}

	hosts := make([]string, 0, len(configuredHosts)+1)
	seen := map[string]struct{}{}
	if trimmedPreferred != "" {
		hosts = append(hosts, trimmedPreferred)
		seen[trimmedPreferred] = struct{}{}
	}
	for _, host := range configuredHosts {
		trimmed := strings.TrimSpace(host)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		hosts = append(hosts, trimmed)
	}
	if len(hosts) == 0 {
		return []string{""}
	}
	return hosts
}

func workerHostsForLog(workerHosts []string) []string {
	result := make([]string, len(workerHosts))
	for index, workerHost := range workerHosts {
		result[index] = workerHostForLog(workerHost)
	}
	return result
}

func workerHostForLog(workerHost string) string {
	if strings.TrimSpace(workerHost) == "" {
		return "local"
	}
	return workerHost
}

func issueContext(issue domain.Issue) string {
	return "issue_id=" + issue.ID + " issue_identifier=" + issue.Identifier
}

func logAgentFailure(issue domain.Issue, reason error) {
	log.Printf("Agent run failed for %s: %v", issueContext(issue), reason)
}
