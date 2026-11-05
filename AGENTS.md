# AGENTS.md

## Origin

skill-manager's skill discovery, pinning and GitHub App token source moved from agent-manager's `internal/skills` (giantswarm/agent-manager#290). agent-manager is its client: `POST /v1/resolve` and `GET /v1/skills` ([docs/api.md](docs/api.md)) are the interface between them; a change to either comes with the client's change in agent-manager.

## Platform skills

- The Agent Platform's skills in [giantswarm/agent-platform `skills/`](https://github.com/giantswarm/agent-platform/tree/main/skills) describe skill-manager's MCP tools. A change that alters what an agent or a person can do, see or configure through it comes with a pull request there updating the affected skills, linked from this one.
