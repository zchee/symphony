package memory

import (
	"testing"

	"github.com/openai/symphony/go/internal/domain"
)

func TestFetchCandidateIssuesPreservesConfiguredOrder(t *testing.T) {
	client := &Client{
		Issues: []domain.Issue{
			{ID: "1", Identifier: "MT-1", State: "Todo"},
			{ID: "2", Identifier: "MT-2", State: "In Progress"},
		},
	}

	issues, err := client.FetchCandidateIssues()
	if err != nil {
		t.Fatalf("FetchCandidateIssues() returned error: %v", err)
	}

	if len(issues) != 2 || issues[0].Identifier != "MT-1" || issues[1].Identifier != "MT-2" {
		t.Fatalf("FetchCandidateIssues() = %#v, want configured issue order", issues)
	}
}

func TestFetchIssuesByStatesNormalizesStateNames(t *testing.T) {
	client := &Client{
		Issues: []domain.Issue{
			{ID: "1", Identifier: "MT-1", State: "Todo"},
			{ID: "2", Identifier: "MT-2", State: "In Progress"},
			{ID: "3", Identifier: "MT-3", State: "Closed"},
		},
	}

	issues, err := client.FetchIssuesByStates([]string{" todo ", "IN PROGRESS"})
	if err != nil {
		t.Fatalf("FetchIssuesByStates() returned error: %v", err)
	}

	if len(issues) != 2 || issues[0].Identifier != "MT-1" || issues[1].Identifier != "MT-2" {
		t.Fatalf("FetchIssuesByStates() = %#v, want Todo and In Progress issues", issues)
	}
}

func TestFetchIssueStatesByIDsFiltersConfiguredIssues(t *testing.T) {
	client := &Client{
		Issues: []domain.Issue{
			{ID: "1", Identifier: "MT-1", State: "Todo"},
			{ID: "2", Identifier: "MT-2", State: "In Progress"},
		},
	}

	issues, err := client.FetchIssueStatesByIDs([]string{"2"})
	if err != nil {
		t.Fatalf("FetchIssueStatesByIDs() returned error: %v", err)
	}

	if len(issues) != 1 || issues[0].Identifier != "MT-2" {
		t.Fatalf("FetchIssueStatesByIDs() = %#v, want MT-2 only", issues)
	}
}

func TestCreateCommentEmitsEvent(t *testing.T) {
	var events []Event
	client := &Client{
		OnEvent: func(event Event) {
			events = append(events, event)
		},
	}

	if err := client.CreateComment("issue-1", "hello"); err != nil {
		t.Fatalf("CreateComment() returned error: %v", err)
	}

	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want 1", len(events))
	}
	if events[0].Kind != EventKindComment || events[0].IssueID != "issue-1" || events[0].Body != "hello" {
		t.Fatalf("events[0] = %#v, want emitted comment event", events[0])
	}
}

func TestUpdateIssueStateEmitsEvent(t *testing.T) {
	var events []Event
	client := &Client{
		OnEvent: func(event Event) {
			events = append(events, event)
		},
	}

	if err := client.UpdateIssueState("issue-2", "Done"); err != nil {
		t.Fatalf("UpdateIssueState() returned error: %v", err)
	}

	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want 1", len(events))
	}
	if events[0].Kind != EventKindStateUpdate || events[0].IssueID != "issue-2" || events[0].StateName != "Done" {
		t.Fatalf("events[0] = %#v, want emitted state-update event", events[0])
	}
}

func TestCloneIssuesProtectsOriginalSlices(t *testing.T) {
	client := &Client{
		Issues: []domain.Issue{
			{
				ID:         "1",
				Identifier: "MT-1",
				Labels:     []string{"backend"},
				BlockedBy:  []domain.BlockerRef{{ID: "blocker-1", Identifier: "MT-2", State: "In Progress"}},
			},
		},
	}

	issues, err := client.FetchCandidateIssues()
	if err != nil {
		t.Fatalf("FetchCandidateIssues() returned error: %v", err)
	}

	issues[0].Labels[0] = "mutated"
	issues[0].BlockedBy[0].Identifier = "mutated"

	if client.Issues[0].Labels[0] != "backend" {
		t.Fatalf("client.Issues labels mutated: %#v", client.Issues[0].Labels)
	}
	if client.Issues[0].BlockedBy[0].Identifier != "MT-2" {
		t.Fatalf("client.Issues blocked_by mutated: %#v", client.Issues[0].BlockedBy)
	}
}
