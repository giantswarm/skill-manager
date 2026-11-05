package server

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/giantswarm/skill-manager/internal/identity"
)

// fakeGitHub answers GET /user for the one token it knows and counts calls.
func fakeGitHub(t *testing.T, token, login string) (*httptest.Server, *int) {
	t.Helper()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/user" || r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"login":"` + login + `"}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

// TestGitHubPinCarriesBothTokens is the App-pinned registration: the bearer
// is the person's App user token, verified with GET /user once per cache
// lifetime, whose login decides about private repositories; the forwarded ID
// token in X-Muster-Id-Token is validated as a forwarded bearer is without
// the pin and becomes the caller.
func TestGitHubPinCarriesBothTokens(t *testing.T) {
	idp := newFakeIdP(t)
	gh, calls := fakeGitHub(t, "ghu_person", "jane")
	cfg := idp.config()
	cfg.GitHub = &GitHubPin{AuthorizationServer: "https://github.com/apps/giantswarm-agent-manager", APIURL: gh.URL}
	o, err := newOAuth(cfg, "/mcp", slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	t.Cleanup(func() { o.shutdown(context.Background()) })

	var seen struct {
		id *identity.Identity
		gh *identity.GitHub
	}
	h := o.protectMCP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.id, _ = identity.FromContext(r.Context())
		seen.gh, _ = identity.GitHubFromContext(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}))
	call := func(bearer, idToken string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		if idToken != "" {
			req.Header.Set(ForwardedIdentityHeader, idToken)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	forwarded := idp.idToken(t, []string{"agent-platform"}, time.Now().Add(30*time.Minute))

	rec := call("ghu_person", forwarded)
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	assert.Equal(t, "admin@lab.local", seen.id.Email, "the caller is the person the ID token names")
	assert.Equal(t, &identity.GitHub{Login: "jane", Token: "ghu_person"}, seen.gh, "the person's GitHub login")

	require.Equal(t, http.StatusNoContent, call("ghu_person", forwarded).Code)
	assert.Equal(t, 1, *calls, "a verified bearer is cached")

	rec = call("ghu_person", "")
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Contains(t, rec.Body.String(), "auth.forwardIdentity: true")

	rec = call("ghu_revoked", forwarded)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Contains(t, rec.Body.String(), "core_auth_login server=skill-manager")

	rec = call("", forwarded)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)

	assert.Equal(t, http.StatusUnauthorized, call("ghu_person", idp.idToken(t, []string{"someone-else"}, time.Now().Add(30*time.Minute))).Code, "the ID token is validated as without the pin")

}

// TestRESTTakesThePinnedPairOrTheIDToken: agent-manager forwards the person's
// GitHub user token as the bearer and their ID token in X-Muster-Id-Token, and
// the request carries their GitHub login; the portal sends the ID token alone,
// and the request carries the person without a login.
func TestRESTTakesThePinnedPairOrTheIDToken(t *testing.T) {
	idp := newFakeIdP(t)
	gh, _ := fakeGitHub(t, "ghu_person", "jane")
	cfg := idp.config()
	// No MCP pin: the REST API takes the pair agent-manager forwards anyway.
	cfg.GitHubAPIURL = gh.URL
	cfg.ServiceToken = "migrate-secret"
	o, err := newOAuth(cfg, "/mcp", quiet())
	require.NoError(t, err)
	t.Cleanup(func() { o.shutdown(context.Background()) })

	var login string
	var person bool
	h := o.protectREST(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, person = identity.FromContext(r.Context())
		login = ""
		if g, ok := identity.GitHubFromContext(r.Context()); ok {
			login = g.Login
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	call := func(bearer, idToken string) int {
		req := httptest.NewRequest(http.MethodPost, "/v1/resolve", nil)
		req.Header.Set("Authorization", "Bearer "+bearer)
		if idToken != "" {
			req.Header.Set(ForwardedIdentityHeader, idToken)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	forwarded := idp.idToken(t, []string{"agent-platform"}, time.Now().Add(30*time.Minute))

	require.Equal(t, http.StatusNoContent, call("ghu_person", forwarded))
	assert.True(t, person)
	assert.Equal(t, "jane", login, "the pinned pair carries the GitHub login")

	require.Equal(t, http.StatusNoContent, call(forwarded, ""))
	assert.True(t, person)
	assert.Empty(t, login, "the ID token alone carries no GitHub login")

	assert.Equal(t, http.StatusUnauthorized, call("ghu_revoked", forwarded), "a GitHub token GitHub refuses")

	// agent-manager's migrate has no caller: it sends the service token.
	var service bool
	svcH := o.protectREST(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		service = identity.IsService(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodPost, "/v1/resolve", nil)
	req.Header.Set("Authorization", "Bearer migrate-secret")
	rec := httptest.NewRecorder()
	svcH.ServeHTTP(rec, req)
	require.Equal(t, http.StatusNoContent, rec.Code)
	assert.True(t, service, "the service token is the platform's own caller")
	req = httptest.NewRequest(http.MethodPost, "/v1/resolve", nil)
	req.Header.Set("Authorization", "Bearer migrate-secreT")
	rec = httptest.NewRecorder()
	svcH.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code, "anything else is validated as an ID token")

	mcp := o.protectMCP(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	req = httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer migrate-secret")
	rec = httptest.NewRecorder()
	mcp.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code, "the service token is for the REST API only")
}

func TestGitHubPinValidation(t *testing.T) {
	assert.Error(t, GitHubPin{}.Validate())
	assert.Error(t, GitHubPin{AuthorizationServer: "github.com/apps/x"}.Validate())
	assert.NoError(t, GitHubPin{AuthorizationServer: "https://github.com/apps/giantswarm-agent-manager"}.Validate())
}
