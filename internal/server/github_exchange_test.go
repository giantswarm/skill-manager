package server

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/giantswarm/mcp-oauth/providers/oidc"
	"github.com/stretchr/testify/require"

	"github.com/giantswarm/skill-manager/internal/catalog"
	"github.com/giantswarm/skill-manager/internal/identity"
)

// JWTs with a placeholder signature: the server validated the bearer before
// the exchange reads it.
var (
	personToken    = unsignedJWT(`{"sub":"alice"}`)
	delegatedToken = unsignedJWT(`{"sub":"alice","act":{"sub":"agent"}}`)
)

func unsignedJWT(claims string) string {
	encode := base64.RawURLEncoding.EncodeToString
	return encode([]byte(`{"alg":"RS256"}`)) + "." + encode([]byte(claims)) + ".signature"
}

func TestGitHubExchange(t *testing.T) {
	for _, scenario := range []string{"success", "delegated actor", "missing identity token", "no grant", "broker error", "redirect", "invalid token type", "refresh token", "missing expiry"} {
		t.Run(scenario, func(t *testing.T) {
			secretFile := filepath.Join(t.TempDir(), "client-secret")
			require.NoError(t, os.WriteFile(secretFile, []byte("fixture-secret"), 0o600))
			exchanges := 0
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/token" {
					exchanges++
					require.Equal(t, http.MethodPost, r.Method)
					require.NoError(t, r.ParseForm())
					require.Equal(t, oidc.GrantTypeTokenExchange, r.Form.Get("grant_type"))
					require.Equal(t, personToken, r.Form.Get("subject_token"))
					require.Equal(t, oidc.TokenTypeIDToken, r.Form.Get("subject_token_type"))
					require.Equal(t, "manager-app", r.Form.Get("audience"))
					require.Empty(t, r.Form.Get("actor_token"))
					client, secret, ok := r.BasicAuth()
					require.True(t, ok)
					require.Equal(t, "manager-client", client)
					require.Equal(t, "fixture-secret", secret)
					switch scenario {
					case "no grant":
						w.WriteHeader(http.StatusBadRequest)
						_, _ = fmt.Fprint(w, `{"error":"invalid_target","error_description":"do not expose fixture-secret"}`)
						return
					case "broker error":
						w.WriteHeader(http.StatusInternalServerError)
						_, _ = fmt.Fprint(w, "do not expose verified-id-token fixture-secret")
						return
					case "redirect":
						w.Header().Set("Location", "/redirected")
						w.WriteHeader(http.StatusTemporaryRedirect)
						return
					}
					result := oidc.TokenExchangeResponse{AccessToken: "fixture-user-grant", TokenType: "Bearer", IssuedTokenType: oidc.TokenTypeAccessToken, ExpiresIn: 300} // #nosec G101 -- synthetic grants served by the test broker
					if scenario == "invalid token type" {
						result.IssuedTokenType = oidc.TokenTypeIDToken
					}
					if scenario == "refresh token" {
						result.RefreshToken = "fixture-refresh"
					}
					if scenario == "missing expiry" {
						result.ExpiresIn = 0
					}
					require.NoError(t, json.NewEncoder(w).Encode(result)) // #nosec G117 -- the fixture is a token endpoint
					return
				}
				t.Errorf("unexpected request %s: credentials never follow a redirect", r.URL.Path)
			})
			upstream := httptest.NewTLSServer(handler)
			defer upstream.Close()
			client := upstream.Client()
			client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
			configuration := GitHubExchangeConfig{TokenEndpoint: upstream.URL + "/token", ClientID: "manager-client", ClientSecretFile: secretFile, Audience: "manager-app"}
			ctx := identity.ContextWith(t.Context(), &identity.Identity{Subject: "alice"})
			switch scenario {
			case "delegated actor":
				ctx = identity.ContextWithToken(ctx, delegatedToken)
			case "missing identity token":
			default:
				ctx = identity.ContextWithToken(ctx, personToken)
			}
			grant := newGitHubExchange(configuration, client)
			require.Equal(t, "manager-app", grant.App, "the audience is the App grant_required names")
			result, err := grant.Token(ctx)
			if scenario == "success" {
				require.NoError(t, err)
				require.Equal(t, "fixture-user-grant", result) // #nosec G101 -- synthetic broker response
				_, err = grant.Token(ctx)
				require.NoError(t, err)
				require.Equal(t, 2, exchanges, "user grants are not cached")
				return
			}
			require.Error(t, err)
			require.Empty(t, result)
			for _, value := range []string{personToken, "fixture-secret", "fixture-user-grant", "fixture-refresh"} {
				require.NotContains(t, err.Error(), value)
			}
			if scenario == "no grant" {
				refusal, ok := errors.AsType[*catalog.ErrGrantRequired](err)
				require.True(t, ok)
				require.Equal(t, "manager-app", refusal.App)
			}
			if scenario == "delegated actor" {
				require.ErrorIs(t, err, catalog.ErrForbidden)
			}
			if scenario == "missing identity token" || scenario == "delegated actor" {
				require.Zero(t, exchanges)
			}
		})
	}
}

func TestGitHubExchangeConfiguration(t *testing.T) {
	valid := GitHubExchangeConfig{TokenEndpoint: "https://broker.example.com/oauth/token", ClientID: "manager", ClientSecretFile: "/mounted/client-secret", Audience: "manager-app"} // #nosec G101 -- configuration fixture with a path, no secret value
	require.NoError(t, valid.Validate())
	for _, endpoint := range []string{"http://broker.example.com/token", "https://user:password@broker.example.com/token", "https://broker.example.com/token?credential=value", "https://broker.example.com/token#fragment", "/token"} {
		configuration := valid
		configuration.TokenEndpoint = endpoint
		require.Error(t, configuration.Validate())
	}
	configuration := valid
	configuration.ClientSecretFile = ""
	require.Error(t, configuration.Validate())
	configuration = valid
	configuration.Audience = ""
	require.Error(t, configuration.Validate())
	configuration = valid
	configuration.ClientID, configuration.ClientIDFile = "", "/mounted/client-id"
	require.NoError(t, configuration.Validate(), "the client ID may come from a file")
	configuration.ClientIDFile = ""
	require.Error(t, configuration.Validate())
}

// The client ID flag wins; without it the mounted file is read at startup,
// and neither fails the start.
func TestGitHubExchangeClientID(t *testing.T) {
	file := filepath.Join(t.TempDir(), "client-id")
	require.NoError(t, os.WriteFile(file, []byte("from-file\n"), 0o600))
	id, err := GitHubExchangeConfig{ClientID: "from-flag", ClientIDFile: file}.resolveClientID()
	require.NoError(t, err)
	require.Equal(t, "from-flag", id)
	id, err = GitHubExchangeConfig{ClientIDFile: file}.resolveClientID()
	require.NoError(t, err)
	require.Equal(t, "from-file", id)
	_, err = GitHubExchangeConfig{ClientIDFile: filepath.Join(t.TempDir(), "absent")}.resolveClientID()
	require.Error(t, err)
}
