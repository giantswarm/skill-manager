package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/giantswarm/mcp-oauth/providers/oidc"

	"github.com/giantswarm/skill-manager/internal/catalog"
	"github.com/giantswarm/skill-manager/internal/identity"
)

// GitHubExchangeConfig identifies a confidential RFC 8693 client and its
// operator-controlled target. No caller can select the endpoint or target.
type GitHubExchangeConfig struct {
	TokenEndpoint string
	// ClientID wins over ClientIDFile, read once at startup.
	ClientID         string
	ClientIDFile     string
	ClientSecretFile string
	// Audience is the RFC 8693 target that releases the person's user token
	// of skill-manager's GitHub App, and the App the grant_required answer
	// names.
	Audience string
	CAFile   string
	// Logger records the broker's refusals; nil discards them.
	Logger *slog.Logger
}

// Validate checks required fields.
func (c GitHubExchangeConfig) Validate() error {
	endpoint, err := url.Parse(c.TokenEndpoint)
	if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return errors.New("GitHub token exchange requires an HTTPS token endpoint without credentials, query or fragment")
	}
	if (c.ClientID == "" && c.ClientIDFile == "") || c.ClientSecretFile == "" || c.Audience == "" {
		return errors.New("GitHub token exchange requires a client ID or client ID file, a client secret file and an audience")
	}
	return nil
}

func (c GitHubExchangeConfig) resolveClientID() (string, error) {
	if c.ClientID != "" {
		return c.ClientID, nil
	}
	clientID, err := os.ReadFile(c.ClientIDFile)
	if err != nil || len(strings.TrimSpace(string(clientID))) == 0 {
		return "", errors.New("GitHub token exchange needs a client ID: set the client ID or provide it in the client ID file")
	}
	return strings.TrimSpace(string(clientID)), nil
}

// NewGitHubExchange obtains the person's user grant on demand. Tokens are
// neither cached nor attached to a session; only the call that fetched one
// holds it.
func NewGitHubExchange(configuration GitHubExchangeConfig) (catalog.GitHubGrant, error) {
	if err := configuration.Validate(); err != nil {
		return catalog.GitHubGrant{}, err
	}
	clientID, err := configuration.resolveClientID()
	if err != nil {
		return catalog.GitHubGrant{}, err
	}
	configuration.ClientID = clientID
	roots, err := loadRootCAs(configuration.CAFile)
	if err != nil {
		return catalog.GitHubGrant{}, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if roots != nil {
		transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}
	}
	client := &http.Client{Transport: transport, Timeout: 15 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	return newGitHubExchange(configuration, client), nil
}

func newGitHubExchange(configuration GitHubExchangeConfig, client *http.Client) catalog.GitHubGrant {
	// The endpoint is operator configuration, never a caller's, and an
	// in-cluster broker resolves to a private address: the client checks
	// only that it is HTTPS.
	exchanger := oidc.NewTokenExchangeClientWithOptions(oidc.TokenExchangeClientOptions{HTTPClient: client, Logger: slog.New(slog.DiscardHandler), AllowPrivateIP: true})
	logger := configuration.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	app := configuration.Audience
	token := func(ctx context.Context) (string, error) {
		person, ok := identity.FromContext(ctx)
		subjectToken, hasToken := identity.TokenFromContext(ctx)
		if !ok || !hasToken || subjectToken == "" {
			return "", fmt.Errorf("%w: a private skill repository needs your verified platform identity to fetch your GitHub authorization", catalog.ErrForbidden)
		}
		// A delegated caller, or one whose token cannot show it is not,
		// never receives the person's GitHub grant.
		if claims, err := oidc.ParseUnverifiedClaims(subjectToken); err != nil || claims["act"] != nil || person.Delegated {
			return "", fmt.Errorf("%w: a private skill repository is read with your own GitHub grant, which an agent acting for you does not receive", catalog.ErrForbidden)
		}
		secret, err := os.ReadFile(configuration.ClientSecretFile)
		if err != nil || len(strings.TrimSpace(string(secret))) == 0 {
			return "", errors.New("GitHub token exchange credentials are unavailable")
		}
		result, err := exchanger.Exchange(ctx, oidc.TokenExchangeRequest{
			TokenEndpoint:      configuration.TokenEndpoint,
			SubjectToken:       subjectToken,
			SubjectTokenType:   oidc.TokenTypeIDToken,
			RequestedTokenType: oidc.TokenTypeAccessToken,
			Audience:           configuration.Audience,
			ClientID:           configuration.ClientID,
			ClientSecret:       strings.TrimSpace(string(secret)),
		})
		if err != nil {
			if refusal, ok := errors.AsType[*oidc.TokenExchangeError](err); ok {
				logger.Warn("GitHub token exchange refused", "audience", app, "status", refusal.StatusCode, "error", refusal.Code, "description", refusal.Description)
				// The broker answers a missing grant with invalid_target;
				// its description is generic, so the code alone decides.
				if refusal.StatusCode == http.StatusBadRequest && refusal.Code == "invalid_target" {
					return "", &catalog.ErrGrantRequired{App: app, Reason: fmt.Sprintf("this private skill repository is read with your grant of the GitHub App %s in muster, which you have not given: grant it (core_auth_login app=%s), then call again", app, app)}
				}
				return "", errors.New("GitHub authorization service refused the exchange")
			}
			logger.Warn("GitHub token exchange failed", "audience", app, "error", err)
			return "", errors.New("GitHub authorization service is unavailable")
		}
		if !strings.EqualFold(result.TokenType, "Bearer") || result.IssuedTokenType != oidc.TokenTypeAccessToken || result.ExpiresIn <= 0 || result.RefreshToken != "" {
			return "", errors.New("GitHub authorization service returned an invalid access token response")
		}
		return result.AccessToken, nil
	}
	return catalog.GitHubGrant{App: app, Token: token}
}
