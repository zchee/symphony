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

	"github.com/openai/symphony/go/config"
	"github.com/openai/symphony/go/pathsafety"
	"github.com/openai/symphony/go/ssh"
)

const remoteWorkspaceMarker = "__SYMPHONY_WORKSPACE__"

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

// CreateForIssue ensures a deterministic local workspace exists for one issue identifier.
func CreateForIssue(issueOrIdentifier any) (string, error) {
	return createForIssue(issueOrIdentifier, "")
}

// CreateForIssueOnHost ensures a deterministic workspace exists on one configured SSH worker.
func CreateForIssueOnHost(issueOrIdentifier any, workerHost string) (string, error) {
	return createForIssue(issueOrIdentifier, workerHost)
}

func createForIssue(issueOrIdentifier any, workerHost string) (string, error) {
	safeID := SafeIdentifier(issueIdentifier(issueOrIdentifier))

	if workerHost == "" {
		workspace, err := localWorkspacePath(safeID)
		if err != nil {
			return "", err
		}
		if err := validateLocalWorkspacePath(workspace); err != nil {
			return "", err
		}

		created, err := ensureLocalWorkspace(workspace)
		if err != nil {
			return "", err
		}
		if created {
			if command := config.Current().Hooks.AfterCreate; strings.TrimSpace(command) != "" {
				if err := runLocalHook("after_create", command, workspace, config.Current().Hooks.TimeoutMS); err != nil {
					return "", err
				}
			}
		}

		return workspace, nil
	}

	workspace := remoteWorkspacePath(safeID)
	if err := validateRemoteWorkspacePath(workspace, workerHost); err != nil {
		return "", err
	}

	actualWorkspace, created, err := ensureRemoteWorkspace(workspace, workerHost, config.Current().Hooks.TimeoutMS)
	if err != nil {
		return "", err
	}
	if created {
		if command := config.Current().Hooks.AfterCreate; strings.TrimSpace(command) != "" {
			if err := runRemoteHook("after_create", command, actualWorkspace, workerHost, config.Current().Hooks.TimeoutMS); err != nil {
				return "", err
			}
		}
	}

	return actualWorkspace, nil
}

// Remove deletes a validated local workspace path.
func Remove(workspace string) error {
	return remove(workspace, "")
}

// RemoveOnHost deletes a validated workspace path on one SSH worker.
func RemoveOnHost(workspace, workerHost string) error {
	return remove(workspace, workerHost)
}

func remove(workspace, workerHost string) error {
	if workerHost == "" {
		if _, err := os.Stat(workspace); err != nil && !os.IsNotExist(err) {
			return err
		}

		if err := validateLocalWorkspacePath(workspace); err != nil {
			return err
		}

		if info, err := os.Stat(workspace); err == nil && info.IsDir() {
			if command := config.Current().Hooks.BeforeRemove; strings.TrimSpace(command) != "" {
				_ = runLocalHook("before_remove", command, workspace, config.Current().Hooks.TimeoutMS)
			}
		}

		return os.RemoveAll(workspace)
	}

	if command := config.Current().Hooks.BeforeRemove; strings.TrimSpace(command) != "" {
		_ = runRemoteRemoveHook(command, workspace, workerHost, config.Current().Hooks.TimeoutMS)
	}

	output, status, err := runRemoteCommand(workerHost, remoteShellAssign("workspace", workspace)+"\nrm -rf \"$workspace\"", config.Current().Hooks.TimeoutMS)
	if err != nil {
		return err
	}
	if status != 0 {
		return &WorkspaceHookFailedError{Hook: "remove", Status: status, Output: output}
	}
	return nil
}

// RemoveIssueWorkspaces removes the workspace associated with one issue identifier.
func RemoveIssueWorkspaces(identifier any) error {
	raw, ok := identifier.(string)
	if !ok || raw == "" {
		return nil
	}

	if hosts := config.Current().WorkerSSHHosts; len(hosts) > 0 {
		for _, host := range hosts {
			if err := RemoveIssueWorkspacesOnHost(raw, host); err != nil {
				return err
			}
		}
		return nil
	}

	workspace, err := localWorkspacePath(SafeIdentifier(raw))
	if err != nil {
		return err
	}
	return remove(workspace, "")
}

// RemoveIssueWorkspacesOnHost removes the workspace associated with one issue identifier on a specific host.
func RemoveIssueWorkspacesOnHost(identifier any, workerHost string) error {
	raw, ok := identifier.(string)
	if !ok || raw == "" {
		return nil
	}

	return remove(remoteWorkspacePath(SafeIdentifier(raw)), workerHost)
}

// RunBeforeRunHook executes the before-run hook when configured.
func RunBeforeRunHook(workspace string) error {
	return RunBeforeRunHookOnHost(workspace, "")
}

// RunBeforeRunHookOnHost executes the before-run hook on a local or remote workspace.
func RunBeforeRunHookOnHost(workspace, workerHost string) error {
	command := config.Current().Hooks.BeforeRun
	if strings.TrimSpace(command) == "" {
		return nil
	}
	if workerHost == "" {
		return runLocalHook("before_run", command, workspace, config.Current().Hooks.TimeoutMS)
	}
	return runRemoteHook("before_run", command, workspace, workerHost, config.Current().Hooks.TimeoutMS)
}

// RunAfterRunHook executes the after-run hook when configured and ignores failures.
func RunAfterRunHook(workspace string) {
	RunAfterRunHookOnHost(workspace, "")
}

// RunAfterRunHookOnHost executes the after-run hook when configured and ignores failures.
func RunAfterRunHookOnHost(workspace, workerHost string) {
	command := config.Current().Hooks.AfterRun
	if strings.TrimSpace(command) == "" {
		return
	}
	if workerHost == "" {
		_ = runLocalHook("after_run", command, workspace, config.Current().Hooks.TimeoutMS)
		return
	}
	_ = runRemoteHook("after_run", command, workspace, workerHost, config.Current().Hooks.TimeoutMS)
}

// PathForIssue returns the deterministic local workspace path for a sanitized issue identifier.
func PathForIssue(safeIdentifier string) string {
	workspace, err := localWorkspacePath(safeIdentifier)
	if err != nil {
		return filepath.Join(config.LocalWorkspaceRoot(), safeIdentifier)
	}
	return workspace
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

func ensureLocalWorkspace(workspace string) (bool, error) {
	info, err := os.Lstat(workspace)
	switch {
	case err == nil && info.IsDir():
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

func localWorkspacePath(safeIdentifier string) (string, error) {
	return pathsafety.Canonicalize(filepath.Join(config.LocalWorkspaceRoot(), safeIdentifier))
}

func remoteWorkspacePath(safeIdentifier string) string {
	return filepath.Join(config.Current().WorkspaceRoot, safeIdentifier)
}

func validateLocalWorkspacePath(workspace string) error {
	canonicalWorkspace, err := pathsafety.Canonicalize(workspace)
	if err != nil {
		return &WorkspacePathUnreadableError{Path: workspace, Reason: err}
	}

	canonicalRoot, err := pathsafety.Canonicalize(config.LocalWorkspaceRoot())
	if err != nil {
		return &WorkspacePathUnreadableError{Path: config.LocalWorkspaceRoot(), Reason: err}
	}

	if canonicalWorkspace == canonicalRoot {
		return &WorkspaceEqualsRootError{Workspace: canonicalWorkspace, Root: canonicalRoot}
	}

	relative, err := filepath.Rel(canonicalRoot, canonicalWorkspace)
	if err != nil {
		return err
	}
	if relative == "." || relative == "" || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return &WorkspaceOutsideRootError{Workspace: canonicalWorkspace, Root: canonicalRoot}
	}

	return nil
}

func validateRemoteWorkspacePath(workspace, _ string) error {
	switch {
	case strings.TrimSpace(workspace) == "":
		return &WorkspacePathUnreadableError{Path: workspace, Reason: errors.New("empty")}
	case strings.Contains(workspace, "\n"), strings.Contains(workspace, "\r"), strings.ContainsRune(workspace, rune(0)):
		return &WorkspacePathUnreadableError{Path: workspace, Reason: errors.New("invalid_characters")}
	default:
		return nil
	}
}

func ensureRemoteWorkspace(workspace, workerHost string, timeoutMS int) (string, bool, error) {
	script := strings.Join([]string{
		"set -eu",
		remoteShellAssign("workspace", workspace),
		"if [ -d \"$workspace\" ]; then",
		"  created=0",
		"elif [ -e \"$workspace\" ]; then",
		"  rm -rf \"$workspace\"",
		"  mkdir -p \"$workspace\"",
		"  created=1",
		"else",
		"  mkdir -p \"$workspace\"",
		"  created=1",
		"fi",
		"cd \"$workspace\"",
		"printf '%s\\t%s\\t%s\\n' '" + remoteWorkspaceMarker + "' \"$created\" \"$(pwd -P)\"",
	}, "\n")

	output, status, err := runRemoteCommand(workerHost, script, timeoutMS)
	if err != nil {
		return "", false, err
	}
	if status != 0 {
		return "", false, &WorkspaceHookFailedError{Hook: "remote_prepare", Status: status, Output: output}
	}

	actualWorkspace, created, err := parseRemoteWorkspaceOutput(output)
	if err != nil {
		return "", false, err
	}
	return actualWorkspace, created, nil
}

func runLocalHook(name, command, workspace string, timeoutMS int) error {
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

func runRemoteHook(name, command, workspace, workerHost string, timeoutMS int) error {
	output, status, err := runRemoteCommand(workerHost, "cd "+shellEscape(workspace)+" && "+command, timeoutMS)
	if err != nil {
		return err
	}
	if status != 0 {
		return &WorkspaceHookFailedError{Hook: name, Status: status, Output: output}
	}
	return nil
}

func runRemoteRemoveHook(command, workspace, workerHost string, timeoutMS int) error {
	script := strings.Join([]string{
		remoteShellAssign("workspace", workspace),
		"if [ -d \"$workspace\" ]; then",
		"  cd \"$workspace\"",
		"  " + command,
		"fi",
	}, "\n")

	output, status, err := runRemoteCommand(workerHost, script, timeoutMS)
	if err != nil {
		return err
	}
	if status != 0 {
		return &WorkspaceHookFailedError{Hook: "before_remove", Status: status, Output: output}
	}
	return nil
}

func runRemoteCommand(workerHost, command string, timeoutMS int) (string, int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutMS)*time.Millisecond)
	defer cancel()

	output, status, err := ssh.RunContext(ctx, workerHost, command)
	if ctx.Err() == context.DeadlineExceeded {
		return output, status, &WorkspaceHookTimeoutError{Hook: "remote_command", TimeoutMS: timeoutMS}
	}
	return output, status, err
}

func parseRemoteWorkspaceOutput(output string) (string, bool, error) {
	for _, line := range strings.Split(output, "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), "\t", 3)
		if len(parts) != 3 || parts[0] != remoteWorkspaceMarker {
			continue
		}
		return parts[2], parts[1] == "1", nil
	}

	return "", false, fmt.Errorf("workspace_prepare_failed: invalid_output")
}

func remoteShellAssign(variableName, rawPath string) string {
	return strings.Join([]string{
		variableName + "=" + shellEscape(rawPath),
		"case \"$" + variableName + "\" in",
		"  '~') " + variableName + "=\"$HOME\" ;;",
		"  '~/'*) " + variableName + "=\"$HOME/${" + variableName + "#~/}\" ;;",
		"esac",
	}, "\n")
}

func shellEscape(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
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
