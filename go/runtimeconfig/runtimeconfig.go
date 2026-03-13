package runtimeconfig

import (
	"errors"
	"sync"
)

var errEmptyValue = errors.New("value must not be empty")

var (
	mu sync.RWMutex

	workflowFilePath        string
	workflowFilePathChanged func()
	logsRoot                string
	serverPort              int
	serverPortSet           bool
)

// SetWorkflowFilePath stores the canonical workflow file path for the current process.
func SetWorkflowFilePath(path string) error {
	if path == "" {
		return errEmptyValue
	}

	mu.Lock()
	workflowFilePath = path
	hook := workflowFilePathChanged
	mu.Unlock()

	if hook != nil {
		hook()
	}
	return nil
}

// ClearWorkflowFilePath removes the current workflow file override.
func ClearWorkflowFilePath() {
	mu.Lock()
	workflowFilePath = ""
	hook := workflowFilePathChanged
	mu.Unlock()

	if hook != nil {
		hook()
	}
}

// WorkflowFilePath returns the current workflow file path override.
func WorkflowFilePath() string {
	mu.RLock()
	defer mu.RUnlock()

	return workflowFilePath
}

// SetWorkflowFilePathChangedHook registers a callback invoked after workflow path changes.
func SetWorkflowFilePathChangedHook(hook func()) {
	mu.Lock()
	defer mu.Unlock()

	workflowFilePathChanged = hook
}

// SetLogsRoot stores the logs root override for the current process.
func SetLogsRoot(path string) error {
	if path == "" {
		return errEmptyValue
	}

	mu.Lock()
	defer mu.Unlock()

	logsRoot = path
	return nil
}

// LogsRoot returns the current logs root override.
func LogsRoot() string {
	mu.RLock()
	defer mu.RUnlock()

	return logsRoot
}

// SetServerPortOverride stores a server port override for the current process.
func SetServerPortOverride(port int) error {
	if port < 0 {
		return errors.New("port must be non-negative")
	}

	mu.Lock()
	defer mu.Unlock()

	serverPort = port
	serverPortSet = true
	return nil
}

// ServerPortOverride returns the configured port override when one exists.
func ServerPortOverride() (int, bool) {
	mu.RLock()
	defer mu.RUnlock()

	return serverPort, serverPortSet
}

func resetForTest() {
	mu.Lock()
	defer mu.Unlock()

	workflowFilePath = ""
	logsRoot = ""
	serverPort = 0
	serverPortSet = false
}
