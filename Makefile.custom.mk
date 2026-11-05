##@ Development

BINARY := skill-manager

.PHONY: build-linux-amd64
build-linux-amd64: ## Build the linux/amd64 binary the Dockerfile expects; version and commit come from the Go build info (the tag at HEAD, else dev).
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o $(BINARY)-linux-amd64 .

.PHONY: docker-build
docker-build: build-linux-amd64 ## Build a local dev image (TAG=skill-manager:dev).
	docker build --build-arg TARGETOS=linux --build-arg TARGETARCH=amd64 -t $(or $(TAG),skill-manager:dev) .

.PHONY: tools-doc
tools-doc: ## Regenerate docs/tools.md from the registered tools (the test TestToolsDocIsCurrent keeps it current).
	TOOLS_DOC_UPDATE=1 go test -count=1 -run TestToolsDocIsCurrent ./internal/api

##@ Lint

# One golangci-lint for CI and every local run. The version is read from the
# pre-commit workflow's install step (the one CI runs, which Renovate bumps), so
# there is no second pin to drift. The release binary lands in .bin/, which
# leads PATH for every recipe: `make lint` and the pre-commit golangci-lint hook
# (`make pre-commit`) then run the same binary CI does.
GOLANGCI_LINT_WORKFLOW := .github/workflows/zz_generated.pre-commit.yaml
GOLANGCI_LINT_VERSION := $(shell sed -n '/binary: golangci-lint/{n;s/.*version: "\([^"]*\)".*/\1/p;}' $(GOLANGCI_LINT_WORKFLOW))
LOCAL_BIN := $(CURDIR)/.bin
GOLANGCI_LINT_BIN := $(LOCAL_BIN)/golangci-lint-$(GOLANGCI_LINT_VERSION)
export PATH := $(LOCAL_BIN):$(PATH)

$(GOLANGCI_LINT_BIN):
	@test -n "$(GOLANGCI_LINT_VERSION)" || { echo "no golangci-lint version in $(GOLANGCI_LINT_WORKFLOW)" >&2; exit 1; }
	@mkdir -p $(LOCAL_BIN)
	@tmp=$$(mktemp -d) && os=$$(uname -s | tr A-Z a-z) && arch=$$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/') \
	  && curl -fsSL https://github.com/golangci/golangci-lint/releases/download/v$(GOLANGCI_LINT_VERSION)/golangci-lint-$(GOLANGCI_LINT_VERSION)-$$os-$$arch.tar.gz | tar -xz -C $$tmp \
	  && mv $$tmp/golangci-lint-$(GOLANGCI_LINT_VERSION)-$$os-$$arch/golangci-lint $@ && rm -rf $$tmp
	@ln -sf $(notdir $@) $(LOCAL_BIN)/golangci-lint

.PHONY: golangci-lint
golangci-lint: $(GOLANGCI_LINT_BIN) ## Install the golangci-lint version CI runs into .bin/.

# The generated lint target runs the pinned binary through PATH.
lint: golangci-lint

.PHONY: pre-commit
pre-commit: golangci-lint ## Run every pre-commit hook on all files, with the CI golangci-lint version.
	pre-commit run --all-files
