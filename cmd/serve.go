package cmd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/giantswarm/mcp-toolkit/metrics"
	"github.com/giantswarm/mcp-toolkit/tracing"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/giantswarm/skill-manager/internal/api"
	"github.com/giantswarm/skill-manager/internal/catalog"
	"github.com/giantswarm/skill-manager/internal/oci"
	"github.com/giantswarm/skill-manager/internal/server"
	"github.com/giantswarm/skill-manager/internal/skills"
	"github.com/giantswarm/skill-manager/internal/tags"
)

type serveOptions struct {
	listen  string
	mcpPath string

	sources       string
	cacheTTL      time.Duration
	githubAPIURL  string
	githubToken   string
	appID         string
	appInstall    string
	appKeyFile    string
	githubPinAuth string
	serviceToken  string
	ociRegistries string
	musterServer  string

	oauthEnabled                  bool
	oauthBaseURL                  string
	dexIssuerURL                  string
	dexClientID                   string
	dexClientSecret               string
	dexCAFile                     string
	dexAllowPrivateIP             bool
	oauthTrustedAudiences         string
	ssoAllowPrivateIPs            bool
	allowPublicClientRegistration bool

	// envErrs are the environment values that did not parse, by the flag
	// they default.
	envErrs map[string]error
	flags   *pflag.FlagSet
}

// envErr fails the start on an environment value that did not parse,
// unless its flag is given: flags win over the environment.
func (o *serveOptions) envErr() error {
	var errs []error
	for flag, err := range o.envErrs {
		if o.flags == nil || !o.flags.Changed(flag) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func newServeCmd() *cobra.Command {
	cmd, _ := newServe()
	return cmd
}

// newServe is the serve command and the options its flags fill.
func newServe() (*cobra.Command, *serveOptions) {
	o := &serveOptions{envErrs: map[string]error{}}
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the MCP server and the REST API",
		Long: `Run the skill-manager server. Every flag can also be set through the
environment variable named next to it; flags win over the environment.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runServe(cmd.Context(), o)
		},
	}
	f := cmd.Flags()
	o.flags = f
	f.StringVar(&o.listen, "listen", envOr("SKILL_MANAGER_LISTEN", ":8080"), "Listen address (SKILL_MANAGER_LISTEN)")
	f.StringVar(&o.mcpPath, "mcp-path", envOr("SKILL_MANAGER_MCP_PATH", "/mcp"), "MCP endpoint path (SKILL_MANAGER_MCP_PATH)")
	f.StringVar(&o.sources, "sources", envOr("SKILL_MANAGER_SOURCES", ""), "The skill sources: a JSON array of github.com repository URLs and {url, ref, path, readOnly} objects, or comma-separated URLs; ref (branch, tag or commit; empty: the default branch) is what discovery reads and a skill without a ref pins, path the directory scanned, readOnly is reported (SKILL_MANAGER_SOURCES)")
	f.DurationVar(&o.cacheTTL, "cache-ttl", envDuration(o.envErrs, "cache-ttl", "SKILL_MANAGER_CACHE_TTL", 5*time.Minute), "How long a source's discovered skills are reused (SKILL_MANAGER_CACHE_TTL)")
	f.StringVar(&o.githubAPIURL, "github-api-url", envOr("SKILL_MANAGER_GITHUB_API_URL", server.DefaultGitHubAPIURL), "GitHub REST API base URL: discovery, pinning and the pin's GET /user (SKILL_MANAGER_GITHUB_API_URL)")
	f.StringVar(&o.githubToken, "github-token", envOr("GITHUB_TOKEN", ""), "Static GitHub token skills are read with; prefer the environment (GITHUB_TOKEN). Mutually exclusive with the GitHub App")
	f.StringVar(&o.appID, "github-app-id", envOr("SKILL_MANAGER_GITHUB_APP_ID", ""), "ID of the read-only GitHub App skills are read with: installation tokens at the App's rate limit (SKILL_MANAGER_GITHUB_APP_ID)")
	f.StringVar(&o.appInstall, "github-app-installation-id", envOr("SKILL_MANAGER_GITHUB_APP_INSTALLATION_ID", ""), "Installation ID of that App (SKILL_MANAGER_GITHUB_APP_INSTALLATION_ID)")
	f.StringVar(&o.appKeyFile, "github-app-private-key-file", envOr("SKILL_MANAGER_GITHUB_APP_PRIVATE_KEY_FILE", ""), "PEM private key file of that App (SKILL_MANAGER_GITHUB_APP_PRIVATE_KEY_FILE)")
	f.StringVar(&o.githubPinAuth, "github-authorization-server", envOr("SKILL_MANAGER_GITHUB_AUTHORIZATION_SERVER", ""), "Issuer identity of the GitHub App muster pins this server's MCP registration to: the bearer is the person's App user token, verified with GET /user, whose login decides about private repositories, and the IdP ID token arrives in "+server.ForwardedIdentityHeader+". Empty: MCP callers have no GitHub login and are refused private repositories. The REST API takes the pair either way (a caller such as agent-manager forwards the person's GitHub token). Needs --enable-oauth (SKILL_MANAGER_GITHUB_AUTHORIZATION_SERVER)")
	f.StringVar(&o.ociRegistries, "oci-registries", envOr("SKILL_MANAGER_OCI_REGISTRIES", strings.Join(oci.DefaultHosts, ",")), "Comma-separated OCI registry hosts (host or host:port) an OCI skill or plugin may come from; no other registry, and none by IP address, is asked. Empty: none (SKILL_MANAGER_OCI_REGISTRIES)")
	f.StringVar(&o.musterServer, "muster-server", envOr("SKILL_MANAGER_MUSTER_SERVER", "skill-manager"), "This server's MCPServer name in muster, which a refusal names for core_auth_login when --github-authorization-server is set (SKILL_MANAGER_MUSTER_SERVER)")
	f.StringVar(&o.serviceToken, "service-token", envOr("SKILL_MANAGER_SERVICE_TOKEN", ""), "A bearer the REST API admits as the platform's own service caller, no person: agent-manager's migrate, which has no caller; no readability check applies to it. Prefer the environment (SKILL_MANAGER_SERVICE_TOKEN). Needs --enable-oauth")
	f.BoolVar(&o.oauthEnabled, "enable-oauth", envBool(o.envErrs, "enable-oauth", "SKILL_MANAGER_OAUTH_ENABLED", false), "Require an OAuth 2.1 bearer token on the MCP endpoint and the REST API, validated against Dex (mcp-oauth) (SKILL_MANAGER_OAUTH_ENABLED)")
	f.StringVar(&o.oauthBaseURL, "oauth-base-url", envOr("SKILL_MANAGER_OAUTH_BASE_URL", ""), "Public base URL of this server: the issuer of its OAuth metadata, https or loopback http (SKILL_MANAGER_OAUTH_BASE_URL)")
	f.StringVar(&o.dexIssuerURL, "dex-issuer-url", envOr("DEX_ISSUER_URL", ""), "Dex issuer URL (DEX_ISSUER_URL)")
	f.StringVar(&o.dexClientID, "dex-client-id", envOr("DEX_CLIENT_ID", ""), "Dex client ID (DEX_CLIENT_ID)")
	f.StringVar(&o.dexClientSecret, "dex-client-secret", envOr("DEX_CLIENT_SECRET", ""), "Dex client secret; prefer the environment (DEX_CLIENT_SECRET)")
	f.StringVar(&o.dexCAFile, "dex-ca-file", envOr("DEX_CA_FILE", ""), "PEM CA bundle of a Dex with a private certificate; verifies discovery, token and JWKS calls (DEX_CA_FILE)")
	f.BoolVar(&o.dexAllowPrivateIP, "allow-private-oauth-urls", envBool(o.envErrs, "allow-private-oauth-urls", "SKILL_MANAGER_OAUTH_ALLOW_PRIVATE_URLS", false), "Let the Dex issuer resolve to a private or loopback address, an in-cluster Dex (SKILL_MANAGER_OAUTH_ALLOW_PRIVATE_URLS)")
	f.StringVar(&o.oauthTrustedAudiences, "oauth-trusted-audiences", envOr("OAUTH_TRUSTED_AUDIENCES", ""), "Comma-separated OAuth client IDs whose Dex id_tokens are accepted as bearer tokens: the platform client and the audiences muster requires (OAUTH_TRUSTED_AUDIENCES)")
	f.BoolVar(&o.ssoAllowPrivateIPs, "sso-allow-private-ips", envBool(o.envErrs, "sso-allow-private-ips", "SSO_ALLOW_PRIVATE_IPS", false), "Let Dex's JWKS endpoint resolve to a private address when validating forwarded tokens (SSO_ALLOW_PRIVATE_IPS)")
	f.BoolVar(&o.allowPublicClientRegistration, "allow-public-client-registration", envBool(o.envErrs, "allow-public-client-registration", "SKILL_MANAGER_OAUTH_ALLOW_PUBLIC_REGISTRATION", false), "Accept unauthenticated dynamic client registration; labs only (SKILL_MANAGER_OAUTH_ALLOW_PUBLIC_REGISTRATION)")
	return cmd, o
}

// parseSources reads --sources.
func (o *serveOptions) parseSources() ([]skills.Source, error) {
	list, err := skills.ParseSources(o.sources)
	if err != nil {
		return nil, fmt.Errorf("--sources: %w", err)
	}
	return list, nil
}

// tokenSource is what skills are read with: the GitHub App's installation
// tokens, a static token, or nothing (anonymous).
func (o *serveOptions) tokenSource() (skills.TokenSource, error) {
	if o.appID == "" && o.appInstall == "" && o.appKeyFile == "" {
		return skills.StaticToken(o.githubToken), nil
	}
	if o.githubToken != "" {
		return nil, errors.New("--github-app-id and --github-token (GITHUB_TOKEN) are mutually exclusive: skills are read with one credential")
	}
	key, err := os.ReadFile(o.appKeyFile)
	if err != nil {
		return nil, fmt.Errorf("--github-app-private-key-file: %w", err)
	}
	return skills.NewApp(o.githubAPIURL, o.appID, o.appInstall, key, nil)
}

// serverConfig is the listener's configuration.
func (o *serveOptions) serverConfig() (server.Config, error) {
	cfg := server.Config{Addr: o.listen, MCPPath: o.mcpPath}
	if o.githubPinAuth != "" && !o.oauthEnabled {
		return server.Config{}, errors.New("--github-authorization-server needs --enable-oauth: the forwarded IdP ID token is validated by the OAuth resource server")
	}
	if strings.TrimSpace(o.serviceToken) != "" && !o.oauthEnabled {
		return server.Config{}, errors.New("--service-token needs --enable-oauth: without it the API admits everyone")
	}
	if !o.oauthEnabled {
		return cfg, nil
	}
	cfg.OAuth = &server.OAuthConfig{
		BaseURL:                       o.oauthBaseURL,
		DexIssuerURL:                  o.dexIssuerURL,
		DexClientID:                   o.dexClientID,
		DexClientSecret:               o.dexClientSecret,
		DexCAFile:                     o.dexCAFile,
		DexAllowPrivateIP:             o.dexAllowPrivateIP,
		TrustedAudiences:              splitList(o.oauthTrustedAudiences),
		SSOAllowPrivateIPs:            o.ssoAllowPrivateIPs,
		AllowPublicClientRegistration: o.allowPublicClientRegistration,
		GitHubAPIURL:                  o.githubAPIURL,
		ServiceToken:                  strings.TrimSpace(o.serviceToken),
	}
	if o.githubPinAuth != "" {
		cfg.OAuth.GitHub = &server.GitHubPin{AuthorizationServer: o.githubPinAuth, APIURL: o.githubAPIURL, MusterServer: o.musterServer}
	}
	return cfg, nil
}

func runServe(ctx context.Context, o *serveOptions) error {
	log := slog.Default()

	// A configuration error fails the start, before anything else is built.
	if err := o.envErr(); err != nil {
		return err
	}
	sources, err := o.parseSources()
	if err != nil {
		return err
	}
	tokens, err := o.tokenSource()
	if err != nil {
		return err
	}
	srvCfg, err := o.serverConfig()
	if err != nil {
		return err
	}

	// Exports only when OTEL_EXPORTER_OTLP_ENDPOINT (or OTEL_TRACES_EXPORTER)
	// is set; the W3C propagator is installed either way.
	shutdownTracing, err := tracing.Init(ctx, tracing.WithServiceName("skill-manager"), tracing.WithServiceVersion(build.Version))
	if err != nil {
		return fmt.Errorf("tracing: %w", err)
	}
	defer flush(ctx, "traces", shutdownTracing, log)
	// Serves Prometheus metrics on its own listener with
	// OTEL_METRICS_EXPORTER=prometheus (the chart's default).
	shutdownMetrics, err := metrics.Init(ctx, metrics.WithServiceName("skill-manager"), metrics.WithServiceVersion(build.Version))
	if err != nil {
		return fmt.Errorf("metrics: %w", err)
	}
	defer flush(ctx, "metrics", shutdownMetrics, log)

	discoverer := skills.New(skills.Config{Repositories: sources, APIURL: o.githubAPIURL, Tokens: tokens, CacheTTL: o.cacheTTL, Tags: tags.AgentChartRule()}, log)
	// An empty list is no registry, not the defaults.
	registries := append([]string{}, splitList(o.ociRegistries)...)
	resolver := skills.NewResolver(o.githubAPIURL, tokens, nil, oci.NewRegistry(registries, nil))
	if o.githubPinAuth != "" {
		resolver.WithSignIn(o.musterServer)
	}
	svc := catalog.New(discoverer, resolver)
	log.Info("skill sources configured", "sources", len(sources), "githubApp", o.appID != "", "githubPin", o.githubPinAuth != "")

	srv, err := server.New(srvCfg, api.NewREST(svc, log), api.NewMCPServer(svc, build.Version), log)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	return srv.Run(ctx)
}

// flush runs an exporter's shutdown with a bounded timeout, after the server
// stopped.
func flush(ctx context.Context, what string, shutdown func(context.Context) error, log *slog.Logger) {
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := shutdown(shutdownCtx); err != nil {
		log.Warn("flushing "+what+" failed", "error", err)
	}
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func envOr(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

// envBool is key's boolean, def when unset; a value that does not parse is
// recorded in errs under flag and fails the start unless flag is given.
func envBool(errs map[string]error, flag, key string, def bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		errs[flag] = fmt.Errorf("%s=%q is not a boolean", key, v)
		return def
	}
	return b
}

// envDuration is key's Go duration, def when unset; a value that does not
// parse is recorded in errs under flag and fails the start unless flag is
// given.
func envDuration(errs map[string]error, flag, key string, def time.Duration) time.Duration {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		errs[flag] = fmt.Errorf("%s=%q is not a Go duration", key, v)
		return def
	}
	return d
}
