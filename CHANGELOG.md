# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).



## [Unreleased]

### Added

- skill-manager v0: the skill sources (`sources: [{url, ref, path, readOnly}]`), their skills and the pins agents mount them by, as the MCP tools `list_skill_sources`, `list_skills` and `resolve_skills` and the REST API `GET /v1/sources`, `GET /v1/skills` and `POST /v1/resolve`. Discovery, pinning and the GitHub App token source moved from agent-manager (giantswarm/agent-manager#290). ([giantswarm#38970](https://github.com/giantswarm/giantswarm/issues/38970), [giantswarm#38322](https://github.com/giantswarm/giantswarm/issues/38322))


[Unreleased]: https://github.com/giantswarm/skill-manager/tree/main
