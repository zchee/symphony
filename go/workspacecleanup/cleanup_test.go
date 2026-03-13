package workspacecleanup

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunPrintsHelp(t *testing.T) {
	var stdout bytes.Buffer
	if err := Run([]string{"--help"}, &stdout, ioDiscard{}); err != nil {
		t.Fatalf("Run(--help) returned error: %v", err)
	}
	if !strings.Contains(stdout.String(), "workspace-before-remove") {
		t.Fatalf("stdout = %q, want help text", stdout.String())
	}
}

func TestRunFailsOnInvalidOptions(t *testing.T) {
	err := Run([]string{"--wat"}, ioDiscard{}, ioDiscard{})
	if err == nil || !strings.Contains(err.Error(), "Invalid option") {
		t.Fatalf("Run(--wat) error = %v, want invalid option error", err)
	}
}

func TestRunNoOpsWhenBranchUnavailable(t *testing.T) {
	withPath(t, nil, func() {
		var stdout, stderr bytes.Buffer
		if err := Run(nil, &stdout, &stderr); err != nil {
			t.Fatalf("Run() returned error: %v", err)
		}
		if stdout.Len() != 0 || stderr.Len() != 0 {
			t.Fatalf("stdout/stderr = %q/%q, want empty output", stdout.String(), stderr.String())
		}
	})
}

func TestRunNoOpsWhenGHUnavailable(t *testing.T) {
	withPath(t, nil, func() {
		var stdout, stderr bytes.Buffer
		if err := Run([]string{"--branch", "feature/no-gh"}, &stdout, &stderr); err != nil {
			t.Fatalf("Run() returned error: %v", err)
		}
		if stdout.Len() != 0 || stderr.Len() != 0 {
			t.Fatalf("stdout/stderr = %q/%q, want empty output", stdout.String(), stderr.String())
		}
	})
}

func TestRunUsesCurrentBranchWhenBranchFlagIsOmitted(t *testing.T) {
	withFakeBinaries(t, map[string]string{
		"gh": `#!/bin/sh
printf '%s\n' "$*" >> "$GH_LOG"
if [ "$1" = "auth" ] && [ "$2" = "status" ]; then
  exit 0
fi
if [ "$1" = "pr" ] && [ "$2" = "list" ]; then
  printf '101\n102\n'
  exit 0
fi
if [ "$1" = "pr" ] && [ "$2" = "close" ] && [ "$3" = "101" ]; then
  exit 0
fi
if [ "$1" = "pr" ] && [ "$2" = "close" ] && [ "$3" = "102" ]; then
  printf 'boom\n' >&2
  exit 17
fi
exit 99
`,
		"git": `#!/bin/sh
printf 'feature/workpad\n'
exit 0
`,
	}, func(logPath string) {
		var stdout, stderr bytes.Buffer
		if err := Run(nil, &stdout, &stderr); err != nil {
			t.Fatalf("Run() returned error: %v", err)
		}
		if !strings.Contains(stdout.String(), "Closed PR #101 for branch feature/workpad") {
			t.Fatalf("stdout = %q, want close success message", stdout.String())
		}
		if !strings.Contains(stderr.String(), "Failed to close PR #102 for branch feature/workpad") {
			t.Fatalf("stderr = %q, want close failure message", stderr.String())
		}

		logText := mustReadFile(t, logPath)
		if !strings.Contains(logText, "pr list --repo openai/symphony --head feature/workpad --state open --json number --jq .[].number") {
			t.Fatalf("log = %q, want pr list for current branch", logText)
		}
	})
}

func TestRunClosesOpenPullRequestsAndToleratesFailures(t *testing.T) {
	withFakeBinaries(t, map[string]string{
		"gh": `#!/bin/sh
printf '%s\n' "$*" >> "$GH_LOG"
if [ "$1" = "auth" ] && [ "$2" = "status" ]; then
  exit 0
fi
if [ "$1" = "pr" ] && [ "$2" = "list" ]; then
  printf '101\n102\n'
  exit 0
fi
if [ "$1" = "pr" ] && [ "$2" = "close" ] && [ "$3" = "101" ]; then
  exit 0
fi
if [ "$1" = "pr" ] && [ "$2" = "close" ] && [ "$3" = "102" ]; then
  printf 'boom\n' >&2
  exit 17
fi
exit 99
`,
	}, func(logPath string) {
		var stdout, stderr bytes.Buffer
		if err := Run([]string{"--branch", "feature/workpad"}, &stdout, &stderr); err != nil {
			t.Fatalf("Run() returned error: %v", err)
		}
		if !strings.Contains(stdout.String(), "Closed PR #101 for branch feature/workpad") {
			t.Fatalf("stdout = %q, want close success message", stdout.String())
		}
		if !strings.Contains(stderr.String(), "Failed to close PR #102 for branch feature/workpad") {
			t.Fatalf("stderr = %q, want close failure message", stderr.String())
		}

		logText := mustReadFile(t, logPath)
		if !strings.Contains(logText, "auth status") || !strings.Contains(logText, "pr close 101 --repo openai/symphony") {
			t.Fatalf("log = %q, want auth/list/close calls", logText)
		}
	})
}

func TestRunFormatsCloseFailuresWithoutOutput(t *testing.T) {
	withFakeBinaries(t, map[string]string{
		"gh": `#!/bin/sh
printf '%s\n' "$*" >> "$GH_LOG"
if [ "$1" = "auth" ] && [ "$2" = "status" ]; then
  exit 0
fi
if [ "$1" = "pr" ] && [ "$2" = "list" ]; then
  printf '102\n'
  exit 0
fi
if [ "$1" = "pr" ] && [ "$2" = "close" ] && [ "$3" = "102" ]; then
  exit 17
fi
exit 99
`,
	}, func(_ string) {
		var stdout, stderr bytes.Buffer
		if err := Run([]string{"--branch", "feature/no-output"}, &stdout, &stderr); err != nil {
			t.Fatalf("Run() returned error: %v", err)
		}
		if !strings.Contains(stderr.String(), "Failed to close PR #102 for branch feature/no-output: exit 17") {
			t.Fatalf("stderr = %q, want exit status message", stderr.String())
		}
		if strings.Contains(stderr.String(), "output=") {
			t.Fatalf("stderr = %q, want no output field", stderr.String())
		}
	})
}

func TestRunNoOpsWhenPRListFails(t *testing.T) {
	withFakeBinaries(t, map[string]string{
		"gh": `#!/bin/sh
printf '%s\n' "$*" >> "$GH_LOG"
if [ "$1" = "auth" ] && [ "$2" = "status" ]; then
  exit 0
fi
if [ "$1" = "pr" ] && [ "$2" = "list" ]; then
  exit 1
fi
exit 99
`,
	}, func(logPath string) {
		var stdout, stderr bytes.Buffer
		if err := Run([]string{"--branch", "feature/list-fails"}, &stdout, &stderr); err != nil {
			t.Fatalf("Run() returned error: %v", err)
		}
		if stdout.Len() != 0 || stderr.Len() != 0 {
			t.Fatalf("stdout/stderr = %q/%q, want empty output", stdout.String(), stderr.String())
		}
		logText := mustReadFile(t, logPath)
		if !strings.Contains(logText, "pr list --repo openai/symphony --head feature/list-fails --state open --json number --jq .[].number") {
			t.Fatalf("log = %q, want pr list call", logText)
		}
		if strings.Contains(logText, "pr close") {
			t.Fatalf("log = %q, want no close calls", logText)
		}
	})
}

func TestRunNoOpsWhenGHAuthFails(t *testing.T) {
	withFakeBinaries(t, map[string]string{
		"gh": `#!/bin/sh
printf '%s\n' "$*" >> "$GH_LOG"
if [ "$1" = "auth" ] && [ "$2" = "status" ]; then
  exit 1
fi
exit 99
`,
	}, func(logPath string) {
		var stdout, stderr bytes.Buffer
		if err := Run([]string{"--branch", "feature/no-auth"}, &stdout, &stderr); err != nil {
			t.Fatalf("Run() returned error: %v", err)
		}
		if stdout.Len() != 0 || stderr.Len() != 0 {
			t.Fatalf("stdout/stderr = %q/%q, want empty output", stdout.String(), stderr.String())
		}
		logText := mustReadFile(t, logPath)
		if !strings.Contains(logText, "auth status") {
			t.Fatalf("log = %q, want auth status call", logText)
		}
		if strings.Contains(logText, "pr list") {
			t.Fatalf("log = %q, want no pr list call", logText)
		}
	})
}

type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) { return len(p), nil }

func withPath(t *testing.T, paths []string, fn func()) {
	t.Helper()
	t.Setenv("PATH", strings.Join(paths, ":"))
	fn()
}

func withFakeBinaries(t *testing.T, scripts map[string]string, fn func(logPath string)) {
	t.Helper()
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	logPath := filepath.Join(root, "gh.log")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("os.MkdirAll(%q) failed: %v", binDir, err)
	}
	if err := os.WriteFile(logPath, []byte(""), 0o644); err != nil {
		t.Fatalf("os.WriteFile(%q) failed: %v", logPath, err)
	}
	for name, script := range scripts {
		path := filepath.Join(binDir, name)
		if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
			t.Fatalf("os.WriteFile(%q) failed: %v", path, err)
		}
	}
	t.Setenv("GH_LOG", logPath)
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))
	fn(logPath)
}

func mustReadFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("os.ReadFile(%q) failed: %v", path, err)
	}
	return string(data)
}
