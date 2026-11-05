# skill-manager

Owns the Agent Platform's skill sources, lists their skills and pins skills and plugins to commits, as one MCP server behind muster and a REST API

**Homepage:** <https://github.com/giantswarm/skill-manager>

## Source Code

* <https://github.com/giantswarm/skill-manager>

## Values

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| replicaCount | int | `1` | Number of replicas. The server keeps no MCP session state and every replica validates forwarded Dex tokens, so two or more are fine for muster, agent-manager and the portal; each keeps its own discovery cache. Tokens the server issues itself (a client signing in to it directly) live in one replica's memory and fail on another. |
| revisionHistoryLimit | int | `3` | Number of old ReplicaSets retained for rollback. |
| image.registry | string | `"gsoci.azurecr.io"` | Image registry. |
| image.repository | string | `"giantswarm/skill-manager"` | Image repository. |
| image.pullPolicy | string | `"IfNotPresent"` | Image pull policy. |
| image.tag | string | `""` | Image tag. Defaults to the chart appVersion. |
| imagePullSecrets | list | `[]` | Image pull secrets. |
| nameOverride | string | `""` | Override the chart name. |
| fullnameOverride | string | `""` | Override the fully qualified release name. |
| global | object | `{}` |  |
| sources | list | `[]` | The skill sources: each a github.com repository URL or `{url, ref, path, readOnly}`. `ref` is a branch, tag or commit (empty: the default branch): what discovery reads and what a skill of the source without its own ref pins. `path` is the directory scanned (empty: the whole repository). `readOnly` is reported, not enforced. A repository given twice needs paths that do not contain each other. |
| github.apiUrl | string | `"https://api.github.com"` | GitHub API base URL: discovery, pinning, and `GET /user` for a person's GitHub token. |
| github.app.secretName | string | `""` | Existing Secret in the release namespace of the read-only skills GitHub App, with the keys `app-id`, `installation-id` and `private-key` (PEM): skills are discovered and pinned with the App's installation tokens, at the App's rate limit. Mutually exclusive with `tokenSecret`. |
| github.tokenSecret.name | string | `""` | Existing Secret in the release namespace with a static GitHub token. Prefer `app`. Empty (and no `app`): anonymous, public repositories only. |
| github.tokenSecret.key | string | `"token"` | Key of the token in that Secret. |
| cacheTTL | string | `"5m"` | How long a source's discovered skills and head commit are reused. A failed read is retried after 30 seconds and keeps the last good listing. |
| oci.registries | list | `["gsoci.azurecr.io","gsociprivate.azurecr.io"]` | The registries (host or host:port) an OCI skill or plugin may come from. No other registry, and none by IP address, is asked; a token realm must be on the registry or another listed host. |
| serviceToken.secretName | string | `""` | Existing Secret in the release namespace with a bearer the REST API admits as the platform's own caller, no person, for agent-manager's `migrate`; no readability check applies to it. Needs `oauth.enabled`. Empty: none. |
| serviceToken.key | string | `"token"` | Key of the token in that Secret. |
| mcp.path | string | `"/mcp"` | MCP endpoint path. |
| oauth.enabled | bool | `false` | Make skill-manager an OAuth 2.1 resource server (mcp-oauth): the MCP endpoint and the REST API require a bearer token Dex issued, validated against Dex's JWKS when its audience is in `trustedAudiences`. muster forwards the session's Dex id_token (`muster.mcpServer.auth`); agent-manager and the portal forward the person's. A REST call may instead carry the person's GitHub token as the bearer and the id_token in `X-Muster-Id-Token`, as agent-manager forwards them on its GitHub registration: the GitHub login then opens the private repositories the person can read. Off: no authentication at all, and private repositories are refused; only for a server nothing but a trusted proxy can reach. Off by default only because it needs the installation's Dex: the `agent-platform` meta chart turns it on. |
| oauth.baseURL | string | `""` | Public base URL of this server: the issuer of its own OAuth metadata (https, or http on loopback). Empty derives `https://<fullname>.<global.domain>` when `global.domain` is set. |
| oauth.dex.issuerURL | string | `""` | Dex issuer URL. Empty falls back to `global.identity.issuerUrl`. |
| oauth.dex.clientID | string | `""` | Dex OAuth client ID. Empty falls back to `global.identity.clientId`. |
| oauth.dex.clientSecret | string | `""` | Dex OAuth client secret (prefer `oauth.existingSecret`). |
| oauth.dex.allowPrivateURLs | bool | `false` | Let the issuer resolve to a private or loopback address (an in-cluster Dex). |
| oauth.dex.caSecret | object | `{"key":"ca.crt","name":""}` | Secret with the CA of a Dex that serves a private certificate; mounted and passed as the `dex-ca-file` flag. Empty name falls back to `global.identity.ca.secretName` / `global.identity.ca.key`. |
| oauth.existingSecret | string | `""` | Existing Secret with the Dex client secret under `dex-client-secret`. Empty falls back to `global.identity.existingSecret`; without that, the chart renders a Secret from `oauth.dex.clientSecret`. |
| oauth.trustedAudiences | list | `[]` | OAuth client IDs whose Dex id_tokens are accepted as bearer tokens (SSO token forwarding). Empty falls back to `[global.identity.clientId]`, the platform client MCP clients and the muster CLI log in with. The server trusts the union of this list and `muster.mcpServer.auth.requiredAudiences` (in that order, without duplicates): every token muster forwards carries the required audiences by construction and they are what the kube-apiserver trusts, so a portal session — whose id_token carries them but not the platform client — is accepted without listing its client here. |
| oauth.sso.allowPrivateIPs | bool | `false` | Let Dex's JWKS endpoint resolve to a private address when validating forwarded tokens (an in-cluster Dex). |
| oauth.allowPublicClientRegistration | bool | `false` | Accept unauthenticated dynamic client registration (labs only). |
| muster.mcpServer.enabled | bool | `false` | Register this server with muster by rendering an `mcpservers.muster.giantswarm.io` CR in the release namespace. Tools then appear as `x_<name>_<tool>`. |
| muster.mcpServer.name | string | `"skill-manager"` | MCPServer CR name (drives the tool prefix). |
| muster.mcpServer.displayName | string | `"Skill Manager"` | Name the Dev Portal shows for this server, rendered as the MCPServer's `ui.giantswarm.io/display-name` annotation. Empty renders no annotation. |
| muster.mcpServer.autoStart | bool | `true` | Start the server connection when muster initializes. |
| muster.mcpServer.description | string | `"Skill sources of this installation, their skills and the pins agents mount them by, for the Agent Platform"` | Human-readable description shown by muster. |
| muster.mcpServer.labels | object | `{}` | Extra labels on the MCPServer CR. |
| muster.mcpServer.timeout | int | `30` | How long muster waits for a tool's answer, in seconds (the MCPServer's `spec.timeout`, 1 to 300): muster's default. |
| muster.mcpServer.auth | object | `{"forwardToken":true,"github":{"authorizationServer":{"authorizationEndpoint":"https://github.com/login/oauth/authorize","clientCredentialsSecretRef":{"name":"giantswarm-agent-manager-oauth-client","namespace":""},"expectedIssuer":"https://github.com/login/oauth","grantScope":"subject","issuer":"https://github.com/apps/giantswarm-agent-manager","scopes":"","tokenEndpoint":"https://github.com/login/oauth/access_token"},"enabled":false},"requiredAudiences":[]}` | How muster authenticates to this server; rendered only with `oauth.enabled`. `forwardToken` makes muster forward the session's Dex id_token byte-identical (the SSO path this chart trusts through its trusted audiences), unless `github.enabled` pins a GitHub App. `requiredAudiences` are extra audiences that token must carry — the Dex cross-client audience the kube-apiserver trusts (`dex-k8s-authenticator` on Giant Swarm clusters; agentlab's is `kubernetes`); muster requests them at login, so users re-login after a change. They are trusted as bearer audiences by construction, since the server's trusted audiences are `oauth.trustedAudiences` plus this list. |
| muster.mcpServer.auth.github.enabled | bool | `false` | Pin the registration to a GitHub App, as agent-manager's `github.enabled` does: muster runs the App's consent once per person and puts their user token on every call, which this server verifies with `GET /user`; its login opens the private repositories the person can read. `forwardIdentity` puts the Dex ID token in `X-Muster-Id-Token` beside it. Needs `oauth.enabled`. Off: no MCP caller has a GitHub login. |
| muster.mcpServer.auth.github.authorizationServer.issuer | string | `"https://github.com/apps/giantswarm-agent-manager"` | The issuer identity the person's grant is filed under: the App's own. |
| muster.mcpServer.auth.github.authorizationServer.expectedIssuer | string | `"https://github.com/login/oauth"` | The issuer GitHub puts in the RFC 9207 `iss` parameter. Empty omits the field. |
| muster.mcpServer.auth.github.authorizationServer.authorizationEndpoint | string | `"https://github.com/login/oauth/authorize"` | GitHub's authorize endpoint. |
| muster.mcpServer.auth.github.authorizationServer.tokenEndpoint | string | `"https://github.com/login/oauth/access_token"` | GitHub's token endpoint. |
| muster.mcpServer.auth.github.authorizationServer.scopes | string | `""` | The OAuth `scope` parameter, only for an authorization server that needs one. Empty omits the field. |
| muster.mcpServer.auth.github.authorizationServer.clientCredentialsSecretRef.name | string | `"giantswarm-agent-manager-oauth-client"` | Secret with the App's OAuth client under `client-id` and `client-secret`, which muster reads. |
| muster.mcpServer.auth.github.authorizationServer.clientCredentialsSecretRef.namespace | string | `""` | Namespace of that Secret; empty is the release namespace. |
| muster.mcpServer.auth.github.authorizationServer.grantScope | string | `"subject"` | `subject`: the grant belongs to the person. |
| networkPolicy.enabled | bool | `true` | Create a Kubernetes NetworkPolicy for the pod. |
| networkPolicy.ingressNamespaces | list | `[]` | Namespaces allowed to reach the MCP endpoint and the REST API (label kubernetes.io/metadata.name), besides the release namespace. List muster's, agent-manager's and the portal's namespaces when they run elsewhere, or they cannot reach the server. |
| networkPolicy.allowInternet | bool | `true` | Allow egress on 443 to every destination: GitHub, OCI registries and Dex. Vanilla NetworkPolicy cannot select by name; narrow with egressCIDRs instead when the addresses are known. |
| networkPolicy.egressCIDRs | list | `[]` | Extra egress CIDRs. |
| networkPolicy.metricsNamespaces | list | `["kube-system"]` | Namespaces allowed to scrape the metrics port (label kubernetes.io/metadata.name), when `observability.metrics.enabled`. |
| serviceAccount.create | bool | `true` | Create a ServiceAccount. |
| serviceAccount.annotations | object | `{}` | Annotations on the ServiceAccount. |
| serviceAccount.name | string | `""` | ServiceAccount name (generated when empty). |
| podAnnotations | object | `{}` | Annotations on the pod. |
| podLabels | object | `{}` | Labels on the pod. |
| podSecurityContext | object | `{"fsGroup":1000,"runAsGroup":1000,"runAsNonRoot":true,"runAsUser":1000,"seccompProfile":{"type":"RuntimeDefault"}}` | Pod security context. |
| securityContext | object | `{"allowPrivilegeEscalation":false,"capabilities":{"drop":["ALL"]},"readOnlyRootFilesystem":true,"runAsGroup":1000,"runAsNonRoot":true,"runAsUser":1000,"seccompProfile":{"type":"RuntimeDefault"}}` | Container security context. |
| service.type | string | `"ClusterIP"` | Service type. |
| service.port | int | `8080` | Service port (the container listens on 8080). |
| resources | object | `{"limits":{"cpu":"500m","ephemeral-storage":"100Mi","memory":"256Mi"},"requests":{"cpu":"50m","ephemeral-storage":"50Mi","memory":"64Mi"}}` | Container resources. |
| observability.otel.endpoint | string | `""` | OTLP collector URL for traces, e.g. `http://otlp-gateway.kube-system.svc:4317` (gRPC) or `https://tempo.example.com:4318` (with protocol `http/protobuf`). Empty exports nothing; inbound `traceparent` headers still propagate. With `networkPolicy.enabled` the policy opens egress to it: the Service's namespace for an in-cluster host, every address otherwise. |
| observability.otel.protocol | string | `"grpc"` | OTLP protocol: `grpc` or `http/protobuf`. |
| observability.otel.headers | string | `""` | OTLP headers (`OTEL_EXPORTER_OTLP_HEADERS`), e.g. `X-Scope-OrgID=giantswarm` for a multi-tenant collector. |
| observability.otel.resourceAttributes | string | `""` | Resource attributes appended to the downward-API `k8s.pod.name`, `k8s.namespace.name` and `k8s.node.name`. |
| observability.otel.sampler | string | `"parentbased_traceidratio"` | `OTEL_TRACES_SAMPLER`. The parent-based default follows the caller's sampling decision and samples `samplerArg` of the traces that start here. |
| observability.otel.samplerArg | string | `"0.1"` | `OTEL_TRACES_SAMPLER_ARG`. |
| observability.metrics.enabled | bool | `true` | Serve Prometheus metrics on `GET /metrics` of the `metrics` port: `mcp_server_operation_duration_seconds` per tool and `http_server_request_duration_seconds` for the MCP endpoint. Off, `OTEL_METRICS_EXPORTER=none`: nothing is served. |
| observability.metrics.port | int | `9464` | Port of the metrics listener, apart from the MCP port so the OAuth guard never covers it. |
| serviceMonitor.enabled | bool | `false` | Render a ServiceMonitor for the `metrics` port. Also needs `observability.metrics.enabled`. |
| serviceMonitor.interval | string | `""` | Scrape interval; empty uses the Prometheus Operator default. |
| serviceMonitor.labels | object | `{}` | Labels on the ServiceMonitor, beside the chart's own. The Giant Swarm observability platform routes a scrape to a Mimir tenant by `observability.giantswarm.io/tenant`; a monitor without it writes to no tenant. |
| logging.verbose | bool | `false` | Enable debug logging. |
| extraArgs | list | `[]` | Extra container arguments. |
| extraEnv | list | `[]` | Extra environment variables. |
| nodeSelector | object | `{}` | Node selector. |
| tolerations | list | `[]` | Tolerations. |
| affinity | object | `{}` | Affinity. |

## CI values

`ci/ci-values.yaml` holds the values the CI render (`helm template` in app-build-suite) needs beyond the defaults, such as a `required` value without a default.
