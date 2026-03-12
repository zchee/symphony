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

// Options controls one agent run.
type Options struct {
	MaxTurns          int
	IssueStateFetcher IssueStateFetcher
	OnCodexUpdate     CodexUpdateHandler
	Context           context.Context
}

// Run executes one issue in a workspace until completion, inactivity, or max turns.
func Run(issue domain.Issue, opts Options) error {
	log.Printf("Starting agent run for %s", issueContext(issue))
	workspacePath, err := workspace.CreateForIssue(issue.Identifier)
	if err != nil {
		logAgentFailure(issue, err)
		return fmt.Errorf("agent run failed for %s: %w", issueContext(issue), err)
	}

	if err := workspace.RunBeforeRunHook(workspacePath); err != nil {
		logAgentFailure(issue, err)
		return fmt.Errorf("agent run failed for %s: %w", issueContext(issue), err)
	}
	defer workspace.RunAfterRunHook(workspacePath)

	maxTurns := opts.MaxTurns
	if maxTurns <= 0 {
		maxTurns = config.Current().AgentMaxTurns
	}
	fetcher := opts.IssueStateFetcher
	if fetcher == nil {
		defaultTracker := tracker.Default()
		fetcher = defaultTracker.FetchIssueStatesByIDs
	}

	session, err := codex.StartSession(workspacePath)
	if err != nil {
		logAgentFailure(issue, err)
		return fmt.Errorf("agent run failed for %s: %w", issueContext(issue), err)
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
				logAgentFailure(currentIssue, opts.Context.Err())
				return opts.Context.Err()
			default:
			}
		}

		turnPrompt, err := buildTurnPrompt(currentIssue, turnNumber, maxTurns)
		if err != nil {
			return err
		}

		runOpts := codex.RunOptions{}
		if opts.OnCodexUpdate != nil {
			runOpts.OnMessage = codex.MessageHandler(opts.OnCodexUpdate)
		}
		result, err := codex.RunTurn(session, turnPrompt, currentIssue, runOpts)
		if err != nil {
			logAgentFailure(currentIssue, err)
			return fmt.Errorf("agent run failed for %s: %w", issueContext(currentIssue), err)
		}
		log.Printf("Completed agent run for %s session_id=%s workspace=%s turn=%d/%d", issueContext(currentIssue), result.SessionID, workspacePath, turnNumber, maxTurns)

		nextIssue, done, err := continueWithIssue(currentIssue, fetcher)
		if err != nil {
			logAgentFailure(currentIssue, err)
			return fmt.Errorf("agent run failed for %s: %w", issueContext(currentIssue), err)
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
	normalized := strings.ToLower(strings.TrimSpace(state))
	for _, activeState := range config.Current().LinearActiveStates {
		if strings.ToLower(strings.TrimSpace(activeState)) == normalized {
			return true
		}
	}
	return false
}

func issueContext(issue domain.Issue) string {
	return "issue_id=" + issue.ID + " issue_identifier=" + issue.Identifier
}

func logAgentFailure(issue domain.Issue, reason error) {
	log.Printf("Agent run failed for %s: %v", issueContext(issue), reason)
}
