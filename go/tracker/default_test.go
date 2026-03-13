package tracker

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/openai/symphony/go/linear"
	"github.com/openai/symphony/go/runtimeconfig"
	"github.com/openai/symphony/go/tracker/memory"
)

func TestDefaultReturnsMemoryClientForMemoryTrackerKind(t *testing.T) {
	writeTrackerWorkflow(t, `---
tracker:
  kind: "memory"
---
`)

	client := Default()
	if _, ok := client.(*memory.Client); !ok {
		t.Fatalf("Default() = %T, want *memory.Client", client)
	}
}

func TestDefaultFallsBackToLinearClientForNonMemoryKinds(t *testing.T) {
	writeTrackerWorkflow(t, `---
tracker:
  kind: "linear"
  api_key: "token"
  project_slug: "project"
---
`)

	client := Default()
	if _, ok := client.(*linear.Client); !ok {
		t.Fatalf("Default() = %T, want *linear.Client", client)
	}
}

func writeTrackerWorkflow(t *testing.T, content string) {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "WORKFLOW.md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("os.WriteFile(%q) failed: %v", path, err)
	}
	if err := runtimeconfig.SetWorkflowFilePath(path); err != nil {
		t.Fatalf("runtimeconfig.SetWorkflowFilePath(%q) failed: %v", path, err)
	}
}
