package server

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/giantswarm/skill-manager/internal/identity"
	"github.com/giantswarm/skill-manager/internal/lru"
)

// gitHubPair is the deprecated forwarded GitHub pair a REST caller
// (agent-manager) sends: the person's GitHub user token as the bearer,
// verified with GET /user, whose login decides about private repositories,
// and the person's IdP ID token in ForwardedIdentityHeader, which goes
// through the same validation a bearer takes without the pair.
type gitHubPair struct {
	// APIURL is the API base URL GET /user goes to (empty: api.github.com).
	APIURL string
	// CacheTTL bounds how long a verified bearer is trusted without asking
	// GitHub again (zero: DefaultGitHubCacheTTL).
	CacheTTL time.Duration
}

// ForwardedIdentityHeader is where a REST caller puts the person's IdP ID
// token beside their GitHub token (the deprecated forwarded GitHub pair).
const ForwardedIdentityHeader = "X-Muster-Id-Token"

// DefaultGitHubAPIURL is GitHub's REST API.
const DefaultGitHubAPIURL = "https://api.github.com"

// DefaultGitHubCacheTTL is how long a verified bearer is trusted without a
// second GET /user: a user token's expiry is not readable from the token,
// and a revoked one must stop working soon.
const DefaultGitHubCacheTTL = 15 * time.Minute

// gitHubCacheMax bounds the verified-token cache.
const gitHubCacheMax = 10_000

// Validate checks required fields.
func (p gitHubPair) Validate() error {
	u, err := url.Parse(p.apiURL())
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return fmt.Errorf("github pair: API URL must be an absolute http(s) URL: %q", p.apiURL())
	}
	return nil
}

func (p gitHubPair) apiURL() string {
	if p.APIURL == "" {
		return DefaultGitHubAPIURL
	}
	return strings.TrimSuffix(p.APIURL, "/")
}

// gitHubGuard verifies the GitHub bearer and hands the forwarded ID token on
// as the bearer the IdP validation reads.
type gitHubGuard struct {
	pair gitHubPair
	http *http.Client
	log  *slog.Logger

	cache *lru.Cache[[sha256.Size]byte, gitHubEntry]
}

type gitHubEntry struct {
	login string
	until time.Time
}

func newGitHubGuard(pair gitHubPair, log *slog.Logger) (*gitHubGuard, error) {
	if err := pair.Validate(); err != nil {
		return nil, err
	}
	if pair.CacheTTL <= 0 {
		pair.CacheTTL = DefaultGitHubCacheTTL
	}
	log.Info("GitHub bearer verification enabled for the forwarded GitHub pair", "api", pair.apiURL(), "cacheTTL", pair.CacheTTL)
	return &gitHubGuard{pair: pair, http: &http.Client{Timeout: 10 * time.Second}, log: log, cache: lru.New[[sha256.Size]byte, gitHubEntry](gitHubCacheMax)}, nil
}

// signIn is the one way to a bearer the pair accepts.
const signIn = "send the person's valid GitHub user token"

// githubTokenKey carries the GitHub bearer from swap to check.
type githubTokenKey struct{}

// protect admits a request that carries the forwarded ID token and whose
// bearer GitHub accepts. The ID token is validated first (inner, mcp-oauth
// with the ID token as the bearer), so GitHub is never asked about a caller
// the IdP refuses; next sees the GitHub identity on the context.
func (g *gitHubGuard) protect(inner func(http.Handler) http.Handler, next http.Handler) http.Handler {
	return g.swap(inner(g.check(next)))
}

// swap moves the GitHub bearer aside and puts the forwarded ID token in its
// place.
func (g *gitHubGuard) swap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := bearerToken(r)
		if token == "" {
			g.refuse(w, "", "no bearer token: "+signIn)
			return
		}
		idToken := strings.TrimSpace(r.Header.Get(ForwardedIdentityHeader))
		if idToken == "" {
			g.refuse(w, errInvalidToken, "no "+ForwardedIdentityHeader+" header: a GitHub token needs your platform identity beside it")
			return
		}
		r = r.Clone(context.WithValue(r.Context(), githubTokenKey{}, token))
		r.Header.Set("Authorization", "Bearer "+idToken)
		r.Header.Del(ForwardedIdentityHeader)
		next.ServeHTTP(w, r)
	})
}

// check verifies the GitHub bearer swap set aside with GET /user. GitHub's
// 401 is the caller's (401); any other failure, its 403 rate limit
// included, is GitHub's (502).
func (g *gitHubGuard) check(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, _ := r.Context().Value(githubTokenKey{}).(string)
		login, status, err := g.verify(r.Context(), token)
		switch {
		case status == http.StatusUnauthorized:
			g.refuse(w, errInvalidToken, fmt.Sprintf("GitHub refused the bearer token (%d): %s", status, signIn))
			return
		case err != nil:
			g.log.Warn("GitHub bearer verification failed", "error", err)
			writeError(w, http.StatusBadGateway, "could not verify the bearer token with GitHub: "+err.Error())
			return
		}
		next.ServeHTTP(w, r.WithContext(identity.ContextWithGitHub(r.Context(), &identity.GitHub{Login: login, Token: token})))
	})
}

// verify is the login GET /user answers for token, from the cache while the
// entry lives; status is GitHub's answer when it refused the token.
func (g *gitHubGuard) verify(ctx context.Context, token string) (string, int, error) {
	key := sha256.Sum256([]byte(token))
	now := time.Now()
	if e, ok := g.cache.Get(key); ok && now.Before(e.until) {
		return e.login, 0, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.pair.apiURL()+"/user", nil)
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := g.http.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", resp.StatusCode, fmt.Errorf("GET /user: %s", resp.Status)
	}
	var user struct {
		Login string `json:"login"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil || user.Login == "" {
		return "", 0, fmt.Errorf("GET /user: no login in the answer")
	}
	g.cache.Add(key, gitHubEntry{login: user.Login, until: now.Add(g.pair.CacheTTL)})
	return user.Login, 0, nil
}

// refuse is the 401 with the RFC 6750 challenge and the reason as the body.
func (g *gitHubGuard) refuse(w http.ResponseWriter, code, description string) {
	challenge := `Bearer realm="skill-manager"`
	if code != "" {
		challenge += fmt.Sprintf(`, error=%q, error_description=%q`, code, headerQuotedString(description))
	}
	w.Header().Set("WWW-Authenticate", challenge)
	writeError(w, http.StatusUnauthorized, description)
}

// errInvalidToken is the RFC 6750 error code for a bearer the resource
// server does not accept.
const errInvalidToken = "invalid_token"

// writeError is a refusal as the REST API's JSON error body.
func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
