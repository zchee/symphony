package linear

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/openai/symphony/go/internal/config"
	"github.com/openai/symphony/go/internal/domain"
	"github.com/openai/symphony/go/internal/runtimeconfig"
)

func TestNormalizeIssueForBlockersLabelsPriorityAndAssignment(t *testing.T) {
	rawIssue := map[string]any{
		"id":          "issue-1",
		"identifier":  "MT-1",
		"title":       "Blocked todo",
		"description": "Needs dependency",
		"priority":    2,
		"state":       map[string]any{"name": "Todo"},
		"branchName":  "mt-1",
		"url":         "https://example.org/issues/MT-1",
		"assignee":    map[string]any{"id": "user-1"},
		"labels": map[string]any{
			"nodes": []any{
				map[string]any{"name": "Backend"},
			},
		},
		"inverseRelations": map[string]any{
			"nodes": []any{
				map[string]any{
					"type": "blocks",
					"issue": map[string]any{
						"id":         "issue-2",
						"identifier": "MT-2",
						"state":      map[string]any{"name": "In Progress"},
					},
				},
				map[string]any{
					"type": "relatesTo",
					"issue": map[string]any{
						"id":         "issue-3",
						"identifier": "MT-3",
						"state":      map[string]any{"name": "Done"},
					},
				},
			},
		},
		"createdAt": "2026-01-01T00:00:00Z",
		"updatedAt": "2026-01-02T00:00:00Z",
	}

	issue, ok := normalizeIssue(rawIssue, &assigneeFilter{matchValues: map[string]struct{}{"user-1": {}}})
	if !ok {
		t.Fatal("normalizeIssue() returned ok=false, want true")
	}

	if len(issue.BlockedBy) != 1 || issue.BlockedBy[0].Identifier != "MT-2" {
		t.Fatalf("issue.BlockedBy = %#v, want MT-2 blocker only", issue.BlockedBy)
	}
	if len(issue.Labels) != 1 || issue.Labels[0] != "backend" {
		t.Fatalf("issue.Labels = %#v, want [backend]", issue.Labels)
	}
	if issue.Priority == nil || *issue.Priority != 2 {
		t.Fatalf("issue.Priority = %#v, want 2", issue.Priority)
	}
	if issue.State != "Todo" {
		t.Fatalf("issue.State = %q, want %q", issue.State, "Todo")
	}
	if issue.AssigneeID != "user-1" {
		t.Fatalf("issue.AssigneeID = %q, want %q", issue.AssigneeID, "user-1")
	}
	if !issue.AssignedToWorker {
		t.Fatal("issue.AssignedToWorker = false, want true")
	}
	if issue.CreatedAt == nil || issue.UpdatedAt == nil {
		t.Fatalf("issue timestamps = %#v / %#v, want non-nil", issue.CreatedAt, issue.UpdatedAt)
	}
}

func TestNormalizeIssueMarksExplicitlyUnassignedIssuesAsNotAssignedToWorker(t *testing.T) {
	rawIssue := map[string]any{
		"id":         "issue-99",
		"identifier": "MT-99",
		"title":      "Someone else's task",
		"state":      map[string]any{"name": "Todo"},
		"assignee":   map[string]any{"id": "user-2"},
	}

	issue, ok := normalizeIssue(rawIssue, &assigneeFilter{matchValues: map[string]struct{}{"user-1": {}}})
	if !ok {
		t.Fatal("normalizeIssue() returned ok=false, want true")
	}
	if issue.AssignedToWorker {
		t.Fatal("issue.AssignedToWorker = true, want false")
	}
}

func TestFetchIssueStatesByIDsEmptyReturnsEmpty(t *testing.T) {
	client := NewClient()
	issues, err := client.FetchIssueStatesByIDs(nil)
	if err != nil {
		t.Fatalf("FetchIssueStatesByIDs(nil) returned error: %v", err)
	}
	if len(issues) != 0 {
		t.Fatalf("len(issues) = %d, want 0", len(issues))
	}
}

func TestFetchIssuesByStatesEmptyReturnsEmpty(t *testing.T) {
	client := NewClient()
	issues, err := client.FetchIssuesByStates(nil)
	if err != nil {
		t.Fatalf("FetchIssuesByStates(nil) returned error: %v", err)
	}
	if len(issues) != 0 {
		t.Fatalf("len(issues) = %d, want 0", len(issues))
	}
}

func TestPaginationMergePreservesIssueOrdering(t *testing.T) {
	merged := []domain.Issue{}
	merged = append(merged, []domain.Issue{{ID: "issue-1", Identifier: "MT-1"}, {ID: "issue-2", Identifier: "MT-2"}}...)
	merged = append(merged, []domain.Issue{{ID: "issue-3", Identifier: "MT-3"}}...)

	if len(merged) != 3 || merged[0].Identifier != "MT-1" || merged[1].Identifier != "MT-2" || merged[2].Identifier != "MT-3" {
		t.Fatalf("merged issues = %#v, want MT-1, MT-2, MT-3", merged)
	}
}

func TestGraphQLReturnsStatusErrorOnNon200(t *testing.T) {
	client := &Client{
		Request: func(_ string, _ map[string]any, _ map[string]string) (Response, error) {
			return Response{
				Status: 400,
				Body: map[string]any{
					"errors": []any{
						map[string]any{
							"message":    `Variable "$ids" got invalid value`,
							"extensions": map[string]any{"code": "BAD_USER_INPUT"},
						},
					},
				},
			}, nil
		},
	}
	writeLinearWorkflow(t, map[string]any{
		"tracker": map[string]any{
			"kind":         "linear",
			"api_key":      "token",
			"project_slug": "project",
		},
	})

	_, err := client.graphql("query Viewer { viewer { id } }", map[string]any{}, "")
	var statusErr *StatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("graphql() error = %v, want StatusError", err)
	}
	if statusErr.Status != 400 {
		t.Fatalf("statusErr.Status = %d, want 400", statusErr.Status)
	}
}

func TestGraphQLReturnsRequestError(t *testing.T) {
	client := &Client{
		Request: func(_ string, _ map[string]any, _ map[string]string) (Response, error) {
			return Response{}, errors.New("boom")
		},
	}
	writeLinearWorkflow(t, map[string]any{
		"tracker": map[string]any{
			"kind":         "linear",
			"api_key":      "token",
			"project_slug": "project",
		},
	})

	_, err := client.graphql("query Viewer { viewer { id } }", map[string]any{}, "")
	var requestErr *RequestError
	if !errors.As(err, &requestErr) {
		t.Fatalf("graphql() error = %v, want RequestError", err)
	}
}

func TestDecodeLinearResponseReturnsGraphQLErrors(t *testing.T) {
	_, err := decodeLinearResponse(map[string]any{
		"errors": []any{map[string]any{"message": "boom"}},
	}, nil)
	var graphQLErrors *GraphQLErrors
	if !errors.As(err, &graphQLErrors) {
		t.Fatalf("decodeLinearResponse() error = %v, want GraphQLErrors", err)
	}
}

func TestNextPageCursorRequiresEndCursorWhenHasNextPage(t *testing.T) {
	_, done, err := nextPageCursor(pageInfo{HasNextPage: true})
	if err == nil || !errors.Is(err, ErrMissingEndCursor) {
		t.Fatalf("nextPageCursor() error = %v, want ErrMissingEndCursor", err)
	}
	if done {
		t.Fatal("nextPageCursor() done = true, want false")
	}
}

func TestBuildAssigneeFilterResolvesViewerForMe(t *testing.T) {
	client := &Client{
		Request: func(_ string, _ map[string]any, _ map[string]string) (Response, error) {
			return Response{
				Status: 200,
				Body: map[string]any{
					"data": map[string]any{
						"viewer": map[string]any{"id": "viewer-1"},
					},
				},
			}, nil
		},
	}
	writeLinearWorkflow(t, map[string]any{
		"tracker": map[string]any{
			"kind":         "linear",
			"api_key":      "token",
			"project_slug": "project",
			"assignee":     "me",
		},
	})

	filter, err := client.routingAssigneeFilter(config.Current())
	if err != nil {
		t.Fatalf("routingAssigneeFilter() returned error: %v", err)
	}
	if filter == nil {
		t.Fatal("routingAssigneeFilter() = nil, want filter")
	}
	if _, ok := filter.matchValues["viewer-1"]; !ok {
		t.Fatalf("filter.matchValues = %#v, want viewer-1", filter.matchValues)
	}
}

func TestFetchCandidateIssuesUsesConfiguredActiveStates(t *testing.T) {
	client := &Client{
		Request: func(_ string, payload map[string]any, _ map[string]string) (Response, error) {
			variables := payload["variables"].(map[string]any)
			stateNames := variables["stateNames"].([]string)
			if len(stateNames) != 2 || stateNames[0] != "Todo" || stateNames[1] != "In Progress" {
				t.Fatalf("stateNames = %#v, want configured active states", stateNames)
			}
			return Response{
				Status: 200,
				Body: map[string]any{
					"data": map[string]any{
						"issues": map[string]any{
							"nodes": []any{
								map[string]any{
									"id":         "issue-1",
									"identifier": "MT-1",
									"title":      "Candidate",
									"state":      map[string]any{"name": "Todo"},
								},
							},
							"pageInfo": map[string]any{
								"hasNextPage": false,
								"endCursor":   "",
							},
						},
					},
				},
			}, nil
		},
	}
	writeLinearWorkflow(t, map[string]any{
		"tracker": map[string]any{
			"kind":          "linear",
			"api_key":       "token",
			"project_slug":  "project",
			"active_states": []any{"Todo", "In Progress"},
		},
	})

	issues, err := client.FetchCandidateIssues()
	if err != nil {
		t.Fatalf("FetchCandidateIssues() returned error: %v", err)
	}
	if len(issues) != 1 || issues[0].Identifier != "MT-1" {
		t.Fatalf("FetchCandidateIssues() = %#v, want MT-1 candidate", issues)
	}
}

func TestCreateCommentMatchesAdapterSuccessAndFailureSemantics(t *testing.T) {
	writeLinearWorkflow(t, map[string]any{
		"tracker": map[string]any{
			"kind":         "linear",
			"api_key":      "token",
			"project_slug": "project",
		},
	})

	client := &Client{
		Request: func(_ string, payload map[string]any, _ map[string]string) (Response, error) {
			variables := payload["variables"].(map[string]any)
			if variables["issueId"] != "issue-1" || variables["body"] != "hello" {
				t.Fatalf("comment payload variables = %#v, want issue-1/hello", variables)
			}
			return Response{
				Status: 200,
				Body: map[string]any{
					"data": map[string]any{
						"commentCreate": map[string]any{"success": true},
					},
				},
			}, nil
		},
	}

	if err := client.CreateComment("issue-1", "hello"); err != nil {
		t.Fatalf("CreateComment() returned error: %v", err)
	}

	client.Request = func(_ string, _ map[string]any, _ map[string]string) (Response, error) {
		return Response{
			Status: 200,
			Body: map[string]any{
				"data": map[string]any{
					"commentCreate": map[string]any{"success": false},
				},
			},
		}, nil
	}
	if err := client.CreateComment("issue-1", "broken"); !errors.Is(err, ErrCommentCreateFailed) {
		t.Fatalf("CreateComment() error = %v, want ErrCommentCreateFailed", err)
	}

	client.Request = func(_ string, _ map[string]any, _ map[string]string) (Response, error) {
		return Response{}, errors.New("boom")
	}
	var requestErr *RequestError
	if err := client.CreateComment("issue-1", "boom"); !errors.As(err, &requestErr) {
		t.Fatalf("CreateComment() error = %v, want RequestError", err)
	}
}

func TestUpdateIssueStateMatchesAdapterSuccessAndFailureSemantics(t *testing.T) {
	writeLinearWorkflow(t, map[string]any{
		"tracker": map[string]any{
			"kind":         "linear",
			"api_key":      "token",
			"project_slug": "project",
		},
	})

	requests := 0
	client := &Client{
		Request: func(_ string, payload map[string]any, _ map[string]string) (Response, error) {
			requests++
			if requests == 1 {
				variables := payload["variables"].(map[string]any)
				if variables["issueId"] != "issue-1" || variables["stateName"] != "Done" {
					t.Fatalf("state lookup variables = %#v, want issue-1/Done", variables)
				}
				return Response{
					Status: 200,
					Body: map[string]any{
						"data": map[string]any{
							"issue": map[string]any{
								"team": map[string]any{
									"states": map[string]any{
										"nodes": []any{map[string]any{"id": "state-1"}},
									},
								},
							},
						},
					},
				}, nil
			}

			variables := payload["variables"].(map[string]any)
			if variables["issueId"] != "issue-1" || variables["stateId"] != "state-1" {
				t.Fatalf("update state variables = %#v, want issue-1/state-1", variables)
			}
			return Response{
				Status: 200,
				Body: map[string]any{
					"data": map[string]any{
						"issueUpdate": map[string]any{"success": true},
					},
				},
			}, nil
		},
	}

	if err := client.UpdateIssueState("issue-1", "Done"); err != nil {
		t.Fatalf("UpdateIssueState() returned error: %v", err)
	}

	requests = 0
	client.Request = func(_ string, _ map[string]any, _ map[string]string) (Response, error) {
		requests++
		if requests == 1 {
			return Response{
				Status: 200,
				Body: map[string]any{
					"data": map[string]any{
						"issue": map[string]any{
							"team": map[string]any{
								"states": map[string]any{
									"nodes": []any{map[string]any{"id": "state-1"}},
								},
							},
						},
					},
				},
			}, nil
		}
		return Response{
			Status: 200,
			Body: map[string]any{
				"data": map[string]any{
					"issueUpdate": map[string]any{"success": false},
				},
			},
		}, nil
	}
	if err := client.UpdateIssueState("issue-1", "Broken"); !errors.Is(err, ErrIssueUpdateFailed) {
		t.Fatalf("UpdateIssueState() error = %v, want ErrIssueUpdateFailed", err)
	}

	client.Request = func(_ string, _ map[string]any, _ map[string]string) (Response, error) {
		return Response{}, errors.New("boom")
	}
	var requestErr *RequestError
	if err := client.UpdateIssueState("issue-1", "Boom"); !errors.As(err, &requestErr) {
		t.Fatalf("UpdateIssueState() error = %v, want RequestError", err)
	}

	client.Request = func(_ string, _ map[string]any, _ map[string]string) (Response, error) {
		return Response{
			Status: 200,
			Body: map[string]any{
				"data": map[string]any{},
			},
		}, nil
	}
	if err := client.UpdateIssueState("issue-1", "Missing"); !errors.Is(err, ErrStateNotFound) {
		t.Fatalf("UpdateIssueState() error = %v, want ErrStateNotFound", err)
	}
}

func writeLinearWorkflow(t *testing.T, configMap map[string]any) {
	t.Helper()

	dir := t.TempDir()
	path := dir + "/WORKFLOW.md"
	content := mapToWorkflow(configMap)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("os.WriteFile(%q) failed: %v", path, err)
	}
	if err := runtimeconfig.SetWorkflowFilePath(path); err != nil {
		t.Fatalf("runtimeconfig.SetWorkflowFilePath(%q) failed: %v", path, err)
	}
}

func mapToWorkflow(configMap map[string]any) string {
	if configMap == nil {
		configMap = map[string]any{}
	}
	if _, ok := configMap["tracker"]; !ok {
		configMap["tracker"] = map[string]any{
			"kind":         "linear",
			"api_key":      "token",
			"project_slug": "project",
		}
	}
	body, _ := json.Marshal(configMap)
	var normalized map[string]any
	_ = json.Unmarshal(body, &normalized)

	lines := []string{"---"}
	if tracker, ok := normalized["tracker"].(map[string]any); ok {
		lines = append(lines, "tracker:")
		for _, key := range []string{"kind", "api_key", "project_slug", "assignee"} {
			if value, ok := tracker[key]; ok {
				lines = append(lines, fmt.Sprintf("  %s: %s", key, yamlScalar(value)))
			}
		}
		if states, ok := tracker["active_states"]; ok {
			lines = append(lines, fmt.Sprintf("  active_states: %s", yamlScalar(states)))
		}
	}
	lines = append(lines, "---", "")
	return strings.Join(lines, "\n")
}

func yamlScalar(value any) string {
	switch typed := value.(type) {
	case string:
		return fmt.Sprintf("%q", typed)
	case []any:
		parts := make([]string, len(typed))
		for index, entry := range typed {
			parts[index] = yamlScalar(entry)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	default:
		return fmt.Sprintf("%v", typed)
	}
}
