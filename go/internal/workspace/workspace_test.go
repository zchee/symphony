package workspace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openai/symphony/go/internal/runtimeconfig"
)

func TestCreateForIssueBootstrapAfterCreateHook(t *testing.T) {
	testRoot := t.TempDir()
	templateRepo := filepath.Join(testRoot, "source")
	workspaceRoot := filepath.Join(testRoot, "workspaces")

	if err := os.MkdirAll(filepath.Join(templateRepo, "keep"), 0o755); err != nil {
		t.Fatalf("os.MkdirAll(template repo) failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(templateRepo, "keep", "file.txt"), []byte("keep me"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(keep/file.txt) failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(templateRepo, "README.md"), []byte("hook clone\n"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(README.md) failed: %v", err)
	}

	writeWorkspaceWorkflow(t, workspaceRoot, map[string]any{
		"hooks": map[string]any{
			"after_create": "cp " + shellQuote(filepath.Join(templateRepo, "README.md")) + " README.md\n" +
				"mkdir -p keep\n" +
				"cp " + shellQuote(filepath.Join(templateRepo, "keep", "file.txt")) + " keep/file.txt",
		},
	})

	workspace, err := CreateForIssue("S-1")
	if err != nil {
		t.Fatalf("CreateForIssue() returned error: %v", err)
	}

	if _, err := os.Stat(filepath.Join(workspace, "README.md")); err != nil {
		t.Fatalf("workspace README missing: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(workspace, "keep", "file.txt")); err != nil || string(got) != "keep me" {
		t.Fatalf("workspace copied keep/file.txt = %q, %v, want %q", string(got), err, "keep me")
	}
}

func TestCreateForIssuePathIsDeterministic(t *testing.T) {
	workspaceRoot := filepath.Join(t.TempDir(), "workspaces")
	writeWorkspaceWorkflow(t, workspaceRoot, nil)

	first, err := CreateForIssue("MT/Det")
	if err != nil {
		t.Fatalf("CreateForIssue(first) returned error: %v", err)
	}
	second, err := CreateForIssue("MT/Det")
	if err != nil {
		t.Fatalf("CreateForIssue(second) returned error: %v", err)
	}

	if first != second {
		t.Fatalf("workspace paths differ: %q != %q", first, second)
	}
	if got, want := filepath.Base(first), "MT_Det"; got != want {
		t.Fatalf("filepath.Base(first) = %q, want %q", got, want)
	}
}

func TestCreateForIssueReusesExistingDirectoryWithoutDeletingLocalChanges(t *testing.T) {
	workspaceRoot := filepath.Join(t.TempDir(), "workspaces")
	writeWorkspaceWorkflow(t, workspaceRoot, map[string]any{
		"hooks": map[string]any{
			"after_create": "echo first > README.md",
		},
	})

	first, err := CreateForIssue("MT-REUSE")
	if err != nil {
		t.Fatalf("CreateForIssue(first) returned error: %v", err)
	}

	mustWriteFile(t, filepath.Join(first, "README.md"), "changed\n")
	mustWriteFile(t, filepath.Join(first, "local-progress.txt"), "in progress\n")
	mustMkdirAll(t, filepath.Join(first, "deps"))
	mustMkdirAll(t, filepath.Join(first, "_build"))
	mustMkdirAll(t, filepath.Join(first, "tmp"))
	mustWriteFile(t, filepath.Join(first, "deps", "cache.txt"), "cached deps\n")
	mustWriteFile(t, filepath.Join(first, "_build", "artifact.txt"), "compiled artifact\n")
	mustWriteFile(t, filepath.Join(first, "tmp", "scratch.txt"), "remove me\n")

	second, err := CreateForIssue("MT-REUSE")
	if err != nil {
		t.Fatalf("CreateForIssue(second) returned error: %v", err)
	}

	if second != first {
		t.Fatalf("workspace paths differ: %q != %q", second, first)
	}
	assertFileContents(t, filepath.Join(second, "README.md"), "changed\n")
	assertFileContents(t, filepath.Join(second, "local-progress.txt"), "in progress\n")
	assertFileContents(t, filepath.Join(second, "deps", "cache.txt"), "cached deps\n")
	assertFileContents(t, filepath.Join(second, "_build", "artifact.txt"), "compiled artifact\n")
	if _, err := os.Stat(filepath.Join(second, "tmp", "scratch.txt")); !os.IsNotExist(err) {
		t.Fatalf("tmp artifact still exists, err=%v", err)
	}
}

func TestCreateForIssueReplacesStaleNonDirectoryPaths(t *testing.T) {
	workspaceRoot := filepath.Join(t.TempDir(), "workspaces")
	staleWorkspace := filepath.Join(workspaceRoot, "MT-STALE")
	mustMkdirAll(t, workspaceRoot)
	mustWriteFile(t, staleWorkspace, "old state\n")
	writeWorkspaceWorkflow(t, workspaceRoot, nil)

	workspace, err := CreateForIssue("MT-STALE")
	if err != nil {
		t.Fatalf("CreateForIssue() returned error: %v", err)
	}
	if workspace != staleWorkspace {
		t.Fatalf("workspace = %q, want %q", workspace, staleWorkspace)
	}
	info, err := os.Stat(workspace)
	if err != nil {
		t.Fatalf("os.Stat(workspace) failed: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("workspace is not a directory")
	}
}

func TestCreateForIssueRejectsSymlinkEscapes(t *testing.T) {
	testRoot := t.TempDir()
	workspaceRoot := filepath.Join(testRoot, "workspaces")
	outsideRoot := filepath.Join(testRoot, "outside")
	symlinkPath := filepath.Join(workspaceRoot, "MT-SYM")
	mustMkdirAll(t, workspaceRoot)
	mustMkdirAll(t, outsideRoot)
	if err := os.Symlink(outsideRoot, symlinkPath); err != nil {
		t.Fatalf("os.Symlink() failed: %v", err)
	}
	writeWorkspaceWorkflow(t, workspaceRoot, nil)

	_, err := CreateForIssue("MT-SYM")
	var symlinkErr *WorkspaceSymlinkEscapeError
	if !errors.As(err, &symlinkErr) {
		t.Fatalf("CreateForIssue() error = %v, want WorkspaceSymlinkEscapeError", err)
	}
	if symlinkErr.Path != symlinkPath {
		t.Fatalf("symlink path = %q, want %q", symlinkErr.Path, symlinkPath)
	}
}

func TestRemoveRejectsWorkspaceRootItself(t *testing.T) {
	workspaceRoot := filepath.Join(t.TempDir(), "workspaces")
	mustMkdirAll(t, workspaceRoot)
	writeWorkspaceWorkflow(t, workspaceRoot, nil)

	err := Remove(workspaceRoot)
	var rootErr *WorkspaceEqualsRootError
	if !errors.As(err, &rootErr) {
		t.Fatalf("Remove() error = %v, want WorkspaceEqualsRootError", err)
	}
}

func TestCreateForIssueSurfacesAfterCreateHookFailures(t *testing.T) {
	workspaceRoot := filepath.Join(t.TempDir(), "workspaces")
	writeWorkspaceWorkflow(t, workspaceRoot, map[string]any{
		"hooks": map[string]any{
			"after_create": "echo nope && exit 17",
		},
	})

	_, err := CreateForIssue("MT-FAIL")
	var hookErr *WorkspaceHookFailedError
	if !errors.As(err, &hookErr) {
		t.Fatalf("CreateForIssue() error = %v, want WorkspaceHookFailedError", err)
	}
	if hookErr.Hook != "after_create" || hookErr.Status != 17 {
		t.Fatalf("hook failure = %#v, want after_create status 17", hookErr)
	}
}

func TestCreateForIssueSurfacesAfterCreateHookTimeouts(t *testing.T) {
	workspaceRoot := filepath.Join(t.TempDir(), "workspaces")
	writeWorkspaceWorkflow(t, workspaceRoot, map[string]any{
		"hooks": map[string]any{
			"timeout_ms":   10,
			"after_create": "sleep 1",
		},
	})

	_, err := CreateForIssue("MT-TIMEOUT")
	var timeoutErr *WorkspaceHookTimeoutError
	if !errors.As(err, &timeoutErr) {
		t.Fatalf("CreateForIssue() error = %v, want WorkspaceHookTimeoutError", err)
	}
	if timeoutErr.Hook != "after_create" || timeoutErr.TimeoutMS != 10 {
		t.Fatalf("timeout error = %#v, want after_create timeout 10", timeoutErr)
	}
}

func TestCreateForIssueCreatesEmptyDirectoryWithoutBootstrapHook(t *testing.T) {
	workspaceRoot := filepath.Join(t.TempDir(), "workspaces")
	writeWorkspaceWorkflow(t, workspaceRoot, nil)

	workspace, err := CreateForIssue("MT-608")
	if err != nil {
		t.Fatalf("CreateForIssue() returned error: %v", err)
	}

	entries, err := os.ReadDir(workspace)
	if err != nil {
		t.Fatalf("os.ReadDir(workspace) failed: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("len(entries) = %d, want 0", len(entries))
	}
}

func TestRemoveIssueWorkspacesRemovesOnlyTheTargetIdentifier(t *testing.T) {
	workspaceRoot := filepath.Join(t.TempDir(), "workspaces")
	targetWorkspace := filepath.Join(workspaceRoot, "S_1")
	untouchedWorkspace := filepath.Join(workspaceRoot, "OTHER")
	mustMkdirAll(t, targetWorkspace)
	mustMkdirAll(t, untouchedWorkspace)
	mustWriteFile(t, filepath.Join(targetWorkspace, "marker.txt"), "stale")
	mustWriteFile(t, filepath.Join(untouchedWorkspace, "marker.txt"), "keep")
	writeWorkspaceWorkflow(t, workspaceRoot, nil)

	if err := RemoveIssueWorkspaces("S_1"); err != nil {
		t.Fatalf("RemoveIssueWorkspaces() returned error: %v", err)
	}
	if _, err := os.Stat(targetWorkspace); !os.IsNotExist(err) {
		t.Fatalf("target workspace still exists, err=%v", err)
	}
	if _, err := os.Stat(untouchedWorkspace); err != nil {
		t.Fatalf("untouched workspace missing: %v", err)
	}
}

func TestRemoveIssueWorkspacesHandlesMissingRootAndNonStringIdentifier(t *testing.T) {
	workspaceRoot := filepath.Join(t.TempDir(), "missing-workspaces")
	writeWorkspaceWorkflow(t, workspaceRoot, nil)

	if err := RemoveIssueWorkspaces("S-2"); err != nil {
		t.Fatalf("RemoveIssueWorkspaces(missing root) returned error: %v", err)
	}
	if err := RemoveIssueWorkspaces(nil); err != nil {
		t.Fatalf("RemoveIssueWorkspaces(nil) returned error: %v", err)
	}
}

func TestRemoveContinuesWhenBeforeRemoveHookFailsOrTimesOut(t *testing.T) {
	workspaceRoot := filepath.Join(t.TempDir(), "workspaces")
	writeWorkspaceWorkflow(t, workspaceRoot, map[string]any{
		"hooks": map[string]any{
			"before_remove": "echo failure && exit 17",
		},
	})

	workspace, err := CreateForIssue("MT-HOOKS-FAIL")
	if err != nil {
		t.Fatalf("CreateForIssue() returned error: %v", err)
	}
	if err := RemoveIssueWorkspaces("MT-HOOKS-FAIL"); err != nil {
		t.Fatalf("RemoveIssueWorkspaces() returned error: %v", err)
	}
	if _, err := os.Stat(workspace); !os.IsNotExist(err) {
		t.Fatalf("workspace still exists after failed before_remove hook, err=%v", err)
	}

	writeWorkspaceWorkflow(t, workspaceRoot, map[string]any{
		"hooks": map[string]any{
			"timeout_ms":    10,
			"before_remove": "sleep 1",
		},
	})

	workspace, err = CreateForIssue("MT-HOOKS-TIMEOUT")
	if err != nil {
		t.Fatalf("CreateForIssue(timeout) returned error: %v", err)
	}
	if err := RemoveIssueWorkspaces("MT-HOOKS-TIMEOUT"); err != nil {
		t.Fatalf("RemoveIssueWorkspaces(timeout) returned error: %v", err)
	}
	if _, err := os.Stat(workspace); !os.IsNotExist(err) {
		t.Fatalf("workspace still exists after timed out before_remove hook, err=%v", err)
	}
}

func writeWorkspaceWorkflow(t *testing.T, workspaceRoot string, overrides map[string]any) {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "WORKFLOW.md")
	content := `---
tracker:
  kind: linear
  api_key: "token"
  project_slug: "project"
workspace:
  root: "` + workspaceRoot + `"
---`

	if overrides != nil {
		if hooks, ok := overrides["hooks"].(map[string]any); ok {
			lines := []string{
				"---",
				"tracker:",
				`  kind: "linear"`,
				`  api_key: "token"`,
				`  project_slug: "project"`,
				"workspace:",
				`  root: "` + workspaceRoot + `"`,
				"hooks:",
			}
			if timeout, ok := hooks["timeout_ms"]; ok {
				lines = append(lines, fmt.Sprintf("  timeout_ms: %v", timeout))
			}
			if afterCreate, ok := hooks["after_create"].(string); ok {
				lines = append(lines, "  after_create: |", indentHook(afterCreate))
			}
			if beforeRemove, ok := hooks["before_remove"].(string); ok {
				lines = append(lines, "  before_remove: |", indentHook(beforeRemove))
			}
			content = strings.Join(lines, "\n") + "\n---\n"
		}
	}

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("os.WriteFile(%q) failed: %v", path, err)
	}
	if err := runtimeconfig.SetWorkflowFilePath(path); err != nil {
		t.Fatalf("runtimeconfig.SetWorkflowFilePath(%q) failed: %v", path, err)
	}
}

func indentHook(command string) string {
	parts := strings.Split(command, "\n")
	for index := range parts {
		parts[index] = "    " + parts[index]
	}
	return strings.Join(parts, "\n")
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("os.WriteFile(%q) failed: %v", path, err)
	}
}

func mustMkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("os.MkdirAll(%q) failed: %v", path, err)
	}
}

func assertFileContents(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("os.ReadFile(%q) failed: %v", path, err)
	}
	if string(got) != want {
		t.Fatalf("file %q = %q, want %q", path, string(got), want)
	}
}
