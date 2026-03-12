package runtimeconfig

import "testing"

func TestSetWorkflowFilePath(t *testing.T) {
	resetForTest()

	if err := SetWorkflowFilePath("/tmp/workflow.md"); err != nil {
		t.Fatalf("SetWorkflowFilePath returned error: %v", err)
	}

	if got := WorkflowFilePath(); got != "/tmp/workflow.md" {
		t.Fatalf("WorkflowFilePath() = %q, want %q", got, "/tmp/workflow.md")
	}
}

func TestSetWorkflowFilePathRejectsEmptyValue(t *testing.T) {
	resetForTest()

	if err := SetWorkflowFilePath(""); err == nil {
		t.Fatal("SetWorkflowFilePath() error = nil, want non-nil")
	}
}

func TestSetLogsRoot(t *testing.T) {
	resetForTest()

	if err := SetLogsRoot("/tmp/logs"); err != nil {
		t.Fatalf("SetLogsRoot returned error: %v", err)
	}

	if got := LogsRoot(); got != "/tmp/logs" {
		t.Fatalf("LogsRoot() = %q, want %q", got, "/tmp/logs")
	}
}

func TestSetLogsRootRejectsEmptyValue(t *testing.T) {
	resetForTest()

	if err := SetLogsRoot(""); err == nil {
		t.Fatal("SetLogsRoot() error = nil, want non-nil")
	}
}

func TestSetServerPortOverride(t *testing.T) {
	resetForTest()

	if err := SetServerPortOverride(8080); err != nil {
		t.Fatalf("SetServerPortOverride returned error: %v", err)
	}

	gotPort, gotSet := ServerPortOverride()
	if !gotSet {
		t.Fatal("ServerPortOverride() set = false, want true")
	}

	if gotPort != 8080 {
		t.Fatalf("ServerPortOverride() port = %d, want %d", gotPort, 8080)
	}
}

func TestSetServerPortOverrideRejectsNegativeValue(t *testing.T) {
	resetForTest()

	if err := SetServerPortOverride(-1); err == nil {
		t.Fatal("SetServerPortOverride() error = nil, want non-nil")
	}
}
