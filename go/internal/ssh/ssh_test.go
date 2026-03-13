package ssh

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunParsesHostPortAndSSHConfig(t *testing.T) {
	testRoot := t.TempDir()
	traceFile := filepath.Join(testRoot, "ssh.trace")
	previousPath := os.Getenv("PATH")
	previousConfig := os.Getenv("SYMPHONY_SSH_CONFIG")
	t.Setenv("PATH", installFakeSSH(t, testRoot, traceFile))
	t.Setenv("SYMPHONY_SSH_CONFIG", "/tmp/symphony-test-ssh-config")
	defer restoreEnv(t, "PATH", previousPath)
	defer restoreEnv(t, "SYMPHONY_SSH_CONFIG", previousConfig)

	output, status, err := Run("localhost:2222", "echo ready")
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}
	if output != "" || status != 0 {
		t.Fatalf("Run() = %q, %d, want empty output and status 0", output, status)
	}

	trace := mustReadFile(t, traceFile)
	for _, fragment := range []string{"-F /tmp/symphony-test-ssh-config", "-T -p 2222 localhost bash -lc", "echo ready"} {
		if !strings.Contains(trace, fragment) {
			t.Fatalf("trace missing %q: %q", fragment, trace)
		}
	}
}

func TestRunKeepsBracketedIPv6TargetsIntact(t *testing.T) {
	testRoot := t.TempDir()
	traceFile := filepath.Join(testRoot, "ssh.trace")
	previousPath := os.Getenv("PATH")
	t.Setenv("PATH", installFakeSSH(t, testRoot, traceFile))
	defer restoreEnv(t, "PATH", previousPath)

	if _, _, err := Run("root@[::1]:2200", "printf ok"); err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	trace := mustReadFile(t, traceFile)
	if !strings.Contains(trace, "-T -p 2200 root@[::1] bash -lc") {
		t.Fatalf("trace = %q, want bracketed IPv6 destination with -p 2200", trace)
	}
}

func TestRunLeavesUnbracketedIPv6StyleTargetsUnchanged(t *testing.T) {
	testRoot := t.TempDir()
	traceFile := filepath.Join(testRoot, "ssh.trace")
	previousPath := os.Getenv("PATH")
	t.Setenv("PATH", installFakeSSH(t, testRoot, traceFile))
	defer restoreEnv(t, "PATH", previousPath)

	if _, _, err := Run("::1:2200", "printf ok"); err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	trace := mustReadFile(t, traceFile)
	if !strings.Contains(trace, "-T ::1:2200 bash -lc") || strings.Contains(trace, "-p 2200") {
		t.Fatalf("trace = %q, want raw unbracketed IPv6-style target", trace)
	}
}

func TestRunReturnsErrorWhenSSHIsUnavailable(t *testing.T) {
	testRoot := t.TempDir()
	previousPath := os.Getenv("PATH")
	t.Setenv("PATH", testRoot)
	defer restoreEnv(t, "PATH", previousPath)

	if _, _, err := Run("localhost", "printf ok"); err != ErrSSHNotFound {
		t.Fatalf("Run() error = %v, want ErrSSHNotFound", err)
	}
}

func TestRemoteShellCommandEscapesSingleQuotes(t *testing.T) {
	if got, want := RemoteShellCommand("printf 'hello'"), `bash -lc 'printf '"'"'hello'"'"''`; got != want {
		t.Fatalf("RemoteShellCommand() = %q, want %q", got, want)
	}
}

func installFakeSSH(t *testing.T, root, traceFile string) string {
	t.Helper()
	fakeBinDir := filepath.Join(root, "bin")
	fakeSSH := filepath.Join(fakeBinDir, "ssh")
	if err := os.MkdirAll(fakeBinDir, 0o755); err != nil {
		t.Fatalf("os.MkdirAll(%q) failed: %v", fakeBinDir, err)
	}
	if err := os.WriteFile(fakeSSH, []byte("#!/bin/sh\nprintf 'ARGV:%s\\n' \"$*\" >> \""+traceFile+"\"\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("os.WriteFile(%q) failed: %v", fakeSSH, err)
	}
	return fakeBinDir + ":" + os.Getenv("PATH")
}

func mustReadFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("os.ReadFile(%q) failed: %v", path, err)
	}
	return string(data)
}

func restoreEnv(t *testing.T, key, value string) {
	t.Helper()
	if value == "" {
		os.Unsetenv(key)
		return
	}
	os.Setenv(key, value)
}
