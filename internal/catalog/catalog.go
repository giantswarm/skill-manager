// Package catalog is skill-manager's service: the configured skill sources,
// the skills they hold, and the pins agents mount them by. The MCP tools and
// the REST API are two views of it; both act for the caller, whose GitHub
// login (identity.GitHub) decides whether a private repository is theirs to
// read.
package catalog

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/giantswarm/skill-manager/internal/identity"
	"github.com/giantswarm/skill-manager/internal/oci"
	"github.com/giantswarm/skill-manager/internal/skills"
)

// What a failed call is: the REST API answers 400, 403, 404 and 502 for
// them, the MCP tools name them.
var (
	ErrInvalid   = errors.New("invalid")
	ErrForbidden = errors.New("forbidden")
	ErrNotFound  = skills.ErrNotFound
	ErrUpstream  = skills.ErrUpstream
)

// Lister is skill discovery (skills.Discoverer).
type Lister interface {
	// List discovers the skills of every configured source, or of
	// repository alone; refresh re-reads a listing older than 10 seconds.
	List(ctx context.Context, repository, ref string, refresh bool) (*skills.Result, error)
	// SourceRef is the ref of the configured source holding path of
	// repoURL; "" for none or the default branch.
	SourceRef(repoURL, path string) string
	// Sources are the configured sources.
	Sources() []skills.Source
	// Head is what a source's ref resolves to now, without its skills.
	Head(ctx context.Context, src skills.Source) (skills.Head, error)
	// Holds reports whether a configured source holds dir of repoURL.
	Holds(repoURL, dir string) bool
	// Skill reads a skill's SKILL.md and file list at a full commit.
	Skill(ctx context.Context, repoURL, commit, dir string) (*skills.SkillContent, error)
	// SkillFile reads one file of a skill at a full commit.
	SkillFile(ctx context.Context, repoURL, commit, dir, file string) (*skills.FileContent, error)
}

// Pinner resolves mutable references to pins (skills.Resolver).
type Pinner interface {
	GitHead(ctx context.Context, repoURL, ref string) (string, error)
	OCIDigest(ctx context.Context, ref string) (string, error)
	RequireReadable(ctx context.Context, repoURL, login string) error
	ClaudeFormatPlugin(ctx context.Context, repoURL, commit, path string) (bool, error)
	PathExists(ctx context.Context, repoURL, commit, path string) (bool, error)
	CommitExists(ctx context.Context, repoURL, commit string) error
}

// Service is the catalog.
type Service struct {
	skills Lister
	pinner Pinner
	// gitHubUser fetches the person's grant for a private repository when
	// the call carries no GitHub login (nil: the pinned registration's only).
	gitHubUser GitHubUser
}

// New builds the service.
func New(l Lister, p Pinner) *Service { return &Service{skills: l, pinner: p} }

// Source is a configured skill source and the commit its ref resolves to
// now.
type Source struct {
	URL      string `json:"url"`
	Ref      string `json:"ref,omitempty"`
	Path     string `json:"path,omitempty"`
	ReadOnly bool   `json:"readOnly,omitempty"`
	Commit   string `json:"commit,omitempty"`
	// Error says why the source could not be read, or why the caller may
	// not see its commit.
	Error string `json:"error,omitempty"`
}

// Sources lists the configured sources, each with the commit its ref
// resolves to now (cached for the cache TTL), without reading their skills.
// A private source is listed only to a caller who can read it; the others
// are counted in Withheld, without their URLs. Listing never asks for a
// GitHub grant: without one, Withheld names the App that would show them.
func (s *Service) Sources(ctx context.Context) ([]Source, Withheld, error) {
	list := s.skills.Sources()
	out := make([]Source, len(list))
	hidden := make([]*withheldEntry, len(list))
	login := s.lazyLogin()
	var wg sync.WaitGroup
	for i, src := range list {
		out[i] = Source{URL: src.URL, Ref: src.Ref, Path: src.Path, ReadOnly: src.ReadOnly}
		wg.Go(func() {
			head, err := s.skills.Head(ctx, src)
			switch {
			case err != nil:
				out[i].Error = err.Error()
			case head.Private && !identity.IsService(ctx):
				if err := s.requireReadable(ctx, src.URL, login); err != nil {
					hidden[i] = withheldFor(err)
					return
				}
				out[i].Commit = head.Commit
			default:
				out[i].Commit = head.Commit
			}
		})
	}
	wg.Wait()
	var withheld Withheld
	shown := make([]Source, 0, len(out))
	for i, src := range out {
		if hidden[i] != nil {
			withheld.withhold(hidden[i].grant)
			continue
		}
		shown = append(shown, src)
	}
	return shown, withheld, nil
}

// withheldEntry is a repository a listing leaves out; grant is set when a
// missing grant is why.
type withheldEntry struct {
	grant *ErrGrantRequired
}

// withheldFor is a private repository a listing leaves out because err kept
// it from confirming the caller may read it; a missing grant is named, any
// other cause (a refusal, an unavailable check) is not, so the listing never
// tells which private repositories exist.
func withheldFor(err error) *withheldEntry {
	if grant, ok := errors.AsType[*ErrGrantRequired](err); ok {
		return &withheldEntry{grant: grant}
	}
	return &withheldEntry{}
}

// Listing is List's answer: the repositories the caller may see and what was
// withheld.
type Listing struct {
	skills.Result
	Withheld
}

// List discovers the skills of every configured source, or of source alone
// (any github.com repository URL; a configured one keeps its ref and path). A
// private repository is listed only to a caller who can read it (and to the
// platform's service caller). A configured one the caller cannot read is
// counted in Withheld, without its URL; one the caller named gets its URL
// and why its skills are withheld. Listing never asks for a GitHub grant.
func (s *Service) List(ctx context.Context, source string, refresh bool) (*Listing, error) {
	res, err := s.skills.List(ctx, source, "", refresh)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	out := &Listing{Result: skills.Result{Repositories: make([]skills.Repository, 0, len(res.Repositories))}}
	login := s.lazyLogin()
	for _, repo := range res.Repositories {
		shown, entry := s.visible(ctx, repo, login)
		if entry != nil {
			out.withhold(entry.grant)
			continue
		}
		out.Repositories = append(out.Repositories, shown)
	}
	return out, nil
}

// visible is repo as the caller may see it. A private repository, and one
// whose read failed (its visibility may be unknown), is checked against the
// caller; refused, only its URL and why are left. For a repository the caller
// named, not a configured one, a missing and an unreadable repository get
// the same answer: the listing does not tell which private repositories
// exist.
func (s *Service) visible(ctx context.Context, repo skills.Repository, login *lazyLogin) (skills.Repository, *withheldEntry) {
	if identity.IsService(ctx) || (!repo.Private && repo.Error == "") {
		return repo, nil
	}
	err := s.requireReadable(ctx, repo.RepoURL, login)
	if err == nil || (!repo.Private && !errors.Is(err, skills.ErrNotReadable) && !errors.Is(err, ErrNotFound)) {
		// Readable, or public with its own error to show.
		return repo, nil
	}
	configured := s.configured(repo.RepoURL)
	if configured && repo.Private {
		return skills.Repository{}, withheldFor(err)
	}
	msg := err.Error()
	if !configured {
		msg = repo.RepoURL + " does not exist or you cannot read it"
	}
	return skills.Repository{RepoURL: repo.RepoURL, Ref: repo.Ref, Path: repo.Path, ReadOnly: repo.ReadOnly, Private: repo.Private, Skills: []skills.Skill{}, Plugins: []skills.Plugin{}, Error: msg, FetchedAt: repo.FetchedAt}, nil
}

// configured reports whether repoURL is a configured source's repository.
func (s *Service) configured(repoURL string) bool {
	for _, src := range s.skills.Sources() {
		if skills.SameRepository(src.URL, repoURL) {
			return true
		}
	}
	return false
}

// Request is what Resolve pins: skills and plugins, each a git or an OCI
// item.
type Request struct {
	Skills  []Item `json:"skills"`
	Plugins []Item `json:"plugins"`
}

// Item is one skill or plugin: a git repository (a ref, or a commit, at a
// path) or an OCI image. Pinned, Git carries the commit and no ref, OCI the
// reference by digest.
type Item struct {
	Name string `json:"name,omitempty"`
	// Path is the directory in the repository; "" is its root.
	Path string `json:"path,omitempty"`
	Git  *Git   `json:"git,omitempty"`
	OCI  *OCI   `json:"oci,omitempty"`
	// Skills is a plugin's skill selection, passed through.
	Skills []string `json:"skills,omitempty"`
	// ClaudeFormat is set on a pinned git plugin: whether it is a Claude Code
	// plugin (a .claude-plugin/plugin.json and no root plugin.json). true on
	// a requested plugin requires it: a git plugin that is not one is
	// refused (ErrInvalid), an OCI plugin cannot be checked and is refused.
	ClaudeFormat *bool `json:"claudeFormat,omitempty"`
}

// Git is a git source.
type Git struct {
	URL    string `json:"url"`
	Ref    string `json:"ref,omitempty"`
	Commit string `json:"commit,omitempty"`
}

// OCI is an OCI image source.
type OCI struct {
	Ref string `json:"ref"`
}

// ItemError is a refused item: its list ("skills" or "plugins"), its index
// there, and why.
type ItemError struct {
	List  string
	Index int
	Err   error
}

func (e *ItemError) Error() string { return fmt.Sprintf("%s[%d]: %v", e.List, e.Index, e.Err) }

// Unwrap exposes the kind (ErrInvalid, ErrForbidden, ErrNotFound,
// ErrUpstream).
func (e *ItemError) Unwrap() error { return e.Err }

// Resolve pins every item for the caller: a git item to the commit of its
// ref (none given: the ref of the configured source holding its path, else
// the default branch; a commit given stays), an OCI item to its digest. The
// person must be able to read every git repository (RequireReadable: a
// private one needs their GitHub login's read permission; the platform's
// service caller is exempt), and every path must exist at its commit. The
// first refused item fails the whole request.
func (s *Service) Resolve(ctx context.Context, req Request) (*Request, error) {
	req.Skills = append([]Item(nil), req.Skills...)
	req.Plugins = append([]Item(nil), req.Plugins...)
	for _, l := range []struct {
		name  string
		items []Item
	}{{"skills", req.Skills}, {"plugins", req.Plugins}} {
		for i, it := range l.items {
			cleaned, err := validate(it, l.name == "skills")
			if err != nil {
				return nil, &ItemError{List: l.name, Index: i, Err: fmt.Errorf("%w: %v", ErrInvalid, err)}
			}
			l.items[i] = cleaned
		}
	}
	r := &resolution{svc: s, login: s.lazyLogin(), service: identity.IsService(ctx), heads: map[string]string{}, readable: map[string]error{}, paths: map[string]bool{}}
	out := &Request{}
	var err error
	if out.Skills, err = r.pin(ctx, "skills", req.Skills); err != nil {
		return nil, err
	}
	if out.Plugins, err = r.pin(ctx, "plugins", req.Plugins); err != nil {
		return nil, err
	}
	return out, nil
}

// validate checks what needs no lookup and returns the item with its path
// cleaned as a source's is: relative, "." the root, never climbing out.
func validate(it Item, skill bool) (Item, error) {
	switch {
	case it.Git != nil && it.OCI != nil:
		return it, errors.New("has both git and oci; give exactly one source")
	case it.Git == nil && it.OCI == nil:
		return it, errors.New("has no source: give git: {url, ref | commit} or oci: {ref}")
	case it.Git != nil:
		g := it.Git
		switch {
		case strings.TrimSpace(g.URL) == "":
			return it, errors.New("git.url is required")
		case g.Ref != "" && g.Commit != "":
			return it, fmt.Errorf("gives both ref %q and commit %q; give the commit to keep, or the ref to have it resolved", g.Ref, g.Commit)
		case g.Commit != "" && !skills.CommitPattern.MatchString(g.Commit):
			return it, fmt.Errorf("git.commit %q is not a full commit id (40 or 64 hex characters)", g.Commit)
		}
		if err := skills.CheckRef(g.Ref); err != nil {
			return it, fmt.Errorf("git.%w", err)
		}
	default:
		if _, err := oci.ParseImageReference(it.OCI.Ref); err != nil {
			return it, fmt.Errorf("oci.ref: %v", err)
		}
		if skill && it.Path != "" {
			return it, errors.New("path is for git skills; an OCI skill is the whole image")
		}
		if it.ClaudeFormat != nil && *it.ClaudeFormat {
			return it, errors.New("claudeFormat is checked for git plugins only")
		}
	}
	if skill && it.ClaudeFormat != nil {
		return it, errors.New("claudeFormat is for plugins")
	}
	cleaned, err := skills.CleanPath(it.Path)
	if err != nil {
		return it, err
	}
	it.Path = cleaned
	return it, nil
}

// resolution is one Resolve: each repository ref, readability and path is
// looked up once, however many items share it. Its items are pinned one
// after another.
type resolution struct {
	svc     *Service
	login   *lazyLogin
	service bool

	heads    map[string]string
	readable map[string]error
	paths    map[string]bool
}

func (r *resolution) pin(ctx context.Context, list string, items []Item) ([]Item, error) {
	if len(items) == 0 {
		return nil, nil
	}
	out := make([]Item, 0, len(items))
	for i, it := range items {
		pinned, err := r.item(ctx, it, list == "plugins")
		if err != nil {
			return nil, &ItemError{List: list, Index: i, Err: err}
		}
		out = append(out, pinned)
	}
	return out, nil
}

func (r *resolution) item(ctx context.Context, it Item, plugin bool) (Item, error) {
	out := Item{Name: it.Name, Path: it.Path, Skills: it.Skills}
	if it.OCI != nil {
		// A digest is kept, but only an allowed registry's, and in the
		// pinned form a tag is.
		ref, err := r.svc.pinner.OCIDigest(ctx, it.OCI.Ref)
		if err != nil {
			return Item{}, classify(err)
		}
		out.OCI = &OCI{Ref: ref}
		return out, nil
	}
	url := strings.TrimSpace(it.Git.URL)
	if err := r.requireReadable(ctx, url); err != nil {
		return Item{}, err
	}
	commit := it.Git.Commit
	if commit != "" && strings.Trim(it.Path, "/") == "" {
		// No path check touches a given commit: ask for it.
		if err := r.svc.pinner.CommitExists(ctx, url, commit); err != nil {
			return Item{}, classify(err)
		}
	}
	if commit == "" {
		ref := it.Git.Ref
		if ref == "" {
			ref = r.svc.skills.SourceRef(url, it.Path)
		}
		var err error
		if commit, err = r.head(ctx, url, ref); err != nil {
			return Item{}, err
		}
	}
	if err := r.requirePath(ctx, url, commit, it.Path); err != nil {
		return Item{}, err
	}
	out.Git = &Git{URL: url, Commit: commit}
	if plugin {
		claude, err := r.svc.pinner.ClaudeFormatPlugin(ctx, url, commit, it.Path)
		if err != nil {
			return Item{}, classify(err)
		}
		if !claude && it.ClaudeFormat != nil && *it.ClaudeFormat {
			return Item{}, fmt.Errorf("%w: %s at %s path %q is not a Claude Code plugin (a .claude-plugin/plugin.json and no root plugin.json)", ErrInvalid, url, commit, it.Path)
		}
		out.ClaudeFormat = &claude
	}
	return out, nil
}

func (r *resolution) requireReadable(ctx context.Context, url string) error {
	if r.service {
		return nil
	}
	err, seen := r.readable[url]
	if !seen {
		err = r.svc.requireReadable(ctx, url, r.login)
		if err != nil {
			err = classify(err)
		}
		r.readable[url] = err
	}
	return err
}

func (r *resolution) head(ctx context.Context, url, ref string) (string, error) {
	key := url + "@" + ref
	head, ok := r.heads[key]
	if ok {
		return head, nil
	}
	head, err := r.svc.pinner.GitHead(ctx, url, ref)
	if err != nil {
		return "", classify(err)
	}
	r.heads[key] = head
	return head, nil
}

func (r *resolution) requirePath(ctx context.Context, url, commit, path string) error {
	path = strings.Trim(path, "/")
	if path == "" {
		return nil
	}
	key := url + "@" + commit + ":" + path
	exists, ok := r.paths[key]
	if !ok {
		var err error
		if exists, err = r.svc.pinner.PathExists(ctx, url, commit, path); err != nil {
			return classify(err)
		}
		r.paths[key] = exists
	}
	if !exists {
		return fmt.Errorf("%w: path %q does not exist in %s at %s", ErrNotFound, path, url, commit)
	}
	return nil
}

// classify keeps a resolver error's kind and makes every other failure of a
// reference invalid: one the resolver could not even look up.
func classify(err error) error {
	if _, ok := errors.AsType[*ErrGrantRequired](err); ok {
		return err
	}
	switch {
	case errors.Is(err, skills.ErrNotReadable):
		return fmt.Errorf("%w: %w", ErrForbidden, err)
	case errors.Is(err, ErrNotFound), errors.Is(err, ErrUpstream):
		return err
	}
	return fmt.Errorf("%w: %w", ErrInvalid, err)
}

// gitHubLogin is the caller's verified GitHub login, "" without one.
func gitHubLogin(ctx context.Context) string {
	if gh, ok := identity.GitHubFromContext(ctx); ok {
		return gh.Login
	}
	return ""
}
