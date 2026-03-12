package workspacecleanup

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

const defaultRepo = "openai/symphony"

const helpText = `Close open GitHub PRs for the current branch before workspace removal.

Usage:

    symphony workspace-before-remove
    symphony workspace-before-remove --branch feature/my-branch
    symphony workspace-before-remove --repo openai/symphony
`

type execFunc func(string, ...string) (string, int, error)

// Run executes the workspace cleanup helper.
func Run(args []string, stdout, stderr io.Writer) error {
	return runWithExec(args, stdout, stderr, defaultExec)
}

func runWithExec(args []string, stdout, stderr io.Writer, runner execFunc) error {
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}

	normalized := normalizeHelpAlias(args)
	fs := flag.NewFlagSet("workspace-before-remove", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	branch := fs.String("branch", "", "")
	repo := fs.String("repo", defaultRepo, "")
	help := fs.Bool("help", false, "")

	if err := fs.Parse(normalized); err != nil {
		return invalidOptionsError(args)
	}

	if *help {
		_, _ = io.WriteString(stdout, helpText)
		return nil
	}

	if fs.NArg() != 0 {
		return invalidOptionsError(fs.Args())
	}

	targetBranch := strings.TrimSpace(*branch)
	if targetBranch == "" {
		targetBranch = currentBranch(runner)
	}
	if targetBranch == "" {
		return nil
	}

	if !ghAvailable() {
		return nil
	}
	if !ghAuthenticated(runner) {
		return nil
	}

	for _, pr := range listOpenPRs(runner, *repo, targetBranch) {
		closePR(runner, stdout, stderr, *repo, targetBranch, pr)
	}

	return nil
}

func normalizeHelpAlias(args []string) []string {
	normalized := make([]string, len(args))
	for i, arg := range args {
		if arg == "-h" {
			normalized[i] = "--help"
			continue
		}
		normalized[i] = arg
	}
	return normalized
}

func invalidOptionsError(values []string) error {
	return fmt.Errorf("Invalid option(s): %q", values)
}

func ghAvailable() bool {
	_, err := exec.LookPath("gh")
	return err == nil
}

func ghAuthenticated(runner execFunc) bool {
	_, status, err := runner("gh", "auth", "status")
	return err == nil && status == 0
}

func currentBranch(runner execFunc) string {
	output, status, err := runner("git", "branch", "--show-current")
	if err != nil || status != 0 {
		return ""
	}
	return strings.TrimSpace(output)
}

func listOpenPRs(runner execFunc, repo, branch string) []string {
	output, status, err := runner(
		"gh", "pr", "list",
		"--repo", repo,
		"--head", branch,
		"--state", "open",
		"--json", "number",
		"--jq", ".[].number",
	)
	if err != nil || status != 0 {
		return nil
	}
	lines := strings.Split(output, "\n")
	prs := make([]string, 0, len(lines))
	for _, line := range lines {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			prs = append(prs, trimmed)
		}
	}
	return prs
}

func closePR(runner execFunc, stdout, stderr io.Writer, repo, branch, number string) {
	output, status, err := runner(
		"gh", "pr", "close", number,
		"--repo", repo,
		"--comment", closingComment(branch),
	)
	if err == nil && status == 0 {
		_, _ = fmt.Fprintf(stdout, "Closed PR #%s for branch %s\n", number, branch)
		return
	}

	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		_, _ = fmt.Fprintf(stderr, "Failed to close PR #%s for branch %s: exit %d\n", number, branch, status)
		return
	}
	_, _ = fmt.Fprintf(stderr, "Failed to close PR #%s for branch %s: exit %d output=%q\n", number, branch, status, trimmed)
}

func closingComment(branch string) string {
	return fmt.Sprintf("Closing because the Linear issue for branch %s entered a terminal state without merge.", branch)
}

func defaultExec(command string, args ...string) (string, int, error) {
	path, err := exec.LookPath(command)
	if err != nil {
		return "", 0, err
	}
	cmd := exec.Command(path, args...)
	output, err := cmd.CombinedOutput()
	if err == nil {
		return string(output), 0, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return string(output), exitErr.ExitCode(), nil
	}
	return string(output), 0, err
}
