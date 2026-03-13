package ssh

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

var ErrSSHNotFound = errors.New("ssh_not_found")

// Run executes one remote shell command through ssh and returns combined stdout/stderr plus exit code.
func Run(host, command string) (string, int, error) {
	return RunContext(context.Background(), host, command)
}

// RunContext executes one remote shell command through ssh with context cancellation.
func RunContext(ctx context.Context, host, command string) (string, int, error) {
	executable, err := sshExecutable()
	if err != nil {
		return "", 0, err
	}

	cmd := exec.CommandContext(ctx, executable, sshArgs(host, command)...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()
	output := stdout.String() + stderr.String()
	if runErr == nil {
		return output, 0, nil
	}

	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		return output, exitErr.ExitCode(), nil
	}

	return output, 0, runErr
}

// StartCommand returns an ssh-backed command configured for remote stdio streaming.
func StartCommand(host, command string) (*exec.Cmd, error) {
	executable, err := sshExecutable()
	if err != nil {
		return nil, err
	}

	return exec.Command(executable, sshArgs(host, command)...), nil
}

// RemoteShellCommand wraps a command for `bash -lc` execution on the remote side.
func RemoteShellCommand(command string) string {
	return "bash -lc " + shellEscape(command)
}

func sshExecutable() (string, error) {
	executable, err := exec.LookPath("ssh")
	if err != nil {
		return "", ErrSSHNotFound
	}
	return executable, nil
}

func sshArgs(host, command string) []string {
	target := parseTarget(host)
	args := maybePutConfig(nil)
	args = append(args, "-T")
	if target.port != "" {
		args = append(args, "-p", target.port)
	}
	args = append(args, target.destination, RemoteShellCommand(command))
	return args
}

func maybePutConfig(args []string) []string {
	configPath := os.Getenv("SYMPHONY_SSH_CONFIG")
	if strings.TrimSpace(configPath) == "" {
		return args
	}

	return append(args, "-F", configPath)
}

type parsedTarget struct {
	destination string
	port        string
}

func parseTarget(target string) parsedTarget {
	trimmed := strings.TrimSpace(target)

	matches := hostPortPattern(trimmed)
	if matches == nil {
		return parsedTarget{destination: trimmed}
	}

	destination, port := matches[0], matches[1]
	if validPortDestination(destination) {
		return parsedTarget{destination: destination, port: port}
	}

	return parsedTarget{destination: trimmed}
}

func hostPortPattern(target string) []string {
	lastColon := strings.LastIndex(target, ":")
	if lastColon <= 0 || lastColon == len(target)-1 {
		return nil
	}

	destination := target[:lastColon]
	port := target[lastColon+1:]
	for _, r := range port {
		if r < '0' || r > '9' {
			return nil
		}
	}

	return []string{destination, port}
}

func validPortDestination(destination string) bool {
	return destination != "" && (!strings.Contains(destination, ":") || bracketedHost(destination))
}

func bracketedHost(destination string) bool {
	return strings.Contains(destination, "[") && strings.Contains(destination, "]")
}

func shellEscape(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

// FormatRemoteChdirExec renders a remote command that changes directory first and then execs.
func FormatRemoteChdirExec(workspace, command string) string {
	return fmt.Sprintf("cd %s && exec %s", shellEscape(workspace), command)
}
