package server

import (
	"context"
	"encoding/json"
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
		if r.Header.Get("Authorization") == "Bearer ghu_ratelimited" {
			http.Error(w, `{"message":"API rate limit exceeded"}`, http.StatusForbidden)
			return
		}
		if r.URL.Path != "/user" || r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"login":"` + login + `"}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

// TestRESTTakesTheForwardedPairOrTheIDToken: agent-manager forwards the
// person's GitHub user token as the bearer and their ID token in
// X-Muster-Id-Token, and the request carries their GitHub login; the portal
// sends the ID token alone, and the request carries the person without a
// login.
func TestRESTTakesTheForwardedPairOrTheIDToken(t *testing.T) {
	idp := newFakeIdP(t)
	gh, _ := fakeGitHub(t, "ghu_person", "jane")
	cfg := idp.config()
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
	assert.Equal(t, "jane", login, "the forwarded pair carries the GitHub login")

	require.Equal(t, http.StatusNoContent, call(forwarded, ""))
	assert.True(t, person)
	assert.Empty(t, login, "the ID token alone carries no GitHub login")

	assert.Equal(t, http.StatusUnauthorized, call("ghu_revoked", forwarded), "a GitHub token GitHub refuses")
	assert.Equal(t, http.StatusUnauthorized, call("ghu_person", idp.idToken(t, []string{"someone-else"}, time.Now().Add(30*time.Minute))), "the ID token is validated as without the pair")

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

	req = httptest.NewRequest(http.MethodGet, "/v1/sources", nil)
	req.Header.Set("Authorization", "Bearer migrate-secret")
	rec = httptest.NewRecorder()
	svcH.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code, "the service token resolves, nothing else")

	mcp := o.protect(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	req = httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer migrate-secret")
	rec = httptest.NewRecorder()
	mcp.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code, "the service token is for the REST API only")

	req = httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer ghu_person")
	req.Header.Set(ForwardedIdentityHeader, forwarded)
	rec = httptest.NewRecorder()
	mcp.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code, "the MCP endpoint takes no GitHub pair")
}

func TestGitHubPairValidation(t *testing.T) {
	assert.NoError(t, gitHubPair{}.Validate(), "api.github.com by default")
	assert.Error(t, gitHubPair{APIURL: "api.github.com"}.Validate())
	assert.NoError(t, gitHubPair{APIURL: "https://ghe.example.com/api/v3"}.Validate())
}

// The ID token is validated before GitHub is asked about the GitHub token,
// GitHub's 403 (a rate limit) is a 502, not the caller's fault, and every
// refusal is a JSON error body.
func TestGitHubGuardOrderAndAnswers(t *testing.T) {
	idp := newFakeIdP(t)
	gh, calls := fakeGitHub(t, "ghu_person", "jane")
	cfg := idp.config()
	cfg.GitHubAPIURL = gh.URL
	o, err := newOAuth(cfg, "/mcp", quiet())
	require.NoError(t, err)
	t.Cleanup(func() { o.shutdown(context.Background()) })
	h := o.protectREST(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	call := func(bearer, idToken string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/v1/sources", nil)
		req.Header.Set("Authorization", "Bearer "+bearer)
		req.Header.Set(ForwardedIdentityHeader, idToken)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	rec := call("ghu_person", "not-a-token")
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Zero(t, *calls, "GitHub is not asked for a caller whose ID token fails")

	forwarded := idp.idToken(t, []string{"agent-platform"}, time.Now().Add(30*time.Minute))
	rec = call("ghu_ratelimited", forwarded)
	assert.Equal(t, http.StatusBadGateway, rec.Code)
	var body map[string]string
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.NotEmpty(t, body["error"])

	rec = call("ghu_revoked", forwarded)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Contains(t, body["error"], "GitHub refused")
	assert.NotContains(t, body["error"], "core_auth_login", "the REST caller forwards the token itself")

	rec = call("ghu_person", forwarded)
	assert.Equal(t, http.StatusNoContent, rec.Code)
	rec = call("ghu_person", forwarded)
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Equal(t, 3, *calls, "a verified bearer is cached")
}
