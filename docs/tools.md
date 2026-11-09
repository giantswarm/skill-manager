# The tools

Generated from the registered tools (`make tools-doc`); through muster every tool is `x_<server>_<name>`. The REST API (`/v1/sources`, `/v1/skills`, `/v1/resolve`) has the same semantics.

| Tool | Kind |
|---|---|
| `list_skill_sources` | read-only |
| `list_skills` | read-only |
| `resolve_skills` | read-only |

## `list_skill_sources`

```text
List the configured skill sources, each with the commit its ref resolves to.

Per source: url, ref (empty: the default branch), path scanned, readOnly, commit (cached up to the cache TTL); error when it cannot be read or you cannot read it.
```

```json
{
  "properties": {},
  "required": [],
  "type": "object"
}
```

## `list_skills`

```text
List the skills of the configured skill sources, with each source's commit.

Per repository: the ref read, the commit it resolved to, the path scanned, readOnly, error, stale; per skill its SKILL.md name, description, repository-relative path and tags. Cached up to the cache TTL; refresh re-reads a listing older than 10 seconds.
```

```json
{
  "properties": {
    "refresh": {
      "description": "Bypass the cache",
      "type": "boolean"
    },
    "source": {
      "description": "Only this repository, any https://github.com/<owner>/<repo>; a configured one keeps its ref and path. Default: every source",
      "type": "string"
    }
  },
  "required": [],
  "type": "object"
}
```

## `resolve_skills`

```text
Pin skills and plugins: a git ref to its commit, an OCI tag to its digest.

Returns the items with git.commit set and git.ref dropped, or oci.ref by digest; a git plugin also gets claudeFormat. No ref: the ref of the source holding the path. The first refused item fails the call, named skills[i] or plugins[i]: a repository you cannot read, a missing path or commit, a non-Claude plugin asking claudeFormat.
```

```json
{
  "properties": {
    "plugins": {
      "description": "Plugins: {path, skills, git: {url, ref | commit}} or {skills, oci: {ref}}",
      "items": {
        "properties": {
          "claudeFormat": {
            "description": "Plugins: true refuses one that is not a Claude Code plugin",
            "type": "boolean"
          },
          "git": {
            "properties": {
              "commit": {
                "description": "Full commit id to keep instead of a ref",
                "type": "string"
              },
              "ref": {
                "description": "Branch, tag or commit; empty: the ref of the source holding path, else the default branch",
                "type": "string"
              },
              "url": {
                "description": "https://github.com/<owner>/<repo>",
                "type": "string"
              }
            },
            "required": [
              "url"
            ],
            "type": "object"
          },
          "name": {
            "type": "string"
          },
          "oci": {
            "properties": {
              "ref": {
                "description": "<registry>/<repository>[:<tag>|@sha256:<digest>]",
                "type": "string"
              }
            },
            "required": [
              "ref"
            ],
            "type": "object"
          },
          "path": {
            "description": "Directory in the repository; empty is its root",
            "type": "string"
          },
          "skills": {
            "description": "A plugin's skill selection, passed through",
            "items": {
              "type": "string"
            },
            "type": "array"
          }
        },
        "type": "object"
      },
      "type": "array"
    },
    "skills": {
      "description": "Skills: {name, path, git: {url, ref | commit}} or {name, oci: {ref}}",
      "items": {
        "properties": {
          "claudeFormat": {
            "description": "Plugins: true refuses one that is not a Claude Code plugin",
            "type": "boolean"
          },
          "git": {
            "properties": {
              "commit": {
                "description": "Full commit id to keep instead of a ref",
                "type": "string"
              },
              "ref": {
                "description": "Branch, tag or commit; empty: the ref of the source holding path, else the default branch",
                "type": "string"
              },
              "url": {
                "description": "https://github.com/<owner>/<repo>",
                "type": "string"
              }
            },
            "required": [
              "url"
            ],
            "type": "object"
          },
          "name": {
            "type": "string"
          },
          "oci": {
            "properties": {
              "ref": {
                "description": "<registry>/<repository>[:<tag>|@sha256:<digest>]",
                "type": "string"
              }
            },
            "required": [
              "ref"
            ],
            "type": "object"
          },
          "path": {
            "description": "Directory in the repository; empty is its root",
            "type": "string"
          },
          "skills": {
            "description": "A plugin's skill selection, passed through",
            "items": {
              "type": "string"
            },
            "type": "array"
          }
        },
        "type": "object"
      },
      "type": "array"
    }
  },
  "required": [],
  "type": "object"
}
```
