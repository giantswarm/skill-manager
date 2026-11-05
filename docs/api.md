# REST API

The MCP tools' semantics over HTTP ([tools.md](tools.md)), on the same port (8080).

## Auth

With `oauth.enabled`, every call carries one of:

- the person's Dex ID token as the bearer (the portal, agent-manager): no GitHub login, so private repositories are refused;
- the person's GitHub user token as the bearer and their ID token in `X-Muster-Id-Token` (agent-manager on its GitHub registration, muster on a pinned one): the token's login (`GET /user`) opens the private repositories the person can read;
- the service token (`serviceToken`): the platform's own caller, for agent-manager's `migrate`; no readability check.

Anything else is `401`.

## Endpoints

| Call | Answer |
|---|---|
| `GET /v1/sources` | `{"sources": [{url, ref, path, readOnly, commit, error}]}` |
| `GET /v1/skills?source=<url>&refresh=true` | `{"repositories": [{repoUrl, ref, path, readOnly, commit, private, truncated, error, fetchedAt, skills: [{name, description, path, tags}]}]}` |
| `POST /v1/resolve` | the request's items pinned |
| `GET /healthz`, `GET /readyz` | `ok` |

## `POST /v1/resolve`

```json
{"skills": [{"name": "x", "path": "skills/x", "git": {"url": "https://github.com/o/r", "ref": "v1"}},
            {"name": "y", "oci": {"ref": "reg/repo:tag"}}],
 "plugins": [{"path": "plugins/p", "skills": ["a"], "claudeFormat": true, "git": {"url": "https://github.com/o/r"}}]}
```

- A git item is pinned to its ref's commit: `git.commit` set, `git.ref` dropped. No ref: the ref of the configured source whose `path` holds the item's `path`, else the default branch. A `commit` instead of a ref is kept.
- An OCI item is pinned to `oci.ref` `<repository>@sha256:<digest>`.
- The person-can-read check runs inside: every git repository must be readable by the caller (public, or the GitHub login has read access). No separate check is needed.
- The item's `path` must exist at the commit.
- A git plugin is answered with `claudeFormat` (a `.claude-plugin/plugin.json` and no root `plugin.json`); `claudeFormat: true` in the request refuses one that is not.

Errors: `{"error": "...", "item": <index>, "list": "skills" | "plugins"}`.

| Status | When |
|---|---|
| `400` | invalid item or body, unresolvable reference, not a Claude Code plugin when asked |
| `401` | no or invalid bearer |
| `403` | the person cannot read the repository |
| `404` | repository, ref, path or image tag not found |
| `502` | GitHub or the registry failed |
