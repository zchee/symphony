package memory

import (
	"strings"

	"github.com/openai/symphony/go/internal/domain"
)

// EventKind identifies emitted memory-tracker side effects.
type EventKind string

const (
	EventKindComment     EventKind = "comment"
	EventKindStateUpdate EventKind = "state_update"
)

// Event records a comment or state-update side effect.
type Event struct {
	Kind      EventKind
	IssueID   string
	Body      string
	StateName string
}

// Client is a deterministic in-memory tracker implementation for tests and local wiring.
type Client struct {
	Issues  []domain.Issue
	OnEvent func(Event)
}

// FetchCandidateIssues returns the configured issues in their stored order.
func (c *Client) FetchCandidateIssues() ([]domain.Issue, error) {
	return cloneIssues(c.Issues), nil
}

// FetchIssuesByStates filters configured issues by normalized issue state.
func (c *Client) FetchIssuesByStates(stateNames []string) ([]domain.Issue, error) {
	wanted := make(map[string]struct{}, len(stateNames))
	for _, state := range stateNames {
		wanted[normalizeState(state)] = struct{}{}
	}

	filtered := make([]domain.Issue, 0, len(c.Issues))
	for _, issue := range c.Issues {
		if _, ok := wanted[normalizeState(issue.State)]; ok {
			filtered = append(filtered, cloneIssue(issue))
		}
	}

	return filtered, nil
}

// FetchIssueStatesByIDs filters configured issues by stable issue ID.
func (c *Client) FetchIssueStatesByIDs(issueIDs []string) ([]domain.Issue, error) {
	wanted := make(map[string]struct{}, len(issueIDs))
	for _, id := range issueIDs {
		wanted[id] = struct{}{}
	}

	filtered := make([]domain.Issue, 0, len(c.Issues))
	for _, issue := range c.Issues {
		if _, ok := wanted[issue.ID]; ok {
			filtered = append(filtered, cloneIssue(issue))
		}
	}

	return filtered, nil
}

// CreateComment records a comment side effect.
func (c *Client) CreateComment(issueID, body string) error {
	c.emit(Event{
		Kind:    EventKindComment,
		IssueID: issueID,
		Body:    body,
	})
	return nil
}

// UpdateIssueState records a state-update side effect.
func (c *Client) UpdateIssueState(issueID, stateName string) error {
	c.emit(Event{
		Kind:      EventKindStateUpdate,
		IssueID:   issueID,
		StateName: stateName,
	})
	return nil
}

func (c *Client) emit(event Event) {
	if c != nil && c.OnEvent != nil {
		c.OnEvent(event)
	}
}

func normalizeState(state string) string {
	return strings.ToLower(state)
}

func cloneIssues(issues []domain.Issue) []domain.Issue {
	cloned := make([]domain.Issue, len(issues))
	for index, issue := range issues {
		cloned[index] = cloneIssue(issue)
	}
	return cloned
}

func cloneIssue(issue domain.Issue) domain.Issue {
	cloned := issue
	cloned.Labels = append([]string(nil), issue.Labels...)
	cloned.BlockedBy = append([]domain.BlockerRef(nil), issue.BlockedBy...)
	return cloned
}
