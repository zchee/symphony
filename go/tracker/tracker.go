package tracker

import "github.com/openai/symphony/go/domain"

// Client defines the tracker-facing read and write contract used by Symphony.
type Client interface {
	FetchCandidateIssues() ([]domain.Issue, error)
	FetchIssuesByStates([]string) ([]domain.Issue, error)
	FetchIssueStatesByIDs([]string) ([]domain.Issue, error)
	CreateComment(issueID, body string) error
	UpdateIssueState(issueID, stateName string) error
}
