package workflow

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/openai/symphony/go/runtimeconfig"
)

func TestNewPathResolver(t *testing.T) {
	t.Parallel()

	resolver := NewPathResolver()
	if resolver == nil {
		t.Fatal("NewPathResolver() returned nil")
	}
}

func TestPathResolverPathDefaultsToWorkflowInProvidedDirectory(t *testing.T) {
	t.Parallel()

	resolver := NewPathResolver()
	cwd := filepath.Join(string(filepath.Separator), "tmp", "symphony")

	if got, want := resolver.Path(cwd), filepath.Join(cwd, FileName); got != want {
		t.Fatalf("resolver.Path(%q) = %q, want %q", cwd, got, want)
	}
}

func TestPathResolverPathFallsBackToProcessWorkingDirectory(t *testing.T) {
	t.Parallel()

	resolver := NewPathResolver()
	expectedWorkingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd() failed: %v", err)
	}

	if got, want := resolver.Path(""), filepath.Join(expectedWorkingDirectory, FileName); got != want {
		t.Fatalf("resolver.Path(\"\") = %q, want %q", got, want)
	}
}

func TestPathResolverSetOverridesDefaultPath(t *testing.T) {
	t.Parallel()

	resolver := NewPathResolver()
	explicitPath := filepath.Join(string(filepath.Separator), "var", "tmp", "custom", FileName)

	resolver.Set(explicitPath)

	if got := resolver.Path(filepath.Join(string(filepath.Separator), "tmp", "ignored")); got != explicitPath {
		t.Fatalf("resolver.Path() = %q, want explicit path %q", got, explicitPath)
	}
}

func TestPathResolverClearRestoresDefaultPath(t *testing.T) {
	t.Parallel()

	resolver := NewPathResolver()
	resolver.Set(filepath.Join(string(filepath.Separator), "tmp", "custom", FileName))
	resolver.Clear()

	cwd := filepath.Join(string(filepath.Separator), "tmp", "restored")
	if got, want := resolver.Path(cwd), filepath.Join(cwd, FileName); got != want {
		t.Fatalf("resolver.Path(%q) after Clear() = %q, want %q", cwd, got, want)
	}
}

func TestNewLoader(t *testing.T) {
	t.Parallel()

	loader := NewLoader()
	if (loader == Loader{}) != true {
		t.Fatal("NewLoader() returned an unexpected value")
	}
}

func TestLoaderLoadReturnsMissingWorkflowFileError(t *testing.T) {
	t.Parallel()

	loader := NewLoader()
	missingPath := filepath.Join(t.TempDir(), "MISSING_WORKFLOW.md")

	_, err := loader.Load(missingPath)
	if err == nil {
		t.Fatal("loader.Load() succeeded for a missing file, want error")
	}

	var missingErr *MissingWorkflowFileError
	if !errors.As(err, &missingErr) {
		t.Fatalf("loader.Load() error = %T, want *MissingWorkflowFileError", err)
	}

	if missingErr.Path != missingPath {
		t.Fatalf("missingErr.Path = %q, want %q", missingErr.Path, missingPath)
	}

	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("errors.Is(err, os.ErrNotExist) = false, want true")
	}
}

func TestLoaderLoadReadsPromptOnlyWorkflowFile(t *testing.T) {
	t.Parallel()

	loader := NewLoader()
	path := filepath.Join("..", "testdata", "workflow", "prompt_only.md")

	loaded, err := loader.Load(path)
	if err != nil {
		t.Fatalf("loader.Load(%q) failed: %v", path, err)
	}

	if got, want := loaded.Config, map[string]any{}; len(got) != len(want) {
		t.Fatalf("len(loaded.Config) = %d, want %d", len(got), len(want))
	}

	if got, want := loaded.Prompt, "Prompt only"; got != want {
		t.Fatalf("loaded.Prompt = %q, want %q", got, want)
	}

	if got, want := loaded.PromptTemplate, "Prompt only"; got != want {
		t.Fatalf("loaded.PromptTemplate = %q, want %q", got, want)
	}
}

func TestLoaderParsePromptOnlyWorkflow(t *testing.T) {
	t.Parallel()

	loader := NewLoader()

	loaded, err := loader.Parse("Prompt only\n")
	if err != nil {
		t.Fatalf("loader.Parse(prompt only) failed: %v", err)
	}

	if got := loaded.Config; len(got) != 0 {
		t.Fatalf("len(loaded.Config) = %d, want 0", len(got))
	}

	if got, want := loaded.Prompt, "Prompt only"; got != want {
		t.Fatalf("loaded.Prompt = %q, want %q", got, want)
	}
}

func TestLoaderParseUnterminatedFrontMatter(t *testing.T) {
	t.Parallel()

	loader := NewLoader()

	loaded, err := loader.Parse("---\ntracker:\n  kind: linear\n")
	if err != nil {
		t.Fatalf("loader.Parse(unterminated front matter) failed: %v", err)
	}

	tracker, ok := loaded.Config["tracker"].(map[string]any)
	if !ok {
		t.Fatalf("loaded.Config[\"tracker\"] = %#v, want map[string]any", loaded.Config["tracker"])
	}

	if got, want := tracker["kind"], "linear"; got != want {
		t.Fatalf("tracker.kind = %#v, want %#v", got, want)
	}

	if got := loaded.Prompt; got != "" {
		t.Fatalf("loaded.Prompt = %q, want empty string", got)
	}
}

func TestLoaderParseRejectsNonMapFrontMatter(t *testing.T) {
	t.Parallel()

	loader := NewLoader()

	_, err := loader.Parse("---\n- not-a-map\n---\nPrompt body\n")
	if !errors.Is(err, ErrWorkflowFrontMatterNotMap) {
		t.Fatalf("loader.Parse(non-map front matter) error = %v, want ErrWorkflowFrontMatterNotMap", err)
	}
}

func TestLoaderParseWrapsYamlErrors(t *testing.T) {
	t.Parallel()

	loader := NewLoader()

	_, err := loader.Parse("---\ntracker: [\n---\nBroken prompt\n")
	if err == nil {
		t.Fatal("loader.Parse(broken yaml) succeeded, want error")
	}

	var parseErr *ParseError
	if !errors.As(err, &parseErr) {
		t.Fatalf("loader.Parse(broken yaml) error = %T, want *ParseError", err)
	}
}

func TestSplitFrontMatterWithoutFrontMatter(t *testing.T) {
	t.Parallel()

	frontMatter, prompt := splitFrontMatter("Prompt only\n")
	if frontMatter != nil {
		t.Fatalf("frontMatter = %#v, want nil", frontMatter)
	}

	if len(prompt) != 2 || prompt[0] != "Prompt only" || prompt[1] != "" {
		t.Fatalf("prompt = %#v, want [\"Prompt only\", \"\"]", prompt)
	}
}

func TestSplitFrontMatterWithClosedFrontMatter(t *testing.T) {
	t.Parallel()

	frontMatter, prompt := splitFrontMatter("---\ntracker:\n  kind: linear\n---\nPrompt body\n")
	if len(frontMatter) != 2 {
		t.Fatalf("len(frontMatter) = %d, want 2", len(frontMatter))
	}

	if frontMatter[0] != "tracker:" || frontMatter[1] != "  kind: linear" {
		t.Fatalf("frontMatter = %#v, want tracker map lines", frontMatter)
	}

	if len(prompt) != 2 || prompt[0] != "Prompt body" || prompt[1] != "" {
		t.Fatalf("prompt = %#v, want [\"Prompt body\", \"\"]", prompt)
	}
}

func TestSplitFrontMatterWithUnterminatedFrontMatter(t *testing.T) {
	t.Parallel()

	frontMatter, prompt := splitFrontMatter("---\ntracker:\n  kind: linear\n")
	if len(frontMatter) != 3 {
		t.Fatalf("len(frontMatter) = %d, want 3", len(frontMatter))
	}

	if len(prompt) != 0 {
		t.Fatalf("prompt = %#v, want empty slice", prompt)
	}
}

func TestFrontMatterToMapBlankReturnsEmptyMap(t *testing.T) {
	t.Parallel()

	config, err := frontMatterToMap(nil)
	if err != nil {
		t.Fatalf("frontMatterToMap(nil) failed: %v", err)
	}

	if len(config) != 0 {
		t.Fatalf("len(config) = %d, want 0", len(config))
	}
}

func TestFrontMatterToMapParsesNestedMap(t *testing.T) {
	t.Parallel()

	config, err := frontMatterToMap([]string{"tracker:", "  kind: linear"})
	if err != nil {
		t.Fatalf("frontMatterToMap(valid map) failed: %v", err)
	}

	tracker, ok := config["tracker"].(map[string]any)
	if !ok {
		t.Fatalf("config[\"tracker\"] = %#v, want map[string]any", config["tracker"])
	}

	if got, want := tracker["kind"], "linear"; got != want {
		t.Fatalf("tracker.kind = %#v, want %#v", got, want)
	}
}

func TestStoreCurrentReloadsWhenWorkflowChanges(t *testing.T) {
	t.Parallel()

	store := NewStore()
	path := filepath.Join(t.TempDir(), FileName)

	if err := os.WriteFile(path, []byte("---\ntracker:\n  kind: linear\n---\nPrompt one\n"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(%q) failed: %v", path, err)
	}

	loaded, err := store.Current(path)
	if err != nil {
		t.Fatalf("store.Current(valid workflow) failed: %v", err)
	}
	if got, want := loaded.Prompt, "Prompt one"; got != want {
		t.Fatalf("loaded.Prompt = %q, want %q", got, want)
	}

	if err := os.WriteFile(path, []byte("---\ntracker:\n  kind: linear\n---\nPrompt two\n"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(%q) rewrite failed: %v", path, err)
	}

	reloaded, err := store.Current(path)
	if err != nil {
		t.Fatalf("store.Current(rewritten workflow) failed: %v", err)
	}
	if got, want := reloaded.Prompt, "Prompt two"; got != want {
		t.Fatalf("reloaded.Prompt = %q, want %q", got, want)
	}
}

func TestStoreCurrentKeepsLastKnownGoodOnReloadError(t *testing.T) {
	t.Parallel()

	store := NewStore()
	path := filepath.Join(t.TempDir(), FileName)

	if err := os.WriteFile(path, []byte("---\ntracker:\n  kind: linear\n---\nPrompt one\n"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(%q) failed: %v", path, err)
	}

	loaded, err := store.Current(path)
	if err != nil {
		t.Fatalf("store.Current(valid workflow) failed: %v", err)
	}
	if got, want := loaded.Prompt, "Prompt one"; got != want {
		t.Fatalf("loaded.Prompt = %q, want %q", got, want)
	}

	if err := os.WriteFile(path, []byte("---\ntracker: [\n---\nBroken\n"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(%q) invalid rewrite failed: %v", path, err)
	}

	reloaded, err := store.Current(path)
	if err != nil {
		t.Fatalf("store.Current(invalid rewrite) error = %v, want nil with last known good", err)
	}
	if got, want := reloaded.Prompt, "Prompt one"; got != want {
		t.Fatalf("reloaded.Prompt = %q, want last known good %q", got, want)
	}
}

func TestStoreForceReloadReturnsErrorAndPreservesLastKnownGood(t *testing.T) {
	t.Parallel()

	store := NewStore()
	path := filepath.Join(t.TempDir(), FileName)

	if err := os.WriteFile(path, []byte("---\ntracker:\n  kind: linear\n---\nPrompt one\n"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(%q) failed: %v", path, err)
	}

	if _, err := store.Current(path); err != nil {
		t.Fatalf("store.Current(valid workflow) failed: %v", err)
	}

	if err := os.WriteFile(path, []byte("---\ntracker: [\n---\nBroken\n"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(%q) invalid rewrite failed: %v", path, err)
	}

	if err := store.ForceReload(path); err == nil {
		t.Fatal("store.ForceReload(invalid rewrite) = nil, want error")
	}

	loaded, err := store.Current(path)
	if err != nil {
		t.Fatalf("store.Current(after failed force reload) error = %v, want nil", err)
	}
	if got, want := loaded.Prompt, "Prompt one"; got != want {
		t.Fatalf("loaded.Prompt = %q, want last known good %q", got, want)
	}
}

func TestStoreStartPollingReloadsChangesWithoutExplicitCurrentCall(t *testing.T) {
	store := NewStore()
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)

	if err := os.WriteFile(path, []byte("---\ntracker:\n  kind: linear\n---\nPrompt one\n"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(%q) failed: %v", path, err)
	}
	if err := runtimeconfig.SetWorkflowFilePath(path); err != nil {
		t.Fatalf("runtimeconfig.SetWorkflowFilePath(%q) failed: %v", path, err)
	}
	if err := store.StartPolling(10 * time.Millisecond); err != nil {
		t.Fatalf("store.StartPolling() failed: %v", err)
	}
	defer store.StopPolling()

	if err := os.WriteFile(path, []byte("---\ntracker:\n  kind: linear\n---\nPrompt two\n"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(%q) rewrite failed: %v", path, err)
	}

	waitForWorkflowStore(t, 500*time.Millisecond, func() bool {
		store.mu.Lock()
		defer store.mu.Unlock()
		return store.state != nil && store.state.workflow.Prompt == "Prompt two"
	})
}

func TestStorePollingKeepsLastKnownGoodOnBrokenRewrite(t *testing.T) {
	store := NewStore()
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)

	if err := os.WriteFile(path, []byte("---\ntracker:\n  kind: linear\n---\nPrompt one\n"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(%q) failed: %v", path, err)
	}
	if err := runtimeconfig.SetWorkflowFilePath(path); err != nil {
		t.Fatalf("runtimeconfig.SetWorkflowFilePath(%q) failed: %v", path, err)
	}
	if _, err := store.Current(path); err != nil {
		t.Fatalf("store.Current(valid workflow) failed: %v", err)
	}
	if err := store.StartPolling(10 * time.Millisecond); err != nil {
		t.Fatalf("store.StartPolling() failed: %v", err)
	}
	defer store.StopPolling()

	if err := os.WriteFile(path, []byte("---\ntracker: [\n---\nBroken\n"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(%q) invalid rewrite failed: %v", path, err)
	}

	time.Sleep(50 * time.Millisecond)

	store.mu.Lock()
	defer store.mu.Unlock()
	if store.state == nil || store.state.workflow.Prompt != "Prompt one" {
		t.Fatalf("store.state = %#v, want last known good Prompt one", store.state)
	}
}

func TestStoreStartPollingReferenceCounts(t *testing.T) {
	store := NewStore()
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)

	if err := os.WriteFile(path, []byte("---\ntracker:\n  kind: linear\n---\nPrompt one\n"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(%q) failed: %v", path, err)
	}
	if err := runtimeconfig.SetWorkflowFilePath(path); err != nil {
		t.Fatalf("runtimeconfig.SetWorkflowFilePath(%q) failed: %v", path, err)
	}
	if err := store.StartPolling(10 * time.Millisecond); err != nil {
		t.Fatalf("first StartPolling() failed: %v", err)
	}
	if err := store.StartPolling(10 * time.Millisecond); err != nil {
		t.Fatalf("second StartPolling() failed: %v", err)
	}

	store.mu.Lock()
	if store.pollRefCount != 2 {
		t.Fatalf("store.pollRefCount = %d, want 2", store.pollRefCount)
	}
	store.mu.Unlock()

	store.StopPolling()
	store.mu.Lock()
	if store.pollRefCount != 1 {
		t.Fatalf("store.pollRefCount after first StopPolling = %d, want 1", store.pollRefCount)
	}
	store.mu.Unlock()

	store.StopPolling()
	store.mu.Lock()
	if store.pollRefCount != 0 || store.pollStop != nil || store.pollDone != nil {
		t.Fatalf("poller state after final StopPolling = %#v", store)
	}
	store.mu.Unlock()
}

func TestDefaultStoreReloadsImmediatelyWhenWorkflowPathChangesDuringPolling(t *testing.T) {
	ResetDefaultStoreForTest()
	defer ResetDefaultStoreForTest()

	dir := t.TempDir()
	firstPath := filepath.Join(dir, "FIRST_WORKFLOW.md")
	secondPath := filepath.Join(dir, "SECOND_WORKFLOW.md")

	if err := os.WriteFile(firstPath, []byte("---\ntracker:\n  kind: linear\n---\nPrompt one\n"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(%q) failed: %v", firstPath, err)
	}
	if err := os.WriteFile(secondPath, []byte("---\ntracker:\n  kind: linear\n---\nPrompt two\n"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(%q) failed: %v", secondPath, err)
	}

	if err := runtimeconfig.SetWorkflowFilePath(firstPath); err != nil {
		t.Fatalf("runtimeconfig.SetWorkflowFilePath(%q) failed: %v", firstPath, err)
	}
	if err := StartDefaultPolling(); err != nil {
		t.Fatalf("StartDefaultPolling() failed: %v", err)
	}
	defer StopDefaultPolling()

	if err := runtimeconfig.SetWorkflowFilePath(secondPath); err != nil {
		t.Fatalf("runtimeconfig.SetWorkflowFilePath(%q) failed: %v", secondPath, err)
	}

	defaultStoreMu.RLock()
	store := defaultStore
	defaultStoreMu.RUnlock()
	store.mu.Lock()
	defer store.mu.Unlock()

	if store.state == nil {
		t.Fatal("default store state = nil, want reloaded state")
	}
	if store.state.path != secondPath {
		t.Fatalf("store.state.path = %q, want %q", store.state.path, secondPath)
	}
	if store.state.workflow.Prompt != "Prompt two" {
		t.Fatalf("store.state.workflow.Prompt = %q, want %q", store.state.workflow.Prompt, "Prompt two")
	}
}

func waitForWorkflowStore(t *testing.T, timeout time.Duration, predicate func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if predicate() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for workflow store condition")
}

func TestFrontMatterToMapRejectsNonMap(t *testing.T) {
	t.Parallel()

	_, err := frontMatterToMap([]string{"- not-a-map"})
	if !errors.Is(err, ErrWorkflowFrontMatterNotMap) {
		t.Fatalf("frontMatterToMap(non-map) error = %v, want ErrWorkflowFrontMatterNotMap", err)
	}
}

func TestNormalizeStringKeyMap(t *testing.T) {
	t.Parallel()

	input := map[any]any{
		"tracker": map[any]any{
			"states": []any{"Todo", "In Progress"},
		},
	}

	normalized, ok := normalizeStringKeyMap(input).(map[string]any)
	if !ok {
		t.Fatalf("normalizeStringKeyMap(input) = %#v, want map[string]any", normalized)
	}

	tracker, ok := normalized["tracker"].(map[string]any)
	if !ok {
		t.Fatalf("normalized[\"tracker\"] = %#v, want map[string]any", normalized["tracker"])
	}

	states, ok := tracker["states"].([]any)
	if !ok {
		t.Fatalf("tracker[\"states\"] = %#v, want []any", tracker["states"])
	}

	if len(states) != 2 || states[0] != "Todo" || states[1] != "In Progress" {
		t.Fatalf("states = %#v, want [\"Todo\", \"In Progress\"]", states)
	}
}
