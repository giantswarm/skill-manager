package catalog

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/giantswarm/skill-manager/internal/skills"
)

// ErrGrantRequired is a call that needs the person's GitHub view of a private
// repository while they have not granted the GitHub App: their grant of App,
// which muster keeps. The MCP answer names App so muster turns it into the
// App's sign-in.
type ErrGrantRequired struct {
	// App is the provider App, by muster's name for it.
	App string
	// Reason says what the grant is for.
	Reason string
}

func (e *ErrGrantRequired) Error() string { return e.Reason }

// GitHubGrant is the person's grant of skill-manager's GitHub App, fetched
// for one call only when a private repository needs it.
type GitHubGrant struct {
	// App is the App's name in grant_required answers and listings: the
	// token exchange audience.
	App string
	// Token is the person's user token of the App; a missing grant is
	// *ErrGrantRequired.
	Token func(context.Context) (string, error)
}

// WithGitHubGrant lets GitHub decide, as the person, about a private
// repository when the call carries no GitHub login: their user token is
// fetched and GitHub asked whether it can read the repository.
func (s *Service) WithGitHubGrant(grant GitHubGrant) *Service {
	s.gitHubGrant = &grant
	return s
}

// GrantHint names the provider App whose grant would show what a listing
// withheld.
type GrantHint struct {
	App string `json:"app"`
}

// Withheld is what a listing left out: private repositories the caller cannot
// read, or cannot read yet, counted without their URLs.
type Withheld struct {
	// Partial is set when anything was withheld.
	Partial bool `json:"partial,omitempty"`
	// Withheld is how many repositories were left out.
	Withheld int `json:"withheld,omitempty"`
	// GrantRequired names the App to grant when a missing grant withheld
	// any of them.
	GrantRequired *GrantHint `json:"grantRequired,omitempty"`
	// Note says the same in plain words.
	Note string `json:"note,omitempty"`
}

const (
	noteGrant    = "Connect GitHub to see private skills."
	noteWithheld = "Some private skill sources are not shown: you cannot read them on GitHub."
)

// withhold counts one repository left out; grant is the App whose missing
// grant left it out, nil when the person cannot read it anyway.
func (w *Withheld) withhold(grant *ErrGrantRequired) {
	w.Partial = true
	w.Withheld++
	if grant != nil && w.GrantRequired == nil {
		w.GrantRequired = &GrantHint{App: grant.App}
	}
	w.Note = noteWithheld
	if w.GrantRequired != nil {
		w.Note = noteGrant
	}
}

// lazyGrant fetches the person's user token at most once per call, and only
// when a private repository asks for it.
type lazyGrant struct {
	fetch func(context.Context) (string, error)
	once  sync.Once
	token string
	err   error
}

func (s *Service) lazyGrant() *lazyGrant {
	if s.gitHubGrant == nil {
		return nil
	}
	return &lazyGrant{fetch: s.gitHubGrant.Token}
}

func (l *lazyGrant) get(ctx context.Context) (string, error) {
	l.once.Do(func() { l.token, l.err = l.fetch(ctx) })
	return l.token, l.err
}

// requireReadable decides whether the caller may read repoURL. A caller with
// a verified GitHub login (the forwarded GitHub pair) is checked by that
// login. Otherwise a public repository is readable without asking anyone,
// and for a private one, with a grant configured, GitHub decides as the
// person: their user token is fetched (grant) and GitHub asked whether it
// reads the repository.
func (s *Service) requireReadable(ctx context.Context, repoURL string, grant *lazyGrant) error {
	known := gitHubLogin(ctx)
	err := s.pinner.RequireReadable(ctx, repoURL, known)
	if known != "" || grant == nil || !errors.Is(err, skills.ErrNotReadable) {
		return err
	}
	token, err := grant.get(ctx)
	if err != nil {
		return err
	}
	err = s.pinner.RequireReadableAs(ctx, repoURL, token)
	if errors.Is(err, skills.ErrUserTokenRefused) {
		app := s.gitHubGrant.App
		return &ErrGrantRequired{App: app, Reason: fmt.Sprintf("GitHub refused your grant of the GitHub App %s: grant it again in muster (core_auth_login app=%s), then call again", app, app)}
	}
	return err
}
