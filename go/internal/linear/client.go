package linear

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/openai/symphony/go/internal/config"
	"github.com/openai/symphony/go/internal/domain"
)

const issuePageSize = 50

const pollQuery = `
query SymphonyLinearPoll($projectSlug: String!, $stateNames: [String!]!, $first: Int!, $relationFirst: Int!, $after: String) {
  issues(filter: {project: {slugId: {eq: $projectSlug}}, state: {name: {in: $stateNames}}}, first: $first, after: $after) {
    nodes {
      id
      identifier
      title
      description
      priority
      state { name }
      branchName
      url
      assignee { id }
      labels { nodes { name } }
      inverseRelations(first: $relationFirst) {
        nodes {
          type
          issue {
            id
            identifier
            state { name }
          }
        }
      }
      createdAt
      updatedAt
    }
    pageInfo {
      hasNextPage
      endCursor
    }
  }
}
`

const issuesByIDQuery = `
query SymphonyLinearIssuesById($ids: [ID!]!, $first: Int!, $relationFirst: Int!) {
  issues(filter: {id: {in: $ids}}, first: $first) {
    nodes {
      id
      identifier
      title
      description
      priority
      state { name }
      branchName
      url
      assignee { id }
      labels { nodes { name } }
      inverseRelations(first: $relationFirst) {
        nodes {
          type
          issue {
            id
            identifier
            state { name }
          }
        }
      }
      createdAt
      updatedAt
    }
  }
}
`

const viewerQuery = `
query SymphonyLinearViewer {
  viewer {
    id
  }
}
`

const createCommentMutation = `
mutation SymphonyCreateComment($issueId: String!, $body: String!) {
  commentCreate(input: {issueId: $issueId, body: $body}) {
    success
  }
}
`

const updateIssueStateMutation = `
mutation SymphonyUpdateIssueState($issueId: String!, $stateId: String!) {
  issueUpdate(id: $issueId, input: {stateId: $stateId}) {
    success
  }
}
`

const resolveStateIDQuery = `
query SymphonyResolveStateId($issueId: String!, $stateName: String!) {
  issue(id: $issueId) {
    team {
      states(filter: {name: {eq: $stateName}}, first: 1) {
        nodes {
          id
        }
      }
    }
  }
}
`

var (
	ErrMissingViewerIdentity = errors.New("missing_linear_viewer_identity")
	ErrUnknownPayload        = errors.New("linear_unknown_payload")
	ErrMissingEndCursor      = errors.New("linear_missing_end_cursor")
	ErrCommentCreateFailed   = errors.New("comment_create_failed")
	ErrIssueUpdateFailed     = errors.New("issue_update_failed")
	ErrStateNotFound         = errors.New("state_not_found")
)

// StatusError reports a non-200 Linear response.
type StatusError struct {
	Status int
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("linear_api_status: %d", e.Status)
}

// RequestError reports a failed transport request.
type RequestError struct {
	Reason error
}

func (e *RequestError) Error() string {
	return fmt.Sprintf("linear_api_request: %v", e.Reason)
}

func (e *RequestError) Unwrap() error {
	return e.Reason
}

// GraphQLErrors reports top-level GraphQL errors returned by Linear.
type GraphQLErrors struct {
	Errors []any
}

func (e *GraphQLErrors) Error() string {
	return fmt.Sprintf("linear_graphql_errors: %d error(s)", len(e.Errors))
}

// Response is the normalized request function output.
type Response struct {
	Status int
	Body   map[string]any
}

// RequestFunc executes a GraphQL request with already-built headers.
type RequestFunc func(endpoint string, payload map[string]any, headers map[string]string) (Response, error)

// Client is the real Linear GraphQL tracker implementation.
type Client struct {
	Request RequestFunc
}

// NewClient creates a Linear client backed by the default HTTP transport.
func NewClient() *Client {
	return &Client{Request: defaultRequest}
}

// GraphQL executes a raw Linear GraphQL request.
func (c *Client) GraphQL(query string, variables map[string]any, operationName string) (map[string]any, error) {
	return c.graphql(query, variables, operationName)
}

// FetchCandidateIssues returns issues in active states for the configured project.
func (c *Client) FetchCandidateIssues() ([]domain.Issue, error) {
	cfg := config.Current()
	if cfg.LinearAPIToken == "" {
		return nil, config.ErrMissingLinearAPIToken
	}
	if cfg.LinearProjectSlug == "" {
		return nil, config.ErrMissingLinearProjectSlug
	}

	assigneeFilter, err := c.routingAssigneeFilter(cfg)
	if err != nil {
		return nil, err
	}

	return c.fetchByStates(cfg.LinearProjectSlug, cfg.LinearActiveStates, assigneeFilter)
}

// FetchIssuesByStates returns issues in the requested states for the configured project.
func (c *Client) FetchIssuesByStates(stateNames []string) ([]domain.Issue, error) {
	normalizedStates := uniqueStrings(stateNames)
	if len(normalizedStates) == 0 {
		return []domain.Issue{}, nil
	}

	cfg := config.Current()
	if cfg.LinearAPIToken == "" {
		return nil, config.ErrMissingLinearAPIToken
	}
	if cfg.LinearProjectSlug == "" {
		return nil, config.ErrMissingLinearProjectSlug
	}

	return c.fetchByStates(cfg.LinearProjectSlug, normalizedStates, nil)
}

// FetchIssueStatesByIDs returns normalized issues by stable ID.
func (c *Client) FetchIssueStatesByIDs(issueIDs []string) ([]domain.Issue, error) {
	ids := uniqueStrings(issueIDs)
	if len(ids) == 0 {
		return []domain.Issue{}, nil
	}

	cfg := config.Current()
	assigneeFilter, err := c.routingAssigneeFilter(cfg)
	if err != nil {
		return nil, err
	}

	issueOrder := make(map[string]int, len(ids))
	for index, issueID := range ids {
		issueOrder[issueID] = index
	}

	issues := make([]domain.Issue, 0, len(ids))
	for start := 0; start < len(ids); start += issuePageSize {
		end := start + issuePageSize
		if end > len(ids) {
			end = len(ids)
		}

		batch := ids[start:end]
		body, err := c.graphql(issuesByIDQuery, map[string]any{
			"ids":           batch,
			"first":         len(batch),
			"relationFirst": issuePageSize,
		}, "")
		if err != nil {
			return nil, err
		}

		decoded, err := decodeLinearResponse(body, assigneeFilter)
		if err != nil {
			return nil, err
		}
		issues = append(issues, decoded...)
	}

	sort.SliceStable(issues, func(i, j int) bool {
		left := issueOrder[issues[i].ID]
		right := issueOrder[issues[j].ID]
		return left < right
	})
	return issues, nil
}

// CreateComment creates a Linear comment for one issue.
func (c *Client) CreateComment(issueID, body string) error {
	response, err := c.graphql(createCommentMutation, map[string]any{
		"issueId": issueID,
		"body":    body,
	}, "")
	if err != nil {
		return err
	}
	if success, ok := nestedBool(response, "data", "commentCreate", "success"); ok && success {
		return nil
	}
	return ErrCommentCreateFailed
}

// UpdateIssueState resolves the target state ID and updates the issue state in Linear.
func (c *Client) UpdateIssueState(issueID, stateName string) error {
	stateID, err := c.resolveStateID(issueID, stateName)
	if err != nil {
		return err
	}

	response, err := c.graphql(updateIssueStateMutation, map[string]any{
		"issueId": issueID,
		"stateId": stateID,
	}, "")
	if err != nil {
		return err
	}
	if success, ok := nestedBool(response, "data", "issueUpdate", "success"); ok && success {
		return nil
	}
	return ErrIssueUpdateFailed
}

func (c *Client) fetchByStates(projectSlug string, stateNames []string, assigneeFilter *assigneeFilter) ([]domain.Issue, error) {
	var acc []domain.Issue
	var after any

	for {
		body, err := c.graphql(pollQuery, map[string]any{
			"projectSlug":   projectSlug,
			"stateNames":    stateNames,
			"first":         issuePageSize,
			"relationFirst": issuePageSize,
			"after":         after,
		}, "")
		if err != nil {
			return nil, err
		}

		issues, pageInfo, err := decodeLinearPageResponse(body, assigneeFilter)
		if err != nil {
			return nil, err
		}

		acc = append(acc, issues...)
		nextCursor, done, err := nextPageCursor(pageInfo)
		if err != nil {
			return nil, err
		}
		if done {
			return acc, nil
		}
		after = nextCursor
	}
}

func (c *Client) graphql(query string, variables map[string]any, operationName string) (map[string]any, error) {
	cfg := config.Current()
	if cfg.LinearAPIToken == "" {
		return nil, config.ErrMissingLinearAPIToken
	}

	headers := map[string]string{
		"Authorization": cfg.LinearAPIToken,
		"Content-Type":  "application/json",
	}

	request := c.Request
	if request == nil {
		request = defaultRequest
	}

	response, err := request(cfg.LinearEndpoint, buildPayload(query, variables, operationName), headers)
	if err != nil {
		return nil, &RequestError{Reason: err}
	}
	if response.Status != http.StatusOK {
		return nil, &StatusError{Status: response.Status}
	}

	return response.Body, nil
}

func buildPayload(query string, variables map[string]any, operationName string) map[string]any {
	payload := map[string]any{
		"query":     query,
		"variables": variables,
	}
	if trimmed := strings.TrimSpace(operationName); trimmed != "" {
		payload["operationName"] = trimmed
	}
	return payload
}

func (c *Client) resolveStateID(issueID, stateName string) (string, error) {
	response, err := c.graphql(resolveStateIDQuery, map[string]any{
		"issueId":   issueID,
		"stateName": stateName,
	}, "")
	if err != nil {
		return "", err
	}

	data, ok := response["data"].(map[string]any)
	if !ok {
		return "", ErrStateNotFound
	}
	issue, ok := data["issue"].(map[string]any)
	if !ok {
		return "", ErrStateNotFound
	}
	team, ok := issue["team"].(map[string]any)
	if !ok {
		return "", ErrStateNotFound
	}
	states, ok := team["states"].(map[string]any)
	if !ok {
		return "", ErrStateNotFound
	}
	nodes, ok := states["nodes"].([]any)
	if !ok || len(nodes) == 0 {
		return "", ErrStateNotFound
	}
	first, ok := nodes[0].(map[string]any)
	if !ok {
		return "", ErrStateNotFound
	}
	stateID := stringValue(first["id"])
	if stateID == "" {
		return "", ErrStateNotFound
	}
	return stateID, nil
}

type assigneeFilter struct {
	matchValues map[string]struct{}
}

func (c *Client) routingAssigneeFilter(cfg config.Effective) (*assigneeFilter, error) {
	if strings.TrimSpace(cfg.LinearAssignee) == "" {
		return nil, nil
	}
	return c.buildAssigneeFilter(cfg.LinearAssignee)
}

func (c *Client) buildAssigneeFilter(assignee string) (*assigneeFilter, error) {
	normalized := normalizeAssigneeMatchValue(assignee)
	if normalized == "" {
		return nil, nil
	}

	if normalized == "me" {
		body, err := c.graphql(viewerQuery, map[string]any{}, "")
		if err != nil {
			return nil, err
		}
		data, ok := body["data"].(map[string]any)
		if !ok {
			return nil, ErrMissingViewerIdentity
		}
		viewer, ok := data["viewer"].(map[string]any)
		if !ok {
			return nil, ErrMissingViewerIdentity
		}
		viewerID := normalizeAssigneeMatchValue(stringValue(viewer["id"]))
		if viewerID == "" {
			return nil, ErrMissingViewerIdentity
		}
		return &assigneeFilter{matchValues: map[string]struct{}{viewerID: {}}}, nil
	}

	return &assigneeFilter{matchValues: map[string]struct{}{normalized: {}}}, nil
}

func decodeLinearResponse(body map[string]any, assigneeFilter *assigneeFilter) ([]domain.Issue, error) {
	if errorsPayload, ok := body["errors"].([]any); ok {
		return nil, &GraphQLErrors{Errors: errorsPayload}
	}

	data, ok := body["data"].(map[string]any)
	if !ok {
		return nil, ErrUnknownPayload
	}
	issuesRoot, ok := data["issues"].(map[string]any)
	if !ok {
		return nil, ErrUnknownPayload
	}
	nodes, ok := issuesRoot["nodes"].([]any)
	if !ok {
		return nil, ErrUnknownPayload
	}

	issues := make([]domain.Issue, 0, len(nodes))
	for _, raw := range nodes {
		issue, ok := normalizeIssue(raw, assigneeFilter)
		if ok {
			issues = append(issues, issue)
		}
	}

	return issues, nil
}

type pageInfo struct {
	HasNextPage bool
	EndCursor   string
}

func decodeLinearPageResponse(body map[string]any, assigneeFilter *assigneeFilter) ([]domain.Issue, pageInfo, error) {
	if errorsPayload, ok := body["errors"].([]any); ok {
		return nil, pageInfo{}, &GraphQLErrors{Errors: errorsPayload}
	}

	data, ok := body["data"].(map[string]any)
	if !ok {
		return nil, pageInfo{}, ErrUnknownPayload
	}
	issuesRoot, ok := data["issues"].(map[string]any)
	if !ok {
		return nil, pageInfo{}, ErrUnknownPayload
	}
	nodes, ok := issuesRoot["nodes"].([]any)
	if !ok {
		return nil, pageInfo{}, ErrUnknownPayload
	}
	rawPageInfo, ok := issuesRoot["pageInfo"].(map[string]any)
	if !ok {
		return nil, pageInfo{}, ErrUnknownPayload
	}

	issues, err := decodeLinearResponse(map[string]any{
		"data": map[string]any{
			"issues": map[string]any{
				"nodes": nodes,
			},
		},
	}, assigneeFilter)
	if err != nil {
		return nil, pageInfo{}, err
	}

	return issues, pageInfo{
		HasNextPage: boolValue(rawPageInfo["hasNextPage"]),
		EndCursor:   stringValue(rawPageInfo["endCursor"]),
	}, nil
}

func nextPageCursor(info pageInfo) (string, bool, error) {
	if info.HasNextPage {
		if strings.TrimSpace(info.EndCursor) == "" {
			return "", false, ErrMissingEndCursor
		}
		return info.EndCursor, false, nil
	}
	return "", true, nil
}

func normalizeIssue(raw any, assigneeFilter *assigneeFilter) (domain.Issue, bool) {
	issue, ok := raw.(map[string]any)
	if !ok {
		return domain.Issue{}, false
	}

	normalized := domain.Issue{
		ID:               stringValue(issue["id"]),
		Identifier:       stringValue(issue["identifier"]),
		Title:            stringValue(issue["title"]),
		Description:      stringValue(issue["description"]),
		Priority:         parsePriority(issue["priority"]),
		State:            nestedString(issue, "state", "name"),
		BranchName:       stringValue(issue["branchName"]),
		URL:              stringValue(issue["url"]),
		AssigneeID:       nestedString(issue, "assignee", "id"),
		BlockedBy:        extractBlockers(issue),
		Labels:           extractLabels(issue),
		AssignedToWorker: assignedToWorker(issue["assignee"], assigneeFilter),
		CreatedAt:        parseTime(issue["createdAt"]),
		UpdatedAt:        parseTime(issue["updatedAt"]),
	}

	return normalized, true
}

func assignedToWorker(rawAssignee any, assigneeFilter *assigneeFilter) bool {
	if assigneeFilter == nil {
		return true
	}
	assignee, ok := rawAssignee.(map[string]any)
	if !ok {
		return false
	}
	assigneeID := normalizeAssigneeMatchValue(stringValue(assignee["id"]))
	if assigneeID == "" {
		return false
	}
	_, ok = assigneeFilter.matchValues[assigneeID]
	return ok
}

func extractLabels(issue map[string]any) []string {
	labelsRoot, ok := issue["labels"].(map[string]any)
	if !ok {
		return []string{}
	}
	nodes, ok := labelsRoot["nodes"].([]any)
	if !ok {
		return []string{}
	}

	labels := make([]string, 0, len(nodes))
	for _, raw := range nodes {
		node, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(stringValue(node["name"])))
		if name != "" {
			labels = append(labels, name)
		}
	}
	return labels
}

func extractBlockers(issue map[string]any) []domain.BlockerRef {
	relationsRoot, ok := issue["inverseRelations"].(map[string]any)
	if !ok {
		return []domain.BlockerRef{}
	}
	nodes, ok := relationsRoot["nodes"].([]any)
	if !ok {
		return []domain.BlockerRef{}
	}

	blockers := make([]domain.BlockerRef, 0, len(nodes))
	for _, raw := range nodes {
		node, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if normalizeState(stringValue(node["type"])) != "blocks" {
			continue
		}
		blockerIssue, ok := node["issue"].(map[string]any)
		if !ok {
			continue
		}
		blockers = append(blockers, domain.BlockerRef{
			ID:         stringValue(blockerIssue["id"]),
			Identifier: stringValue(blockerIssue["identifier"]),
			State:      nestedString(blockerIssue, "state", "name"),
		})
	}

	return blockers
}

func parsePriority(value any) *int {
	switch typed := value.(type) {
	case int:
		return intPointer(typed)
	case int64:
		return intPointer(int(typed))
	case float64:
		return intPointer(int(typed))
	default:
		return nil
	}
}

func parseTime(value any) *time.Time {
	raw := strings.TrimSpace(stringValue(value))
	if raw == "" {
		return nil
	}

	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil
	}
	return &parsed
}

func nestedString(root map[string]any, path ...string) string {
	current := any(root)
	for _, segment := range path {
		node, ok := current.(map[string]any)
		if !ok {
			return ""
		}
		current, ok = node[segment]
		if !ok {
			return ""
		}
	}
	return stringValue(current)
}

func normalizeAssigneeMatchValue(value string) string {
	return strings.TrimSpace(value)
}

func normalizeState(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func stringValue(value any) string {
	if raw, ok := value.(string); ok {
		return raw
	}
	return ""
}

func boolValue(value any) bool {
	raw, ok := value.(bool)
	return ok && raw
}

func nestedBool(root map[string]any, path ...string) (bool, bool) {
	current := any(root)
	for _, segment := range path {
		node, ok := current.(map[string]any)
		if !ok {
			return false, false
		}
		current, ok = node[segment]
		if !ok {
			return false, false
		}
	}
	value, ok := current.(bool)
	return value, ok
}

func intPointer(value int) *int {
	result := value
	return &result
}

func uniqueStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		stringValue := fmt.Sprint(value)
		if _, ok := seen[stringValue]; ok {
			continue
		}
		seen[stringValue] = struct{}{}
		result = append(result, stringValue)
	}
	return result
}

func defaultRequest(endpoint string, payload map[string]any, headers map[string]string) (Response, error) {
	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return Response{}, err
	}

	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return Response{}, err
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Response{}, err
	}
	defer resp.Body.Close()

	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return Response{}, err
	}

	var decoded map[string]any
	if len(responseBody) > 0 {
		if err := json.Unmarshal(responseBody, &decoded); err != nil {
			return Response{}, err
		}
	} else {
		decoded = map[string]any{}
	}

	return Response{
		Status: resp.StatusCode,
		Body:   decoded,
	}, nil
}
