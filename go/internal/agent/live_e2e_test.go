package agent

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openai/symphony/go/internal/domain"
	"github.com/openai/symphony/go/internal/linear"
	"github.com/openai/symphony/go/internal/runtimeconfig"
	"github.com/openai/symphony/go/internal/ssh"
	"github.com/openai/symphony/go/internal/workflow"
)

const (
	liveE2EResultFile = "LIVE_E2E_RESULT.txt"
	defaultTeamKey    = "SYME2E"
)

const teamQuery = `
query SymphonyLiveE2ETeam($key: String!) {
  teams(filter: {key: {eq: $key}}, first: 1) {
    nodes {
      id
      key
      name
      states(first: 50) {
        nodes {
          id
          name
          type
        }
      }
    }
  }
}
`

const createProjectMutation = `
mutation SymphonyLiveE2ECreateProject($name: String!, $teamIds: [String!]!) {
  projectCreate(input: {name: $name, teamIds: $teamIds}) {
    success
    project {
      id
      name
      slugId
      url
    }
  }
}
`

const createIssueMutation = `
mutation SymphonyLiveE2ECreateIssue($teamId: String!, $projectId: String!, $title: String!, $description: String!, $stateId: String) {
  issueCreate(input: {teamId: $teamId, projectId: $projectId, title: $title, description: $description, stateId: $stateId}) {
    success
    issue {
      id
      identifier
      title
      description
      url
      state { name type }
    }
  }
}
`

const projectStatusesQuery = `
query SymphonyLiveE2EProjectStatuses {
  projectStatuses(first: 50) {
    nodes {
      id
      name
      type
    }
  }
}
`

const issueDetailsQuery = `
query SymphonyLiveE2EIssueDetails($id: String!) {
  issue(id: $id) {
    id
    identifier
    state { name type }
    comments(first: 20) {
      nodes { body }
    }
  }
}
`

const completeProjectMutation = `
mutation SymphonyLiveE2ECompleteProject($id: String!, $statusId: String!, $completedAt: DateTime!) {
  projectUpdate(id: $id, input: {statusId: $statusId, completedAt: $completedAt}) {
    success
  }
}
`

func TestLiveE2ELocal(t *testing.T) {
	requireLiveE2E(t)
	runLiveIssueFlow(t, "local")
}

func TestLiveE2ESSH(t *testing.T) {
	requireLiveE2E(t)
	if len(liveSSHWorkerHosts()) == 0 {
		t.Skip("set SYMPHONY_LIVE_SSH_WORKER_HOSTS to enable the SSH live E2E test")
	}
	runLiveIssueFlow(t, "ssh")
}

func requireLiveE2E(t *testing.T) {
	t.Helper()
	if os.Getenv("SYMPHONY_RUN_LIVE_E2E") != "1" {
		t.Skip("set SYMPHONY_RUN_LIVE_E2E=1 to enable the real Linear/Codex end-to-end test")
	}
	if strings.TrimSpace(os.Getenv("LINEAR_API_KEY")) == "" {
		t.Skip("set LINEAR_API_KEY to enable the real Linear/Codex end-to-end test")
	}
	if _, err := exec.LookPath("codex"); err != nil {
		t.Skip("install codex to enable the real Linear/Codex end-to-end test")
	}
}

func runLiveIssueFlow(t *testing.T, backend string) {
	t.Helper()
	runID := fmt.Sprintf("symphony-live-e2e-%s-%d", backend, time.Now().UnixNano())
	testRoot := filepath.Join(os.TempDir(), runID)
	workflowRoot := filepath.Join(testRoot, "workflow")
	workflowFile := filepath.Join(workflowRoot, "WORKFLOW.md")
	client := linear.NewClient()
	teamKey := firstNonEmptyEnv("SYMPHONY_LIVE_LINEAR_TEAM_KEY", defaultTeamKey)
	workerSetup := liveWorkerSetup(t, backend, runID, testRoot)
	originalWorkflowPath := runtimeconfig.WorkflowFilePath()
	workflow.ResetDefaultStoreForTest()
	if err := os.MkdirAll(workflowRoot, 0o755); err != nil {
		t.Fatalf("os.MkdirAll(%q) failed: %v", workflowRoot, err)
	}

	defer func() {
		workflow.ResetDefaultStoreForTest()
		if originalWorkflowPath != "" {
			if err := runtimeconfig.SetWorkflowFilePath(originalWorkflowPath); err != nil {
				t.Fatalf("runtimeconfig.SetWorkflowFilePath(%q) restore failed: %v", originalWorkflowPath, err)
			}
		} else {
			runtimeconfig.ClearWorkflowFilePath()
		}
		workerSetup.cleanup()
		_ = os.RemoveAll(testRoot)
	}()

	if err := runtimeconfig.SetWorkflowFilePath(workflowFile); err != nil {
		t.Fatalf("runtimeconfig.SetWorkflowFilePath(%q) failed: %v", workflowFile, err)
	}

	writeLiveWorkflow(t, workflowFile, workerSetup.workspaceRoot, workerSetup.workerHosts, "bootstrap", workerSetup.codexCommand, "")

	team := fetchTeam(t, client, teamKey)
	activeState := activeState(t, team)
	completedProjectStatus := completedProjectStatus(t, client)
	project := createProject(t, client, teamID(team), fmt.Sprintf("Symphony Live E2E %s %d", backend, time.Now().UnixNano()))
	issue := createIssue(t, client, teamID(team), projectID(project), stateID(activeState), fmt.Sprintf("Symphony live e2e %s issue for %s", backend, projectSlug(project)))

	writeLiveWorkflow(t, workflowFile, workerSetup.workspaceRoot, workerSetup.workerHosts, projectSlug(project), workerSetup.codexCommand, livePrompt(projectSlug(project)))

	var runtimeInfo map[string]any
	if err := Run(issue, Options{
		MaxTurns: 3,
		OnRuntimeInfo: func(info map[string]any) {
			runtimeInfo = info
		},
	}); err != nil {
		t.Fatalf("Run(live issue) returned error: %v", err)
	}

	result := readWorkerResult(t, runtimeInfo, liveE2EResultFile)
	if got, want := result, expectedResult(issue.Identifier, projectSlug(project)); got != want {
		t.Fatalf("worker result = %q, want %q", got, want)
	}

	issueSnapshot := fetchIssueDetails(t, client, issue.ID)
	if !issueCompleted(issueSnapshot) {
		t.Fatalf("issue snapshot = %#v, want completed issue state", issueSnapshot)
	}
	if !issueHasComment(issueSnapshot, expectedComment(issue.Identifier, projectSlug(project))) {
		t.Fatalf("issue comments = %#v, want expected comment body", issueSnapshot)
	}

	completeProject(t, client, projectID(project), projectStatusID(completedProjectStatus))
}

type workerSetup struct {
	workspaceRoot string
	workerHosts   []string
	codexCommand  string
	cleanup       func()
}

func liveWorkerSetup(t *testing.T, backend, runID, testRoot string) workerSetup {
	t.Helper()
	switch backend {
	case "local":
		return workerSetup{
			workspaceRoot: filepath.Join(testRoot, "workspaces"),
			workerHosts:   nil,
			codexCommand:  "codex app-server",
			cleanup:       func() {},
		}
	case "ssh":
		hosts := liveSSHWorkerHosts()
		remoteRoot := filepath.Join(remoteHome(t, hosts[0]), "."+runID)
		return workerSetup{
			workspaceRoot: "~/." + runID + "/workspaces",
			workerHosts:   hosts,
			codexCommand:  "codex app-server",
			cleanup: func() {
				cleanupRemoteRoot(t, remoteRoot, hosts)
			},
		}
	default:
		t.Fatalf("unsupported backend %q", backend)
		return workerSetup{}
	}
}

func liveSSHWorkerHosts() []string {
	raw := strings.TrimSpace(os.Getenv("SYMPHONY_LIVE_SSH_WORKER_HOSTS"))
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	hosts := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			hosts = append(hosts, trimmed)
		}
	}
	return hosts
}

func remoteHome(t *testing.T, workerHost string) string {
	t.Helper()
	output, status, err := ssh.Run(workerHost, "printf '%s\\n' \"$HOME\"")
	if err != nil {
		t.Fatalf("ssh.Run(%q, $HOME) returned error: %v", workerHost, err)
	}
	if status != 0 {
		t.Fatalf("ssh.Run(%q, $HOME) status = %d output=%q", workerHost, status, output)
	}
	home := strings.TrimSpace(output)
	if home == "" {
		t.Fatalf("ssh.Run(%q, $HOME) returned empty home", workerHost)
	}
	return home
}

func cleanupRemoteRoot(t *testing.T, remoteRoot string, workerHosts []string) {
	t.Helper()
	for _, workerHost := range workerHosts {
		_, _, _ = ssh.Run(workerHost, "rm -rf "+shellEscape(remoteRoot))
	}
}

func readWorkerResult(t *testing.T, runtimeInfo map[string]any, resultFile string) string {
	t.Helper()
	workspacePath := stringValue(runtimeInfo["workspace_path"])
	workerHost := stringValue(runtimeInfo["worker_host"])
	if workspacePath == "" {
		t.Fatalf("runtimeInfo = %#v, want workspace_path", runtimeInfo)
	}
	if workerHost == "" {
		data, err := os.ReadFile(filepath.Join(workspacePath, resultFile))
		if err != nil {
			t.Fatalf("os.ReadFile(local result) failed: %v", err)
		}
		return string(data)
	}
	output, status, err := ssh.Run(workerHost, "cat "+shellEscape(filepath.Join(workspacePath, resultFile)))
	if err != nil {
		t.Fatalf("ssh.Run(%q, cat result) returned error: %v", workerHost, err)
	}
	if status != 0 {
		t.Fatalf("ssh.Run(%q, cat result) status = %d output=%q", workerHost, status, output)
	}
	return output
}

func livePrompt(projectSlug string) string {
	return fmt.Sprintf(`You are running a real Symphony end-to-end test.

The current working directory is the workspace root.

Step 1:
Create a file named %s in the current working directory by running exactly this shell sequence:
cat > %s <<'EOF'
identifier={{ issue.identifier }}
project_slug=%s
EOF

Then verify it by running:
cat %s

The file content must be exactly:
identifier={{ issue.identifier }}
project_slug=%s

Step 2:
You must use the linear_graphql tool to query the current issue by {{ issue.id }} and read:
- existing comments
- team workflow states

A turn that only creates the file is incomplete. Do not stop after Step 1.

If the exact comment body below is not already present, post exactly one comment on the current issue with this exact body:
%s

Use these exact GraphQL operations:
query IssueContext($id: String!) {
  issue(id: $id) {
    comments(first: 20) {
      nodes {
        body
      }
    }
    team {
      states(first: 50) {
        nodes {
          id
          name
          type
        }
      }
    }
  }
}

mutation AddComment($issueId: String!, $body: String!) {
  commentCreate(input: {issueId: $issueId, body: $body}) {
    success
  }
}

Step 3:
Use the same issue-context query result to choose a workflow state whose type is completed.
Then move the current issue to that state with this exact mutation:
mutation CompleteIssue($id: String!, $stateId: String!) {
  issueUpdate(id: $id, input: {stateId: $stateId}) {
    success
  }
}

Step 4:
Verify all outcomes with one final linear_graphql query against {{ issue.id }}:
- the exact comment body is present
- the issue state type is completed

Do not ask for approval.
Stop only after all three conditions are true:
1. the file exists with the exact contents above
2. the Linear comment exists with the exact body above
3. the Linear issue is in a completed terminal state
`, liveE2EResultFile, liveE2EResultFile, projectSlug, liveE2EResultFile, projectSlug, expectedComment("{{ issue.identifier }}", projectSlug))
}

func expectedResult(issueIdentifier, projectSlug string) string {
	return fmt.Sprintf("identifier=%s\nproject_slug=%s\n", issueIdentifier, projectSlug)
}

func expectedComment(issueIdentifier, projectSlug string) string {
	return fmt.Sprintf("Symphony live e2e comment\nidentifier=%s\nproject_slug=%s", issueIdentifier, projectSlug)
}

func writeLiveWorkflow(t *testing.T, path, workspaceRoot string, workerHosts []string, projectSlug, codexCommand, prompt string) {
	t.Helper()
	lines := []string{
		"---",
		"tracker:",
		`  kind: "linear"`,
		`  api_key: "$LINEAR_API_KEY"`,
		`  project_slug: "` + projectSlug + `"`,
		"workspace:",
		`  root: "` + workspaceRoot + `"`,
		"agent:",
		"  max_turns: 3",
		"codex:",
		`  command: "` + strings.ReplaceAll(codexCommand, `"`, `\"`) + `"`,
		`  approval_policy: "never"`,
		"  turn_timeout_ms: 600000",
		"  stall_timeout_ms: 600000",
		"observability:",
		"  dashboard_enabled: false",
	}
	if len(workerHosts) > 0 {
		lines = append(lines, "worker:", "  ssh_hosts:")
		for _, host := range workerHosts {
			lines = append(lines, `    - "`+host+`"`)
		}
	}
	if prompt == "" {
		prompt = "Prompt"
	}
	lines = append(lines, "---", prompt)
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(%q) failed: %v", path, err)
	}
}

func fetchTeam(t *testing.T, client *linear.Client, teamKey string) map[string]any {
	t.Helper()
	data := graphqlData(t, client, teamQuery, map[string]any{"key": teamKey})
	nodes, _ := data["teams"].(map[string]any)["nodes"].([]any)
	if len(nodes) == 0 {
		t.Fatalf("expected team %q to exist", teamKey)
	}
	team, _ := nodes[0].(map[string]any)
	return team
}

func activeState(t *testing.T, team map[string]any) map[string]any {
	t.Helper()
	states, _ := team["states"].(map[string]any)["nodes"].([]any)
	for _, raw := range states {
		state, _ := raw.(map[string]any)
		stateType := stringValue(state["type"])
		if stateType == "started" || stateType == "unstarted" || (stateType != "completed" && stateType != "canceled") {
			return state
		}
	}
	t.Fatalf("expected a non-terminal team state, got %#v", states)
	return nil
}

func completedProjectStatus(t *testing.T, client *linear.Client) map[string]any {
	t.Helper()
	data := graphqlData(t, client, projectStatusesQuery, map[string]any{})
	nodes, _ := data["projectStatuses"].(map[string]any)["nodes"].([]any)
	for _, raw := range nodes {
		status, _ := raw.(map[string]any)
		if stringValue(status["type"]) == "completed" {
			return status
		}
	}
	t.Fatalf("expected a completed project status, got %#v", nodes)
	return nil
}

func createProject(t *testing.T, client *linear.Client, teamID, name string) map[string]any {
	t.Helper()
	data := graphqlData(t, client, createProjectMutation, map[string]any{"teamIds": []string{teamID}, "name": name})
	projectCreate, _ := data["projectCreate"].(map[string]any)
	project, _ := projectCreate["project"].(map[string]any)
	if success, _ := projectCreate["success"].(bool); !success || project == nil {
		t.Fatalf("projectCreate failed: %#v", projectCreate)
	}
	return project
}

func createIssue(t *testing.T, client *linear.Client, teamID, projectID, stateID, title string) domain.Issue {
	t.Helper()
	data := graphqlData(t, client, createIssueMutation, map[string]any{
		"teamId":      teamID,
		"projectId":   projectID,
		"title":       title,
		"description": title,
		"stateId":     stateID,
	})
	issueCreate, _ := data["issueCreate"].(map[string]any)
	issue, _ := issueCreate["issue"].(map[string]any)
	if success, _ := issueCreate["success"].(bool); !success || issue == nil {
		t.Fatalf("issueCreate failed: %#v", issueCreate)
	}
	state, _ := issue["state"].(map[string]any)
	return domain.Issue{
		ID:          stringValue(issue["id"]),
		Identifier:  stringValue(issue["identifier"]),
		Title:       stringValue(issue["title"]),
		Description: stringValue(issue["description"]),
		State:       stringValue(state["name"]),
		URL:         stringValue(issue["url"]),
		Labels:      nil,
		BlockedBy:   nil,
	}
}

func fetchIssueDetails(t *testing.T, client *linear.Client, issueID string) map[string]any {
	t.Helper()
	data := graphqlData(t, client, issueDetailsQuery, map[string]any{"id": issueID})
	issue, _ := data["issue"].(map[string]any)
	if issue == nil {
		t.Fatalf("issue details missing for %q", issueID)
	}
	return issue
}

func issueCompleted(issue map[string]any) bool {
	state, _ := issue["state"].(map[string]any)
	stateType := stringValue(state["type"])
	return stateType == "completed" || stateType == "canceled"
}

func issueHasComment(issue map[string]any, expectedBody string) bool {
	comments, _ := issue["comments"].(map[string]any)["nodes"].([]any)
	for _, raw := range comments {
		comment, _ := raw.(map[string]any)
		if stringValue(comment["body"]) == expectedBody {
			return true
		}
	}
	return false
}

func completeProject(t *testing.T, client *linear.Client, projectID, completedStatusID string) {
	t.Helper()
	data := graphqlData(t, client, completeProjectMutation, map[string]any{
		"id":          projectID,
		"statusId":    completedStatusID,
		"completedAt": time.Now().UTC().Format(time.RFC3339),
	})
	projectUpdate, _ := data["projectUpdate"].(map[string]any)
	if success, _ := projectUpdate["success"].(bool); !success {
		t.Fatalf("projectUpdate failed: %#v", projectUpdate)
	}
}

func graphqlData(t *testing.T, client *linear.Client, query string, variables map[string]any) map[string]any {
	t.Helper()
	body, err := client.GraphQL(query, variables, "")
	if err != nil {
		t.Fatalf("GraphQL request failed: %v", err)
	}
	data, _ := body["data"].(map[string]any)
	if data == nil {
		t.Fatalf("GraphQL response missing data: %#v", body)
	}
	return data
}

func teamID(team map[string]any) string            { return stringValue(team["id"]) }
func projectID(project map[string]any) string      { return stringValue(project["id"]) }
func projectSlug(project map[string]any) string    { return stringValue(project["slugId"]) }
func stateID(state map[string]any) string          { return stringValue(state["id"]) }
func projectStatusID(status map[string]any) string { return stringValue(status["id"]) }
func firstNonEmptyEnv(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
func stringValue(value any) string    { raw, _ := value.(string); return raw }
func shellEscape(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }
