# REST API

The MCP tools' semantics over HTTP ([tools.md](tools.md)), on the same port (8080).

## Auth

With `oauth.enabled`, every call carries one of:

- the person's Dex ID token as the bearer (the portal, agent-manager): no GitHub login, so private repositories are refused, unless `githubTokenExchange` is set: then a private repository fetches the person's GitHub App grant from muster's RFC 8693 broker, and its login opens the private repositories the person can read;
- the person's GitHub user token as the bearer and their ID token in `X-Muster-Id-Token` (agent-manager on its GitHub registration, muster on a pinned one): the token's login (`GET /user`) opens the private repositories the person can read;
- on `POST /v1/resolve` only, the service token (`serviceToken`): the platform's own caller, for agent-manager's `migrate`; no readability check.

Anything else is `401`. The ID token is validated before GitHub is asked about a GitHub token; GitHub failing that check (its rate limit included) is `502`. Every refusal is a JSON `{"error": "..."}`.

## Endpoints

| Call | Answer |
|---|---|
| `GET /v1/sources` | `{"sources": [{url, ref, path, readOnly, commit, error}], partial, withheld, grantRequired: {app}, note}`: each ref's commit, cached up to `cacheTTL`; no skills read |
| `GET /v1/skills?source=<url>&refresh=true` | `{"repositories": [{repoUrl, ref, path, readOnly, commit, private, truncated, error, stale, fetchedAt, skills: [{name, description, path, tags}], plugins: [{name, path, format, skills: [{name, description}]}]}], partial, withheld, grantRequired: {app}, note}` |
| `POST /v1/resolve` | the request's items pinned |
| `GET /v1/skills/content?url=<url>&commit=<sha>&path=<dir>` | `{repoUrl, path, commit, name, description, tags, instructions, files: [{path, size, sha}], filesTruncated}` |
| `GET /v1/skills/content/file?url=<url>&commit=<sha>&path=<dir>&file=<file>` | `{path, size, sha, binary, truncated, content}` |
| `GET /healthz`, `GET /readyz` | `ok` |

`GET /v1/skills`: `source` is any github.com repository (a configured one keeps its ref and path); one that does not exist and one you cannot read get the same `error`. A repository that cannot be read is still `200`, with `error` on that repository; with a good listing before, `stale: true` and that listing. A failed read is retried after 30 seconds; `refresh` re-reads a listing older than 10 seconds.

A plugin is a directory with a root `plugin.json` (`format: agent-plugins`, an Agent Plugins package; `skills` are the skills directly under its `skills/`, which an agent selects by name) or a `.claude-plugin/plugin.json` (`format: claude-code`, loaded whole; `skills` is empty). A directory with both is `agent-plugins`. A hidden directory (`.claude-plugin`, `.codex-plugin`, `.cursor-plugin`) is its parent's manifest, never a package.

A configured private repository the caller cannot read, or cannot yet (no GitHub grant), is left out of both listings, never named: `partial: true` and `withheld` count them, `grantRequired.app` names the provider App to grant when a missing grant left any out, and `note` says the same in words. A listing never answers a missing grant with `401`.

`GET /v1/skills/content` and `/content/file` read one skill at a full commit id (`400` otherwise), below a configured source (`400` otherwise), a private repository only for a caller who can read it (`403`; without the person's GitHub grant, `401` with `grantRequired: {app}`, and the MCP tool answers `{"status": "grant_required", "apps": [app]}`). `instructions` is the SKILL.md body below the frontmatter. `files` lists the skill directory, SKILL.md first, at most 200 (`filesTruncated`); a file below a nested SKILL.md is that skill's. A file must be one of the skill's files, listed or past the cap (`404` otherwise); `content` is set only for a UTF-8 text file of at most 512 KiB (`binary` by extension, a NUL byte in the first 8000 bytes or invalid UTF-8; `truncated` past the size). At most four reads go to GitHub at once; a read that waits more than 30 seconds is a `502`. A skill at a commit is cached, a missing one for 5 minutes; a file is read from GitHub on each call.

## `POST /v1/resolve`

```json
{"skills": [{"name": "x", "path": "skills/x", "git": {"url": "https://github.com/o/r", "ref": "v1"}},
            {"name": "y", "oci": {"ref": "reg/repo:tag"}}],
 "plugins": [{"path": "plugins/p", "skills": ["a"], "claudeFormat": true, "git": {"url": "https://github.com/o/r"}}]}
```

- A git item is pinned to its ref's commit: `git.commit` set, `git.ref` dropped. No ref: the ref of the configured source whose `path` holds the item's `path`, else the default branch. A `commit` instead of a ref is kept.
- An OCI item is pinned to `oci.ref` `<repository>@sha256:<digest>`; a given digest is kept, its tag dropped.
- The person-can-read check runs inside: every git repository must be readable by the caller (public, or the GitHub login has read access). No separate check is needed.
- `path` is cleaned (`./a//b/` is `a/b`, `.` is the root) and returned cleaned; an absolute path or one with `..` is refused.
- The item's `path` must exist at the commit; a commit, given or as the ref, must exist.
- An OCI item must come from an allowed registry (`oci.registries`), also one given by digest.
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
- An OCI digest must come from an allowed registry.
- A git plugin's format is checked on every resolve (`claudeFormat`).
