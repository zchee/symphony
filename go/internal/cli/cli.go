package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/openai/symphony/go/internal/app"
	"github.com/openai/symphony/go/internal/runtimeconfig"
	"github.com/openai/symphony/go/internal/workspacecleanup"
)

const AcknowledgementFlag = "--i-understand-that-this-will-be-running-without-the-usual-guardrails"
const workspaceBeforeRemoveCommand = "workspace-before-remove"

const usage = "Usage: symphony [--logs-root <path>] [--port <port>] [path-to-WORKFLOW.md]"

var ErrRuntimeNotImplemented = errors.New("runtime bootstrap not implemented")

type Dependencies struct {
	FileRegular           func(string) bool
	SetWorkflowFilePath   func(string) error
	SetLogsRoot           func(string) error
	SetServerPortOverride func(int) error
	EnsureStarted         func() error
	WaitForShutdown       func() int
}

type stringFlag struct {
	value string
	set   bool
}

func (f *stringFlag) String() string {
	return f.value
}

func (f *stringFlag) Set(value string) error {
	f.value = value
	f.set = true
	return nil
}

type intFlag struct {
	value int
	set   bool
}

func (f *intFlag) String() string {
	return fmt.Sprintf("%d", f.value)
}

func (f *intFlag) Set(value string) error {
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return err
	}

	f.value = parsed
	f.set = true
	return nil
}

// Main evaluates CLI arguments, starts the runtime, and returns the intended process exit code.
func Main(args []string, deps Dependencies, stderr io.Writer) int {
	if stderr == nil {
		stderr = io.Discard
	}

	if len(args) > 0 && args[0] == workspaceBeforeRemoveCommand {
		if err := workspacecleanup.Run(args[1:], io.Discard, stderr); err != nil {
			_, _ = fmt.Fprintln(stderr, err.Error())
			return 1
		}
		return 0
	}

	if err := Evaluate(args, deps); err != nil {
		_, _ = fmt.Fprintln(stderr, err.Error())
		return 1
	}

	if deps.WaitForShutdown == nil {
		return 0
	}

	return deps.WaitForShutdown()
}

// Evaluate validates CLI arguments and records startup configuration into the supplied dependencies.
func Evaluate(args []string, deps Dependencies) error {
	deps = withDefaults(deps)

	fs := flag.NewFlagSet("symphony", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	ack := fs.Bool(strings.TrimPrefix(AcknowledgementFlag, "--"), false, "")
	logsRoot := &stringFlag{}
	port := &intFlag{value: -1}

	fs.Var(logsRoot, "logs-root", "")
	fs.Var(port, "port", "")

	if err := fs.Parse(args); err != nil {
		return errors.New(usage)
	}

	if !*ack {
		return errors.New(acknowledgementBanner())
	}

	if logsRoot.set {
		trimmedLogsRoot := strings.TrimSpace(logsRoot.value)
		if trimmedLogsRoot == "" {
			return errors.New(usage)
		}

		if err := deps.SetLogsRoot(mustExpandPath(trimmedLogsRoot)); err != nil {
			return err
		}
	}

	if port.set {
		if port.value < 0 {
			return errors.New(usage)
		}

		if err := deps.SetServerPortOverride(port.value); err != nil {
			return errors.New(usage)
		}
	}

	remaining := fs.Args()
	if len(remaining) > 1 {
		return errors.New(usage)
	}

	workflowPath := "WORKFLOW.md"
	if len(remaining) == 1 {
		workflowPath = remaining[0]
	}

	return Run(workflowPath, deps)
}

// Run expands and validates the workflow path before starting the runtime.
func Run(workflowPath string, deps Dependencies) error {
	deps = withDefaults(deps)

	expandedPath := mustExpandPath(workflowPath)
	if !deps.FileRegular(expandedPath) {
		return fmt.Errorf("Workflow file not found: %s", expandedPath)
	}

	if err := deps.SetWorkflowFilePath(expandedPath); err != nil {
		return err
	}

	if err := deps.EnsureStarted(); err != nil {
		return fmt.Errorf("Failed to start Symphony with workflow %s: %v", expandedPath, err)
	}

	return nil
}

// RuntimeDependencies returns the current process-backed dependency set used by the real binary.
func RuntimeDependencies() Dependencies {
	return withDefaults(Dependencies{
		SetWorkflowFilePath:   runtimeconfig.SetWorkflowFilePath,
		SetLogsRoot:           runtimeconfig.SetLogsRoot,
		SetServerPortOverride: runtimeconfig.SetServerPortOverride,
		EnsureStarted:         app.EnsureStarted,
		WaitForShutdown:       app.WaitForShutdown,
	})
}

func withDefaults(deps Dependencies) Dependencies {
	if deps.FileRegular == nil {
		deps.FileRegular = func(path string) bool {
			info, err := os.Stat(path)
			return err == nil && info.Mode().IsRegular()
		}
	}

	if deps.SetWorkflowFilePath == nil {
		deps.SetWorkflowFilePath = runtimeconfig.SetWorkflowFilePath
	}

	if deps.SetLogsRoot == nil {
		deps.SetLogsRoot = runtimeconfig.SetLogsRoot
	}

	if deps.SetServerPortOverride == nil {
		deps.SetServerPortOverride = runtimeconfig.SetServerPortOverride
	}

	if deps.EnsureStarted == nil {
		deps.EnsureStarted = func() error {
			return ErrRuntimeNotImplemented
		}
	}

	return deps
}

func acknowledgementBanner() string {
	lines := []string{
		"This Symphony implementation is a low key engineering preview.",
		"Codex will run without any guardrails.",
		"SymphonyGo is not a supported product and is presented as-is.",
		fmt.Sprintf("To proceed, start with `%s` CLI argument", AcknowledgementFlag),
	}

	width := 0
	for _, line := range lines {
		if len(line) > width {
			width = len(line)
		}
	}

	border := strings.Repeat("─", width+2)
	spacer := "│ " + strings.Repeat(" ", width) + " │"

	var b strings.Builder
	b.WriteString("\x1b[31;1m")
	b.WriteString("╭")
	b.WriteString(border)
	b.WriteString("╮\n")
	b.WriteString(spacer)
	b.WriteString("\n")
	for _, line := range lines {
		b.WriteString("│ ")
		b.WriteString(line)
		b.WriteString(strings.Repeat(" ", width-len(line)))
		b.WriteString(" │\n")
	}
	b.WriteString(spacer)
	b.WriteString("\n╰")
	b.WriteString(border)
	b.WriteString("╯")
	b.WriteString("\x1b[0m")

	return b.String()
}

func mustExpandPath(path string) string {
	if strings.HasPrefix(path, "~") {
		home, err := os.UserHomeDir()
		if err == nil {
			switch {
			case path == "~":
				path = home
			case strings.HasPrefix(path, "~/"):
				path = filepath.Join(home, strings.TrimPrefix(path, "~/"))
			}
		}
	}

	expanded, err := filepath.Abs(path)
	if err != nil {
		return path
	}

	return expanded
}
