# skill-manager

Owns the Agent Platform's skill sources: lists their skills and pins skills and plugins to commits and digests, for the person asking. One MCP server behind muster and a REST API for agent-manager and the portal.

- Tools: [docs/tools.md](docs/tools.md)
- REST API: [docs/api.md](docs/api.md)
- Configuration: the chart's [values](helm/skill-manager/values.yaml) — `sources`, `github`, `cacheTTL`, `oauth`, `muster.mcpServer`, `serviceToken`.
