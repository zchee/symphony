package workflow

import (
	"errors"
	"fmt"
	"hash/fnv"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/openai/symphony/go/runtimeconfig"
	"gopkg.in/yaml.v3"
)

const FileName = "WORKFLOW.md"
const defaultPollInterval = 1 * time.Second

var ErrWorkflowFrontMatterNotMap = errors.New("workflow_front_matter_not_a_map")

type Loaded struct {
	Config         map[string]any
	Prompt         string
	PromptTemplate string
}

type MissingWorkflowFileError struct {
	Path   string
	Reason error
}

func (e *MissingWorkflowFileError) Error() string {
	return fmt.Sprintf("missing_workflow_file path=%s: %v", e.Path, e.Reason)
}

func (e *MissingWorkflowFileError) Unwrap() error {
	return e.Reason
}

type ParseError struct {
	Reason error
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("workflow_parse_error: %v", e.Reason)
}

func (e *ParseError) Unwrap() error {
	return e.Reason
}

type PathResolver struct {
	mu           sync.RWMutex
	explicitPath string
}

type Store struct {
	mu           sync.Mutex
	state        *storeState
	pollRefCount int
	pollStop     chan struct{}
	pollDone     chan struct{}
}

type storeState struct {
	path     string
	stamp    fileStamp
	workflow Loaded
}

type fileStamp struct {
	modUnixNano int64
	size        int64
	hash        uint32
}

var defaultStore = NewStore()
var defaultStoreMu sync.RWMutex

func init() {
	runtimeconfig.SetWorkflowFilePathChangedHook(func() {
		defaultStoreMu.RLock()
		store := defaultStore
		defaultStoreMu.RUnlock()
		store.forceReloadIfPolling()
	})
}

func NewPathResolver() *PathResolver {
	return &PathResolver{}
}

func NewStore() *Store {
	return &Store{}
}

// Current returns the current workflow, reloading when the file stamp changes and
// preserving the last known good workflow when reload fails.
func Current() (Loaded, error) {
	defaultStoreMu.RLock()
	store := defaultStore
	defaultStoreMu.RUnlock()
	return store.Current("")
}

// ForceReload reloads the workflow immediately and returns an error on reload failure.
func ForceReload() error {
	defaultStoreMu.RLock()
	store := defaultStore
	defaultStoreMu.RUnlock()
	return store.ForceReload("")
}

// StartDefaultPolling starts the default workflow poller.
func StartDefaultPolling() error {
	defaultStoreMu.RLock()
	store := defaultStore
	defaultStoreMu.RUnlock()
	return store.StartPolling(defaultPollInterval)
}

// StopDefaultPolling stops the default workflow poller when no users remain.
func StopDefaultPolling() {
	defaultStoreMu.RLock()
	store := defaultStore
	defaultStoreMu.RUnlock()
	store.StopPolling()
}

// ResetDefaultStoreForTest clears the process-local workflow cache for deterministic tests.
func ResetDefaultStoreForTest() {
	defaultStoreMu.Lock()
	previous := defaultStore
	defaultStore = NewStore()
	defaultStoreMu.Unlock()
	if previous != nil {
		previous.StopPolling()
	}
}

func (r *PathResolver) Path(cwd string) string {
	r.mu.RLock()
	explicitPath := r.explicitPath
	r.mu.RUnlock()

	if explicitPath != "" {
		return explicitPath
	}

	if cwd == "" {
		workingDirectory, err := os.Getwd()
		if err == nil {
			cwd = workingDirectory
		}
	}

	return filepath.Join(cwd, FileName)
}

func (r *PathResolver) Set(path string) {
	r.mu.Lock()
	r.explicitPath = path
	r.mu.Unlock()
}

func (r *PathResolver) Clear() {
	r.mu.Lock()
	r.explicitPath = ""
	r.mu.Unlock()
}

func (s *Store) Current(path string) (Loaded, error) {
	resolvedPath := resolvePath(path)

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.state == nil {
		state, err := loadState(resolvedPath)
		if err != nil {
			return Loaded{}, err
		}
		s.state = state
		return state.workflow, nil
	}

	if resolvedPath != s.state.path {
		return s.reloadPathLocked(resolvedPath, false)
	}

	stamp, err := currentStamp(resolvedPath)
	if err != nil {
		logReloadError(resolvedPath, err)
		return s.state.workflow, nil
	}
	if stamp == s.state.stamp {
		return s.state.workflow, nil
	}

	return s.reloadPathLocked(resolvedPath, false)
}

func (s *Store) ForceReload(path string) error {
	resolvedPath := resolvePath(path)

	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.reloadPathLocked(resolvedPath, true)
	return err
}

// StartPolling starts a background poller that keeps the store warm and aligned with workflow file changes.
func (s *Store) StartPolling(interval time.Duration) error {
	if interval <= 0 {
		interval = defaultPollInterval
	}

	s.mu.Lock()
	if s.state == nil {
		state, err := loadState(resolvePath(""))
		if err != nil {
			s.mu.Unlock()
			return err
		}
		s.state = state
	}
	if s.pollRefCount > 0 {
		s.pollRefCount++
		s.mu.Unlock()
		return nil
	}

	stopCh := make(chan struct{})
	doneCh := make(chan struct{})
	s.pollRefCount = 1
	s.pollStop = stopCh
	s.pollDone = doneCh
	s.mu.Unlock()

	go s.pollLoop(interval, stopCh, doneCh)
	return nil
}

// StopPolling releases one polling user and stops the background poller when the count reaches zero.
func (s *Store) StopPolling() {
	s.mu.Lock()
	if s.pollRefCount == 0 {
		s.mu.Unlock()
		return
	}
	s.pollRefCount--
	if s.pollRefCount > 0 {
		s.mu.Unlock()
		return
	}
	stopCh := s.pollStop
	doneCh := s.pollDone
	s.pollStop = nil
	s.pollDone = nil
	s.mu.Unlock()

	if stopCh != nil {
		close(stopCh)
	}
	if doneCh != nil {
		<-doneCh
	}
}

func (s *Store) forceReloadIfPolling() {
	s.mu.Lock()
	active := s.pollRefCount > 0
	s.mu.Unlock()
	if !active {
		return
	}
	_ = s.ForceReload("")
}

func (s *Store) reloadPathLocked(path string, surfaceError bool) (Loaded, error) {
	state, err := loadState(path)
	if err != nil {
		if s.state != nil {
			logReloadError(path, err)
			if surfaceError {
				return s.state.workflow, err
			}
			return s.state.workflow, nil
		}
		return Loaded{}, err
	}
	s.state = state
	return state.workflow, nil
}

func (s *Store) pollLoop(interval time.Duration, stopCh <-chan struct{}, doneCh chan<- struct{}) {
	ticker := time.NewTicker(interval)
	defer func() {
		ticker.Stop()
		close(doneCh)
	}()

	for {
		select {
		case <-ticker.C:
			_, _ = s.Current("")
		case <-stopCh:
			return
		}
	}
}

type Loader struct{}

func NewLoader() Loader {
	return Loader{}
}

func (Loader) Load(path string) (Loaded, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return Loaded{}, &MissingWorkflowFileError{Path: path, Reason: err}
	}

	return Loader{}.Parse(string(content))
}

func (Loader) Parse(content string) (Loaded, error) {
	frontMatterLines, promptLines := splitFrontMatter(content)
	frontMatter, err := frontMatterToMap(frontMatterLines)
	if err != nil {
		if errors.Is(err, ErrWorkflowFrontMatterNotMap) {
			return Loaded{}, ErrWorkflowFrontMatterNotMap
		}

		return Loaded{}, &ParseError{Reason: err}
	}

	prompt := strings.TrimSpace(strings.Join(promptLines, "\n"))

	return Loaded{
		Config:         frontMatter,
		Prompt:         prompt,
		PromptTemplate: prompt,
	}, nil
}

func splitFrontMatter(content string) ([]string, []string) {
	normalizedContent := strings.ReplaceAll(content, "\r\n", "\n")
	normalizedContent = strings.ReplaceAll(normalizedContent, "\r", "\n")

	lines := strings.Split(normalizedContent, "\n")
	if len(lines) == 0 || lines[0] != "---" {
		return nil, lines
	}

	frontMatter := make([]string, 0, len(lines))

	for index := 1; index < len(lines); index++ {
		if lines[index] == "---" {
			return frontMatter, lines[index+1:]
		}

		frontMatter = append(frontMatter, lines[index])
	}

	return frontMatter, []string{}
}

func frontMatterToMap(lines []string) (map[string]any, error) {
	yamlSource := strings.Join(lines, "\n")
	if strings.TrimSpace(yamlSource) == "" {
		return map[string]any{}, nil
	}

	var decoded any
	if err := yaml.Unmarshal([]byte(yamlSource), &decoded); err != nil {
		return nil, err
	}

	normalized := normalizeStringKeyMap(decoded)
	if normalized == nil {
		return nil, ErrWorkflowFrontMatterNotMap
	}

	result, ok := normalized.(map[string]any)
	if !ok {
		return nil, ErrWorkflowFrontMatterNotMap
	}

	return result, nil
}

func normalizeStringKeyMap(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		normalized := make(map[string]any, len(typed))
		for key, nested := range typed {
			normalized[key] = normalizeStringKeyMap(nested)
		}
		return normalized
	case map[any]any:
		normalized := make(map[string]any, len(typed))
		for key, nested := range typed {
			normalized[fmt.Sprint(key)] = normalizeStringKeyMap(nested)
		}
		return normalized
	case []any:
		normalized := make([]any, len(typed))
		for index, nested := range typed {
			normalized[index] = normalizeStringKeyMap(nested)
		}
		return normalized
	default:
		return value
	}
}

func resolvePath(path string) string {
	if strings.TrimSpace(path) != "" {
		return path
	}
	if workflowPath := runtimeconfig.WorkflowFilePath(); strings.TrimSpace(workflowPath) != "" {
		return workflowPath
	}
	return NewPathResolver().Path("")
}

func loadState(path string) (*storeState, error) {
	loaded, err := NewLoader().Load(path)
	if err != nil {
		return nil, err
	}
	stamp, err := currentStamp(path)
	if err != nil {
		return nil, err
	}
	return &storeState{
		path:     path,
		stamp:    stamp,
		workflow: loaded,
	}, nil
}

func currentStamp(path string) (fileStamp, error) {
	info, err := os.Stat(path)
	if err != nil {
		return fileStamp{}, err
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return fileStamp{}, err
	}
	hasher := fnv.New32a()
	_, _ = hasher.Write(content)
	return fileStamp{
		modUnixNano: info.ModTime().UnixNano(),
		size:        info.Size(),
		hash:        hasher.Sum32(),
	}, nil
}

func logReloadError(path string, reason error) {
	log.Printf("Failed to reload workflow path=%s reason=%v; keeping last known good configuration", path, reason)
}
