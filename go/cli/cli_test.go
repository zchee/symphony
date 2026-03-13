package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/openai/symphony/go/app"
	"github.com/openai/symphony/go/runtimeconfig"
)

func TestEvaluateReturnsAcknowledgementBannerWhenFlagIsMissing(t *testing.T) {
	deps := Dependencies{
		FileRegular: func(string) bool {
			t.Fatal("FileRegular should not be called when acknowledgement is missing")
			return false
		},
		SetWorkflowFilePath: func(string) error {
			t.Fatal("SetWorkflowFilePath should not be called when acknowledgement is missing")
			return nil
		},
		SetLogsRoot: func(string) error {
			t.Fatal("SetLogsRoot should not be called when acknowledgement is missing")
			return nil
		},
		SetServerPortOverride: func(int) error {
			t.Fatal("SetServerPortOverride should not be called when acknowledgement is missing")
			return nil
		},
		EnsureStarted: func() error {
			t.Fatal("EnsureStarted should not be called when acknowledgement is missing")
			return nil
		},
	}

	err := Evaluate([]string{"WORKFLOW.md"}, deps)
	if err == nil {
		t.Fatal("Evaluate() error = nil, want non-nil")
	}

	message := err.Error()
	if !bytes.Contains([]byte(message), []byte("This Symphony implementation is a low key engineering preview.")) {
		t.Fatalf("Evaluate() error = %q, missing preview line", message)
	}
	if !bytes.Contains([]byte(message), []byte(AcknowledgementFlag)) {
		t.Fatalf("Evaluate() error = %q, missing acknowledgement flag", message)
	}
}

func TestEvaluateDefaultsToWorkflowDotMDWhenWorkflowPathIsMissing(t *testing.T) {
	var checkedPath string
	var setPath string

	deps := Dependencies{
		FileRegular: func(path string) bool {
			checkedPath = path
			return filepath.Base(path) == "WORKFLOW.md"
		},
		SetWorkflowFilePath: func(path string) error {
			setPath = path
			return nil
		},
		SetLogsRoot:           func(string) error { return nil },
		SetServerPortOverride: func(int) error { return nil },
		EnsureStarted:         func() error { return nil },
	}

	if err := Evaluate([]string{AcknowledgementFlag}, deps); err != nil {
		t.Fatalf("Evaluate() returned error: %v", err)
	}

	if filepath.Base(checkedPath) != "WORKFLOW.md" {
		t.Fatalf("checked workflow path base = %q, want WORKFLOW.md", filepath.Base(checkedPath))
	}
	if setPath != checkedPath {
		t.Fatalf("workflow set path = %q, want %q", setPath, checkedPath)
	}
}

func TestEvaluateUsesExplicitWorkflowPathOverrideWhenProvided(t *testing.T) {
	var checkedPath string
	var setPath string
	workflowPath := filepath.Join("tmp", "custom", "WORKFLOW.md")
	expected := filepath.Clean(filepath.Join(mustExpandPath("."), workflowPath))

	deps := Dependencies{
		FileRegular: func(path string) bool {
			checkedPath = path
			return path == expected
		},
		SetWorkflowFilePath: func(path string) error {
			setPath = path
			return nil
		},
		SetLogsRoot:           func(string) error { return nil },
		SetServerPortOverride: func(int) error { return nil },
		EnsureStarted:         func() error { return nil },
	}

	if err := Evaluate([]string{AcknowledgementFlag, workflowPath}, deps); err != nil {
		t.Fatalf("Evaluate() returned error: %v", err)
	}

	if checkedPath != expected {
		t.Fatalf("checked workflow path = %q, want %q", checkedPath, expected)
	}
	if setPath != expected {
		t.Fatalf("set workflow path = %q, want %q", setPath, expected)
	}
}

func TestEvaluatePassesExpandedLogsRootToDependencies(t *testing.T) {
	var gotLogsRoot string

	deps := Dependencies{
		FileRegular:         func(string) bool { return true },
		SetWorkflowFilePath: func(string) error { return nil },
		SetLogsRoot: func(path string) error {
			gotLogsRoot = path
			return nil
		},
		SetServerPortOverride: func(int) error { return nil },
		EnsureStarted:         func() error { return nil },
	}

	if err := Evaluate([]string{AcknowledgementFlag, "--logs-root", "tmp/custom-logs", "WORKFLOW.md"}, deps); err != nil {
		t.Fatalf("Evaluate() returned error: %v", err)
	}

	expected := filepath.Clean(filepath.Join(mustExpandPath("."), "tmp/custom-logs"))
	if gotLogsRoot != expected {
		t.Fatalf("logs root = %q, want %q", gotLogsRoot, expected)
	}
}

func TestEvaluateReturnsUsageForExplicitBlankLogsRoot(t *testing.T) {
	deps := Dependencies{
		FileRegular:           func(string) bool { return true },
		SetWorkflowFilePath:   func(string) error { return nil },
		SetLogsRoot:           func(string) error { return nil },
		SetServerPortOverride: func(int) error { return nil },
		EnsureStarted:         func() error { return nil },
	}

	err := Evaluate([]string{AcknowledgementFlag, "--logs-root", "   ", "WORKFLOW.md"}, deps)
	if err == nil {
		t.Fatal("Evaluate() error = nil, want non-nil")
	}
	if err.Error() != usage {
		t.Fatalf("Evaluate() error = %q, want %q", err.Error(), usage)
	}
}

func TestEvaluatePassesPortOverrideToDependencies(t *testing.T) {
	gotPort := -1

	deps := Dependencies{
		FileRegular:         func(string) bool { return true },
		SetWorkflowFilePath: func(string) error { return nil },
		SetLogsRoot:         func(string) error { return nil },
		SetServerPortOverride: func(port int) error {
			gotPort = port
			return nil
		},
		EnsureStarted: func() error { return nil },
	}

	if err := Evaluate([]string{AcknowledgementFlag, "--port", "8080", "WORKFLOW.md"}, deps); err != nil {
		t.Fatalf("Evaluate() returned error: %v", err)
	}

	if gotPort != 8080 {
		t.Fatalf("port override = %d, want %d", gotPort, 8080)
	}
}

func TestEvaluateReturnsUsageForNegativePort(t *testing.T) {
	deps := Dependencies{
		FileRegular:           func(string) bool { return true },
		SetWorkflowFilePath:   func(string) error { return nil },
		SetLogsRoot:           func(string) error { return nil },
		SetServerPortOverride: func(int) error { return nil },
		EnsureStarted:         func() error { return nil },
	}

	err := Evaluate([]string{AcknowledgementFlag, "--port", "-1", "WORKFLOW.md"}, deps)
	if err == nil {
		t.Fatal("Evaluate() error = nil, want non-nil")
	}
	if err.Error() != usage {
		t.Fatalf("Evaluate() error = %q, want %q", err.Error(), usage)
	}
}

func TestEvaluateReturnsNotFoundWhenWorkflowFileDoesNotExist(t *testing.T) {
	deps := Dependencies{
		FileRegular:           func(string) bool { return false },
		SetWorkflowFilePath:   func(string) error { return nil },
		SetLogsRoot:           func(string) error { return nil },
		SetServerPortOverride: func(int) error { return nil },
		EnsureStarted:         func() error { return nil },
	}

	err := Evaluate([]string{AcknowledgementFlag, "WORKFLOW.md"}, deps)
	if err == nil {
		t.Fatal("Evaluate() error = nil, want non-nil")
	}
	if got := err.Error(); !bytes.Contains([]byte(got), []byte("Workflow file not found:")) {
		t.Fatalf("Evaluate() error = %q, want workflow not found message", got)
	}
}

func TestEvaluateReturnsStartupErrorWhenRuntimeCannotStart(t *testing.T) {
	deps := Dependencies{
		FileRegular:           func(string) bool { return true },
		SetWorkflowFilePath:   func(string) error { return nil },
		SetLogsRoot:           func(string) error { return nil },
		SetServerPortOverride: func(int) error { return nil },
		EnsureStarted: func() error {
			return errors.New("boom")
		},
	}

	err := Evaluate([]string{AcknowledgementFlag, "WORKFLOW.md"}, deps)
	if err == nil {
		t.Fatal("Evaluate() error = nil, want non-nil")
	}

	message := err.Error()
	if !bytes.Contains([]byte(message), []byte("Failed to start Symphony with workflow")) {
		t.Fatalf("Evaluate() error = %q, want startup error prefix", message)
	}
	if !bytes.Contains([]byte(message), []byte("boom")) {
		t.Fatalf("Evaluate() error = %q, want embedded runtime error", message)
	}
}

func TestEvaluateReturnsUsageForExtraPositionalArguments(t *testing.T) {
	deps := Dependencies{
		FileRegular:           func(string) bool { return true },
		SetWorkflowFilePath:   func(string) error { return nil },
		SetLogsRoot:           func(string) error { return nil },
		SetServerPortOverride: func(int) error { return nil },
		EnsureStarted:         func() error { return nil },
	}

	err := Evaluate([]string{AcknowledgementFlag, "one", "two"}, deps)
	if err == nil {
		t.Fatal("Evaluate() error = nil, want non-nil")
	}
	if err.Error() != usage {
		t.Fatalf("Evaluate() error = %q, want %q", err.Error(), usage)
	}
}

func TestMainWritesErrorToStderrAndReturnsOneOnFailure(t *testing.T) {
	var stderr bytes.Buffer

	exitCode := Main([]string{"WORKFLOW.md"}, Dependencies{}, &stderr)
	if exitCode != 1 {
		t.Fatalf("Main() exit code = %d, want %d", exitCode, 1)
	}
	if stderr.Len() == 0 {
		t.Fatal("Main() stderr is empty, want error output")
	}
}

func TestMainWaitsForShutdownWhenEvaluationSucceeds(t *testing.T) {
	var stderr bytes.Buffer
	waitCalled := false

	deps := Dependencies{
		FileRegular:           func(string) bool { return true },
		SetWorkflowFilePath:   func(string) error { return nil },
		SetLogsRoot:           func(string) error { return nil },
		SetServerPortOverride: func(int) error { return nil },
		EnsureStarted:         func() error { return nil },
		WaitForShutdown: func() int {
			waitCalled = true
			return 17
		},
	}

	exitCode := Main([]string{AcknowledgementFlag, "WORKFLOW.md"}, deps, &stderr)
	if exitCode != 17 {
		t.Fatalf("Main() exit code = %d, want %d", exitCode, 17)
	}
	if !waitCalled {
		t.Fatal("Main() did not call WaitForShutdown")
	}
	if stderr.Len() != 0 {
		t.Fatalf("Main() stderr = %q, want empty output", stderr.String())
	}
}

func TestMainRunsWorkspaceBeforeRemoveWithoutAcknowledgement(t *testing.T) {
	dir := t.TempDir()
	ghPath := filepath.Join(dir, "gh")
	logPath := filepath.Join(dir, "gh.log")
	if err := os.WriteFile(logPath, []byte(""), 0o644); err != nil {
		t.Fatalf("os.WriteFile(%q) failed: %v", logPath, err)
	}
	if err := os.WriteFile(ghPath, []byte(`#!/bin/sh
printf '%s\n' "$*" >> "$GH_LOG"
if [ "$1" = "auth" ] && [ "$2" = "status" ]; then
  exit 0
fi
if [ "$1" = "pr" ] && [ "$2" = "list" ]; then
  printf '101\n'
  exit 0
fi
if [ "$1" = "pr" ] && [ "$2" = "close" ] && [ "$3" = "101" ]; then
  exit 0
fi
exit 99
`), 0o755); err != nil {
		t.Fatalf("os.WriteFile(%q) failed: %v", ghPath, err)
	}
	t.Setenv("GH_LOG", logPath)
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))

	var stderr bytes.Buffer
	exitCode := Main([]string{workspaceBeforeRemoveCommand, "--branch", "feature/cleanup"}, Dependencies{}, &stderr)
	if exitCode != 0 {
		t.Fatalf("Main() exit code = %d, want 0 stderr=%q", exitCode, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("Main() stderr = %q, want empty output", stderr.String())
	}
}

func TestMainWorkspaceBeforeRemoveWritesErrorsToStderr(t *testing.T) {
	var stderr bytes.Buffer
	exitCode := Main([]string{workspaceBeforeRemoveCommand, "--wat"}, Dependencies{}, &stderr)
	if exitCode != 1 {
		t.Fatalf("Main() exit code = %d, want 1", exitCode)
	}
	if !bytes.Contains(stderr.Bytes(), []byte("Invalid option")) {
		t.Fatalf("Main() stderr = %q, want invalid option error", stderr.String())
	}
}

func TestRuntimeDependenciesStartTheRealRuntime(t *testing.T) {
	t.Cleanup(func() {
		_ = app.StopDefault()
	})

	dir := t.TempDir()
	workflowPath := filepath.Join(dir, "WORKFLOW.md")
	if err := os.WriteFile(workflowPath, []byte("---\ntracker:\n  kind: \"memory\"\n---\nPrompt\n"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(%q) failed: %v", workflowPath, err)
	}
	if err := runtimeconfig.SetWorkflowFilePath(workflowPath); err != nil {
		t.Fatalf("runtimeconfig.SetWorkflowFilePath(%q) failed: %v", workflowPath, err)
	}

	deps := RuntimeDependencies()

	if err := deps.EnsureStarted(); err != nil {
		t.Fatalf("EnsureStarted() returned error: %v", err)
	}
}

func TestMustExpandPathExpandsHomeDirectory(t *testing.T) {
	homeDir, err := filepath.Abs(t.TempDir())
	if err != nil {
		t.Fatalf("filepath.Abs() returned error: %v", err)
	}

	t.Setenv("HOME", homeDir)

	got := mustExpandPath("~/workflow.md")
	want := filepath.Join(homeDir, "workflow.md")
	if got != want {
		t.Fatalf("mustExpandPath() = %q, want %q", got, want)
	}
}
