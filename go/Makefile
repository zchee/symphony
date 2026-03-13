GO ?= go

.PHONY: test vet build e2e

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

build:
	$(GO) build ./cmd/symphony

e2e:
	@if [ -z "$$LINEAR_API_KEY" ]; then echo "LINEAR_API_KEY is required for live e2e"; exit 1; fi
	@if ! command -v codex >/dev/null 2>&1; then echo "codex is required for live e2e"; exit 1; fi
	SYMPHONY_RUN_LIVE_E2E=1 $(GO) test ./agent -run 'TestLiveE2E(Local|SSH)' -count=1
