package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/openai/symphony/go/internal/config"
)

var excludedEntries = map[string]struct{}{
	".elixir_ls": {},
	"tmp":        {},
}

// WorkspaceEqualsRootError reports attempts to operate on the workspace root itself.
type WorkspaceEqualsRootError struct {
	Workspace string
	Root      string
}

func (e *WorkspaceEqualsRootError) Error() string {
	return fmt.Sprintf("workspace_equals_root workspace=%s root=%s", e.Workspace, e.Root)
}

// WorkspaceOutsideRootError reports paths outside the configured workspace root.
type WorkspaceOutsideRootError struct {
	Workspace string
	Root      string
}

func (e *WorkspaceOutsideRootError) Error() string {
	return fmt.Sprintf("workspace_outside_root workspace=%s root=%s", e.Workspace, e.Root)
}

// WorkspaceSymlinkEscapeError reports symlink components under the workspace root.
type WorkspaceSymlinkEscapeError struct {
	Path string
	Root string
}

func (e *WorkspaceSymlinkEscapeError) Error() string {
	return fmt.Sprintf("workspace_symlink_escape path=%s root=%s", e.Path, e.Root)
}

// WorkspacePathUnreadableError reports unreadable workspace path components.
type WorkspacePathUnreadableError struct {
	Path   string
	Reason error
}

func (e *WorkspacePathUnreadableError) Error() string {
	return fmt.Sprintf("workspace_path_unreadable path=%s: %v", e.Path, e.Reason)
}

// WorkspaceHookFailedError reports a non-zero hook command exit.
type WorkspaceHookFailedError struct {
	Hook   string
	Status int
	Output string
}

func (e *WorkspaceHookFailedError) Error() string {
	return fmt.Sprintf("workspace_hook_failed hook=%s status=%d", e.Hook, e.Status)
}

// WorkspaceHookTimeoutError reports a timed-out hook.
type WorkspaceHookTimeoutError struct {
	Hook      string
	TimeoutMS int
}

func (e *WorkspaceHookTimeoutError) Error() string {
	return fmt.Sprintf("workspace_hook_timeout hook=%s timeout_ms=%d", e.Hook, e.TimeoutMS)
}

// CreateForIssue ensures a deterministic workspace exists for one issue identifier.
func CreateForIssue(issueOrIdentifier any) (string, error) {
	safeID := SafeIdentifier(issueIdentifier(issueOrIdentifier))
	workspace := PathForIssue(safeID)

	if err := validateWorkspacePath(workspace); err != nil {
		return "", err
	}

	created, err := ensureWorkspace(workspace)
	if err != nil {
		return "", err
	}

	if created {
		if command := config.Current().Hooks.AfterCreate; strings.TrimSpace(command) != "" {
			if err := runHook("after_create", command, workspace, config.Current().Hooks.TimeoutMS); err != nil {
				return "", err
			}
		}
	}

	return workspace, nil
}

// Remove deletes a validated workspace path.
func Remove(workspace string) error {
	if _, err := os.Stat(workspace); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
	}

	if err := validateWorkspacePath(workspace); err != nil {
		return err
	}

	if info, err := os.Stat(workspace); err == nil && info.IsDir() {
		if command := config.Current().Hooks.BeforeRemove; strings.TrimSpace(command) != "" {
			_ = runHook("before_remove", command, workspace, config.Current().Hooks.TimeoutMS)
		}
	}

	return os.RemoveAll(workspace)
}

// RemoveIssueWorkspaces removes the workspace associated with one issue identifier.
func RemoveIssueWorkspaces(identifier any) error {
	raw, ok := identifier.(string)
	if !ok || raw == "" {
		return nil
	}

	return Remove(PathForIssue(SafeIdentifier(raw)))
}

// RunBeforeRunHook executes the before-run hook when configured.
func RunBeforeRunHook(workspace string) error {
	command := config.Current().Hooks.BeforeRun
	if strings.TrimSpace(command) == "" {
		return nil
	}

	return runHook("before_run", command, workspace, config.Current().Hooks.TimeoutMS)
}

// RunAfterRunHook executes the after-run hook when configured and ignores failures.
func RunAfterRunHook(workspace string) {
	command := config.Current().Hooks.AfterRun
	if strings.TrimSpace(command) == "" {
		return
	}

	_ = runHook("after_run", command, workspace, config.Current().Hooks.TimeoutMS)
}

// PathForIssue returns the deterministic workspace path for a sanitized issue identifier.
func PathForIssue(safeIdentifier string) string {
	return filepath.Join(config.Current().WorkspaceRoot, safeIdentifier)
}

// SafeIdentifier applies the Elixir allowlist replacement rule to issue identifiers.
func SafeIdentifier(identifier string) string {
	if identifier == "" {
		identifier = "issue"
	}

	var b strings.Builder
	for _, r := range identifier {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '.' || r == '_' || r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}

	return b.String()
}

func ensureWorkspace(workspace string) (bool, error) {
	info, err := os.Lstat(workspace)
	switch {
	case err == nil && info.IsDir():
		cleanTmpArtifacts(workspace)
		return false, nil
	case err == nil:
		if err := os.RemoveAll(workspace); err != nil {
			return false, err
		}
	case err != nil && !os.IsNotExist(err):
		return false, err
	}

	if err := os.RemoveAll(workspace); err != nil {
		return false, err
	}
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		return false, err
	}

	return true, nil
}

func cleanTmpArtifacts(workspace string) {
	for entry := range excludedEntries {
		_ = os.RemoveAll(filepath.Join(workspace, entry))
	}
}

func validateWorkspacePath(workspace string) error {
	expandedWorkspace, err := filepath.Abs(workspace)
	if err != nil {
		return err
	}
	root, err := filepath.Abs(config.Current().WorkspaceRoot)
	if err != nil {
		return err
	}

	if expandedWorkspace == root {
		return &WorkspaceEqualsRootError{Workspace: expandedWorkspace, Root: root}
	}

	relative, err := filepath.Rel(root, expandedWorkspace)
	if err != nil {
		return err
	}
	if relative == "." || relative == "" || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return &WorkspaceOutsideRootError{Workspace: expandedWorkspace, Root: root}
	}

	return ensureNoSymlinkComponents(expandedWorkspace, root)
}

func ensureNoSymlinkComponents(workspace, root string) error {
	relative, err := filepath.Rel(root, workspace)
	if err != nil {
		return err
	}

	current := root
	for _, segment := range strings.Split(relative, string(os.PathSeparator)) {
		if segment == "." || segment == "" {
			continue
		}

		current = filepath.Join(current, segment)
		info, err := os.Lstat(current)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return &WorkspacePathUnreadableError{Path: current, Reason: err}
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return &WorkspaceSymlinkEscapeError{Path: current, Root: root}
		}
	}

	return nil
}

func runHook(name, command, workspace string, timeoutMS int) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutMS)*time.Millisecond)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-lc", command)
	cmd.Dir = workspace
	output, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return &WorkspaceHookTimeoutError{Hook: name, TimeoutMS: timeoutMS}
	}
	if err != nil {
		exitCode := 1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		}
		return &WorkspaceHookFailedError{
			Hook:   name,
			Status: exitCode,
			Output: string(output),
		}
	}

	return nil
}

func issueIdentifier(issueOrIdentifier any) string {
	switch typed := issueOrIdentifier.(type) {
	case string:
		return typed
	case interface{ GetIdentifier() string }:
		return typed.GetIdentifier()
	default:
		return ""
	}
}
