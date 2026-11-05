// Package identity carries the authenticated caller through a request: who
// the OAuth layer validated (subject, email, groups) and, on the App-pinned
// registration, the person's GitHub login, which decides whether they may
// read a private skill repository. A context without an identity only exists
// in tests and in a server that runs without OAuth.
package identity

import (
	"context"
)

// Source says how the caller was authenticated.
type Source string

const (
	// SourceSSO is an IdP id_token forwarded by a trusted aggregator (muster)
	// or sent by the portal through the gateway, validated against the IdP's
	// JWKS.
	SourceSSO Source = "sso"
	// SourceOAuth is an access token issued by this server's own OAuth 2.1
	// flow (a client that authenticated directly, e.g. mcp-debug).
	SourceOAuth Source = "oauth"
	// SourceService is the platform's own service token (agent-manager's
	// migrate), not a person.
	SourceService Source = "service"
)

// Identity is the authenticated caller.
type Identity struct {
	// Subject is the IdP's stable user id (the `sub` claim).
	Subject string
	// Email is the caller's email when the IdP provided one.
	Email string
	// Name is the display name when the IdP provided one.
	Name string
	// Groups are the IdP group claims (Dex `groups`; empty for Google).
	Groups []string
	// Source is how the token was validated.
	Source Source
}

// String is the caller as logged and reported on results: the email, else
// the subject.
func (id *Identity) String() string {
	if id == nil {
		return ""
	}
	if id.Email != "" {
		return id.Email
	}
	return id.Subject
}

type ctxKey int

const (
	identityKey ctxKey = iota
	gitHubKey
)

// ContextWith returns ctx carrying id.
func ContextWith(ctx context.Context, id *Identity) context.Context {
	if id == nil {
		return ctx
	}
	return context.WithValue(ctx, identityKey, id)
}

// FromContext returns the caller, if any.
func FromContext(ctx context.Context) (*Identity, bool) {
	id, ok := ctx.Value(identityKey).(*Identity)
	return id, ok && id != nil
}

// GitHub is the caller's GitHub identity on the App-pinned registration: the
// login GET /user answered for the person's GitHub user token muster (or
// agent-manager, forwarding it) put on the call, and that token.
type GitHub struct {
	Login string
	Token string
}

// ContextWithGitHub returns ctx carrying the caller's GitHub identity.
func ContextWithGitHub(ctx context.Context, gh *GitHub) context.Context {
	if gh == nil || gh.Token == "" {
		return ctx
	}
	return context.WithValue(ctx, gitHubKey, gh)
}

// GitHubFromContext returns the caller's GitHub identity, if any.
func GitHubFromContext(ctx context.Context) (*GitHub, bool) {
	gh, ok := ctx.Value(gitHubKey).(*GitHub)
	return gh, ok && gh != nil
}

type serviceKey struct{}

// ContextWithService marks the caller as the platform's own service (the
// service token agent-manager's migrate sends): no person, so no GitHub
// login and no person's readability check.
func ContextWithService(ctx context.Context) context.Context {
	return context.WithValue(ctx, serviceKey{}, true)
}

// IsService reports whether the caller is the platform's own service.
func IsService(ctx context.Context) bool {
	v, _ := ctx.Value(serviceKey{}).(bool)
	return v
}
