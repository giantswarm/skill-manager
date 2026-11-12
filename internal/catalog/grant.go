package catalog

import (
	"context"
	"errors"
	"sync"

	"github.com/giantswarm/skill-manager/internal/identity"
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

// GitHubUser fetches the person's GitHub grant for one call.
type GitHubUser func(context.Context) (*identity.GitHub, error)

// WithGitHubUser fetches the person's grant when a private repository needs
// their GitHub login and the call carries none (no App-pinned registration).
func (s *Service) WithGitHubUser(user GitHubUser) *Service {
	s.gitHubUser = user
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

// lazyLogin fetches the person's GitHub login at most once per call, and only
// when a private repository asks for it.
type lazyLogin struct {
	user  GitHubUser
	once  sync.Once
	login string
	err   error
}

func (s *Service) lazyLogin() *lazyLogin {
	if s.gitHubUser == nil {
		return nil
	}
	return &lazyLogin{user: s.gitHubUser}
}

func (l *lazyLogin) get(ctx context.Context) (string, error) {
	l.once.Do(func() {
		gh, err := l.user(ctx)
		switch {
		case err != nil:
			l.err = err
		case gh != nil:
			l.login = gh.Login
		}
	})
	return l.login, l.err
}

// requireReadable is the pinner's RequireReadable with the caller's GitHub
// login: the App-pinned registration's, else, only for a repository that is
// private, the one login fetches. A public repository never asks for it.
func (s *Service) requireReadable(ctx context.Context, repoURL string, login *lazyLogin) error {
	known := gitHubLogin(ctx)
	err := s.pinner.RequireReadable(ctx, repoURL, known)
	if known != "" || login == nil || !errors.Is(err, skills.ErrNotReadable) {
		return err
	}
	fetched, lerr := login.get(ctx)
	if lerr != nil {
		return lerr
	}
	if fetched == "" {
		return err
	}
	return s.pinner.RequireReadable(ctx, repoURL, fetched)
}
