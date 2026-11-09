# The tools

Generated from the registered tools (`make tools-doc`); through muster every tool is `x_<server>_<name>`. The REST API (`/v1/sources`, `/v1/skills`, `/v1/resolve`) has the same semantics.

| Tool | Kind |
|---|---|
| `get_skill` | read-only |
| `get_skill_file` | read-only |
| `list_skill_sources` | read-only |
| `list_skills` | read-only |
| `resolve_skills` | read-only |

## `get_skill`

```text
Read one skill of the catalogue at a commit: its SKILL.md and its files.

Returns name, description, tags, instructions (the body below the frontmatter) and files (path, size, sha; SKILL.md first, at most 200, filesTruncated past that). A file below a nested SKILL.md belongs to that skill. Only a full commit id, only below a configured source, and a private repository only for a caller who can read it.
```

```json
{
  "properties": {
    "commit": {
      "description": "Full commit id, as list_skills reports it",
      "type": "string"
    },
    "path": {
      "description": "The skill directory; empty is the repository root",
      "type": "string"
    },
    "url": {
      "description": "https://github.com/<owner>/<repo>, a configured source",
      "type": "string"
    }
  },
  "required": [
    "url",
    "commit"
  ],
  "type": "object"
}
```

## `get_skill_file`

```text
Read one file of a skill at a commit.

Returns path, size, sha, binary, truncated, and content for a text file of at most 512 KiB. The file must be one get_skill lists; the same checks as get_skill apply.
```

```json
{
  "properties": {
    "commit": {
      "description": "Full commit id",
      "type": "string"
    },
    "file": {
      "description": "Path relative to the skill directory, e.g. references/style.md",
      "type": "string"
    },
    "path": {
      "description": "The skill directory; empty is the repository root",
      "type": "string"
    },
    "url": {
      "description": "https://github.com/<owner>/<repo>, a configured source",
      "type": "string"
    }
  },
  "required": [
    "url",
    "commit",
    "file"
  ],
  "type": "object"
}
```

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

Per repository: the ref read, the commit it resolved to, the path scanned, readOnly, error, stale; per skill its SKILL.md name, description, repository-relative path and tags; per plugin its name, path and format (agent-plugins: root plugin.json, select its skills by name; claude-code: .claude-plugin/plugin.json, loaded whole). Cached up to the cache TTL; refresh re-reads a listing older than 10 seconds.
```

```json
{
  "properties": {
    "refresh": {
      "description": "Re-read a listing older than 10 seconds instead of serving it from the cache",
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
  "additionalProperties": false,
  "properties": {
    "plugins": {
      "description": "Plugins: {path, skills, git: {url, ref | commit}} or {skills, oci: {ref}}",
      "items": {
        "additionalProperties": false,
        "properties": {
          "claudeFormat": {
            "description": "Plugins: true refuses one that is not a Claude Code plugin",
            "type": "boolean"
          },
          "git": {
            "additionalProperties": false,
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
            "additionalProperties": false,
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
        "additionalProperties": false,
        "properties": {
          "claudeFormat": {
            "description": "Plugins: true refuses one that is not a Claude Code plugin",
            "type": "boolean"
          },
          "git": {
            "additionalProperties": false,
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
            "additionalProperties": false,
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
