# REST API

The MCP tools' semantics over HTTP ([tools.md](tools.md)), on the same port (8080).

## Auth

With `oauth.enabled`, every call carries one of:

- the person's Dex ID token as the bearer (the portal, agent-manager): no GitHub login, so private repositories are refused;
- the person's GitHub user token as the bearer and their ID token in `X-Muster-Id-Token` (agent-manager on its GitHub registration, muster on a pinned one): the token's login (`GET /user`) opens the private repositories the person can read;
- on `POST /v1/resolve` only, the service token (`serviceToken`): the platform's own caller, for agent-manager's `migrate`; no readability check.

Anything else is `401`. The ID token is validated before GitHub is asked about a GitHub token; GitHub failing that check (its rate limit included) is `502`. Every refusal is a JSON `{"error": "..."}`.

## Endpoints

| Call | Answer |
|---|---|
| `GET /v1/sources` | `{"sources": [{url, ref, path, readOnly, commit, error}]}`: each ref's commit, cached up to `cacheTTL`; no skills read |
| `GET /v1/skills?source=<url>&refresh=true` | `{"repositories": [{repoUrl, ref, path, readOnly, commit, private, truncated, error, stale, fetchedAt, skills: [{name, description, path, tags}]}]}` |
| `POST /v1/resolve` | the request's items pinned |
| `GET /healthz`, `GET /readyz` | `ok` |

`GET /v1/skills`: `source` is any github.com repository (a configured one keeps its ref and path). A repository that cannot be read is still `200`, with `error` on that repository; with a good listing before, `stale: true` and that listing. A failed read is retried after 30 seconds.

## `POST /v1/resolve`

```json
{"skills": [{"name": "x", "path": "skills/x", "git": {"url": "https://github.com/o/r", "ref": "v1"}},
            {"name": "y", "oci": {"ref": "reg/repo:tag"}}],
 "plugins": [{"path": "plugins/p", "skills": ["a"], "claudeFormat": true, "git": {"url": "https://github.com/o/r"}}]}
```

- A git item is pinned to its ref's commit: `git.commit` set, `git.ref` dropped. No ref: the ref of the configured source whose `path` holds the item's `path`, else the default branch. A `commit` instead of a ref is kept.
- An OCI item is pinned to `oci.ref` `<repository>@sha256:<digest>`.
- The person-can-read check runs inside: every git repository must be readable by the caller (public, or the GitHub login has read access). No separate check is needed.
- `path` is cleaned (`./a//b/` is `a/b`, `.` is the root) and returned cleaned; an absolute path or one with `..` is refused.
- The item's `path` must exist at the commit; a given `commit` with no path must exist.
- An OCI item must come from an allowed registry (`oci.registries`).
- The first refused item fails the whole call.
- A git plugin is answered with `claudeFormat` (a `.claude-plugin/plugin.json` and no root `plugin.json`); `claudeFormat: true` in the request refuses one that is not.

Errors: `{"error": "...", "item": <index>, "list": "skills" | "plugins"}`; `item` and `list` name the refused item, `error` is the cause alone.

| Status | When |
|---|---|
| `400` | invalid item or body, unresolvable reference, not a Claude Code plugin when asked |
| `401` | no or invalid bearer |
| `403` | the person cannot read the repository |
| `404` | repository, ref, path or image tag not found |
| `502` | GitHub or the registry failed |

## Changes from agent-manager's in-process pinning

Intended, stricter than agent-manager before:

- The read check applies to every git item, also on `refreshSkills`.
- The path of every git item must exist at its commit; a given commit must exist.
- A git plugin's format is checked on every resolve (`claudeFormat`).
