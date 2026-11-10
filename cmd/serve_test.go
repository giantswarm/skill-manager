package cmd

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/giantswarm/skill-manager/internal/skills"
)

// parse runs the serve command's flag parsing over args.
func parse(t *testing.T, args ...string) *serveOptions {
	t.Helper()
	cmd, o := newServe()
	require.NoError(t, cmd.Flags().Parse(args))
	return o
}

// --sources takes the chart's JSON list: URLs and {url, ref, path, readOnly}.
func TestServeSourcesFlag(t *testing.T) {
	o := parse(t, `--sources=[{"url":"https://github.com/giantswarm/agent-platform","ref":"v4.180.0","path":"skills","readOnly":true},"https://github.com/giantswarm/agent-skills"]`)
	got, err := o.parseSources()
	require.NoError(t, err)
	assert.Equal(t, []skills.Source{
		{URL: "https://github.com/giantswarm/agent-platform", Ref: "v4.180.0", Path: "skills", ReadOnly: true},
		{URL: "https://github.com/giantswarm/agent-skills"},
	}, got)

	t.Setenv("SKILL_MANAGER_SOURCES", "https://github.com/giantswarm/agent-skills")
	got, err = parse(t).parseSources()
	require.NoError(t, err)
	assert.Equal(t, []skills.Source{{URL: "https://github.com/giantswarm/agent-skills"}}, got, "the environment is the default")
}

// A configuration the sources cannot be served from fails the start, naming
// the flag.
func TestServeSourcesFlagRefusesOverlaps(t *testing.T) {
	o := parse(t, `--sources=[{"url":"https://github.com/giantswarm/x","path":"skills"},{"url":"https://github.com/giantswarm/x","path":"skills/a"}]`)
	_, err := o.parseSources()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--sources")
	assert.Contains(t, err.Error(), "overlapping")
}

// Skills are read with one credential: the GitHub App, a token, or none.
func TestServeTokenSource(t *testing.T) {
	ts, err := parse(t).tokenSource()
	require.NoError(t, err)
	assert.Equal(t, skills.StaticToken(""), ts, "anonymous")

	ts, err = parse(t, "--github-token=t0k").tokenSource()
	require.NoError(t, err)
	assert.Equal(t, skills.StaticToken("t0k"), ts)

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	keyFile := filepath.Join(t.TempDir(), "private-key")
	require.NoError(t, os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0o600))
	app := []string{"--github-app-id=1", "--github-app-installation-id=2", "--github-app-private-key-file=" + keyFile}
	ts, err = parse(t, app...).tokenSource()
	require.NoError(t, err)
	assert.IsType(t, &skills.App{}, ts)

	_, err = parse(t, append(app, "--github-token=t0k")...).tokenSource()
	assert.ErrorContains(t, err, "mutually exclusive")
}

func TestServeDefaults(t *testing.T) {
	o := parse(t)
	assert.Equal(t, ":8080", o.listen)
	assert.Equal(t, "/mcp", o.mcpPath)
	assert.Equal(t, 5*time.Minute, o.cacheTTL)
	assert.Equal(t, "https://api.github.com", o.githubAPIURL)
	assert.False(t, o.oauthEnabled)
}

// The GitHub App pin needs OAuth, which validates the ID token beside it.
func TestServePinNeedsOAuth(t *testing.T) {
	_, err := parse(t, "--github-authorization-server=https://github.com/apps/x").serverConfig()
	assert.ErrorContains(t, err, "--enable-oauth")
}

// The service token and the GitHub API reach the REST guard.
func TestServeServiceToken(t *testing.T) {
	t.Setenv("SKILL_MANAGER_SERVICE_TOKEN", "s3cret")
	cfg, err := parse(t, "--enable-oauth", "--github-api-url=https://ghe.example.com/api/v3").serverConfig()
	require.NoError(t, err)
	assert.Equal(t, "s3cret", cfg.OAuth.ServiceToken)
	assert.Equal(t, "https://ghe.example.com/api/v3", cfg.OAuth.GitHubAPIURL)

	_, err = parse(t).serverConfig()
	assert.ErrorContains(t, err, "--enable-oauth", "a service token without OAuth guards nothing")
}

// An environment value that does not parse fails the start, naming it.
func TestServeRefusesUnparsableEnvironment(t *testing.T) {
	t.Setenv("SKILL_MANAGER_OAUTH_ENABLED", "yes please")
	o, envErr := parseServe(t)
	require.Nil(t, o)
	assert.ErrorContains(t, envErr, "SKILL_MANAGER_OAUTH_ENABLED")

	t.Setenv("SKILL_MANAGER_OAUTH_ENABLED", "")
	t.Setenv("SKILL_MANAGER_CACHE_TTL", "five minutes")
	_, envErr = parseServe(t)
	assert.ErrorContains(t, envErr, "SKILL_MANAGER_CACHE_TTL")
}

// A flag wins over an unparsable environment value of its own.
func TestServeFlagWinsOverAnUnparsableEnvironment(t *testing.T) {
	t.Setenv("SKILL_MANAGER_CACHE_TTL", "5")
	cmd, o := newServe()
	require.NoError(t, cmd.Flags().Parse([]string{"--cache-ttl=5m"}))
	require.NoError(t, o.envErr())
	assert.Equal(t, 5*time.Minute, o.cacheTTL)
}

// The service token is trimmed: a Secret written with a trailing newline
// still matches.
func TestServeTrimsTheServiceToken(t *testing.T) {
	t.Setenv("SKILL_MANAGER_SERVICE_TOKEN", "s3cret\n")
	cfg, err := parse(t, "--enable-oauth").serverConfig()
	require.NoError(t, err)
	assert.Equal(t, "s3cret", cfg.OAuth.ServiceToken)
}

// parseServe builds the serve command and validates its environment as the
// start does.
func parseServe(t *testing.T) (*serveOptions, error) {
	t.Helper()
	cmd, o := newServe()
	require.NoError(t, cmd.Flags().Parse(nil))
	if err := o.envErr(); err != nil {
		return nil, err
	}
	return o, nil
}
