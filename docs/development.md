# Developing on skill-manager

- `go test -race ./...`, `make lint`, `make pre-commit` (the CI hooks with CI's golangci-lint).
- `make helm-test`: helm lint and the helm-unittest suites in `helm/skill-manager/tests/`.
- `make tools-doc` after changing a tool: regenerates [tools.md](tools.md).
- `go run . serve --sources=https://github.com/giantswarm/agent-skills`: anonymous, no OAuth, on `:8080`.
