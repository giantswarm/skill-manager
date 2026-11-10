// Package catalogtest holds the catalog's fakes for tests: a Pinner that
// answers like GitHub and a registry would, and a Lister over a fixed
// listing.
package catalogtest

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"
	"sync"

	"github.com/giantswarm/skill-manager/internal/skills"
)

// The fake's repositories, the commits their refs resolve to and the
// digest of its image.
const (
	SkillsRepo  = "https://github.com/giantswarm/agent-skills"
	PrivateRepo = "https://github.com/giantswarm/private-skills"
	MissingRepo = "https://github.com/giantswarm/missing"
	MainHead    = "0123456789abcdef0123456789abcdef01234567"
	FeatureHead = "89abcdef0123456789abcdef0123456789abcdef"
	TagHead     = "fedcba9876543210fedcba9876543210fedcba98"
	Digest      = "sha256:5b0bcabd1ed22e9fb1310cf6c2dec7cdef19f0ad69efa1f392e94a4333501270"
)

// Pinner answers like GitHub and a registry would: refs of SkillsRepo,
// PrivateRepo readable by alice only, OCI tags of one image, and the paths
// that exist at every commit.
type Pinner struct {
	mu    sync.Mutex
	calls []string
}

func (p *Pinner) record(call string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, call)
}

func (p *Pinner) GitHead(_ context.Context, repoURL, ref string) (string, error) {
	p.record("git " + repoURL + "@" + ref)
	switch {
	case repoURL == "https://github.com/giantswarm/down":
		return "", fmt.Errorf("%w: %w: GitHub answered 500", skills.ErrUnresolvable, skills.ErrUpstream)
	case !strings.HasPrefix(repoURL, "https://github.com/"):
		return "", fmt.Errorf("%w: not a github.com repository URL: %s", skills.ErrUnresolvable, repoURL)
	}
	switch ref {
	case "", "main":
		return MainHead, nil
	case "feature":
		return FeatureHead, nil
	case "v1.2.0":
		return TagHead, nil
	}
	return "", fmt.Errorf("%w: %w: ref %s of %s", skills.ErrUnresolvable, skills.ErrNotFound, ref, repoURL)
}

func (p *Pinner) OCIDigest(_ context.Context, ref string) (string, error) {
	p.record("oci " + ref)
	switch ref {
	case "ghcr.io/giantswarm/skills/kubectl:1.4.0", "ghcr.io/giantswarm/skills/kubectl@" + Digest:
		return "ghcr.io/giantswarm/skills/kubectl@" + Digest, nil
	}
	if strings.Contains(ref, "@sha256:") {
		return "", fmt.Errorf("%w: %w: OCI skill %s: registry host not allowed", skills.ErrUnresolvable, skills.ErrNotAllowed, ref)
	}
	return "", fmt.Errorf("%w: %w: OCI skill %s", skills.ErrUnresolvable, skills.ErrNotFound, ref)
}

func (p *Pinner) RequireReadable(_ context.Context, repoURL, login string) error {
	p.record("readable " + repoURL + " " + login)
	if repoURL == PrivateRepo && login != "alice" {
		return fmt.Errorf("%w: %s is private and %q cannot read it", skills.ErrNotReadable, repoURL, login)
	}
	if repoURL == MissingRepo {
		return fmt.Errorf("%w: %w: %s: GitHub answered 404", skills.ErrUnresolvable, skills.ErrNotFound, repoURL)
	}
	return nil
}

func (p *Pinner) ClaudeFormatPlugin(_ context.Context, _, _, path string) (bool, error) {
	return strings.HasSuffix(path, "claude"), nil
}

func (p *Pinner) PathExists(_ context.Context, repoURL, commit, path string) (bool, error) {
	p.record("path " + repoURL + "@" + commit + ":" + path)
	return !strings.Contains(path, "missing"), nil
}

// GitCalls are the GitHead lookups made, sorted.
func (p *Pinner) GitCalls() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []string
	for _, c := range p.calls {
		if strings.HasPrefix(c, "git ") {
			out = append(out, c)
		}
	}
	sort.Strings(out)
	return out
}

// Lister is the discoverer over a fixed listing: its repositories are the
// configured sources, their commit is the head, their error the head's.
type Lister struct {
	Repos []skills.Repository
	// Configured, when not nil, are the configured sources; else Repos are.
	Configured []skills.Source
	// Observe, when set, sees every List's repository and refresh.
	Observe func(repository string, refresh bool)
}

func (l Lister) List(_ context.Context, repository, _ string, refresh bool) (*skills.Result, error) {
	if l.Observe != nil {
		l.Observe(repository, refresh)
	}
	out := &skills.Result{Repositories: []skills.Repository{}}
	for _, r := range l.Repos {
		if repository == "" || r.RepoURL == repository {
			out.Repositories = append(out.Repositories, r)
		}
	}
	return out, nil
}

// Calls are every lookup made, in order.
func (p *Pinner) Calls() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.calls...)
}

// SourceRef is no configured source's ref.
func (l Lister) SourceRef(string, string) string { return "" }

// Sources are the listing's repositories.
func (l Lister) Sources() []skills.Source {
	if l.Configured != nil {
		return l.Configured
	}
	out := make([]skills.Source, 0, len(l.Repos))
	for _, r := range l.Repos {
		out = append(out, skills.Source{URL: r.RepoURL, Ref: r.Ref, Path: r.Path, ReadOnly: r.ReadOnly})
	}
	return out
}

// Head is the listed repository's commit, or its error.
func (l Lister) Head(_ context.Context, src skills.Source) (skills.Head, error) {
	for _, r := range l.Repos {
		if r.RepoURL == src.URL {
			if r.Error != "" {
				return skills.Head{}, fmt.Errorf("%s", r.Error)
			}
			return skills.Head{Commit: r.Commit, Private: r.Private}, nil
		}
	}
	return skills.Head{}, fmt.Errorf("%w: %s", skills.ErrNotFound, src.URL)
}

// CommitExists knows the fake's three commits.
func (p *Pinner) CommitExists(_ context.Context, repoURL, commit string) error {
	p.record("commit " + repoURL + "@" + commit)
	switch commit {
	case MainHead, FeatureHead, TagHead:
		return nil
	}
	return fmt.Errorf("%w: %w: commit %s of %s", skills.ErrUnresolvable, skills.ErrNotFound, commit, repoURL)
}

// Holds reports whether one of the listing's repositories holds dir below
// its path.
func (l Lister) Holds(repoURL, dir string) bool {
	for _, r := range l.Repos {
		if r.RepoURL == repoURL && (r.Path == "" || dir == r.Path || strings.HasPrefix(dir, r.Path+"/")) {
			return true
		}
	}
	return false
}

// Skill answers a skill named after its directory; a dir containing
// "missing" does not exist.
func (l Lister) Skill(_ context.Context, repoURL, commit, dir string) (*skills.SkillContent, error) {
	if strings.Contains(dir, "missing") {
		return nil, fmt.Errorf("%w: %s at %s", skills.ErrNotFound, dir, commit)
	}
	return &skills.SkillContent{RepoURL: repoURL, Path: dir, Commit: commit, Name: path.Base(dir), Files: []skills.File{{Path: "SKILL.md"}}}, nil
}

// SkillFile answers SKILL.md of any skill Skill answers.
func (l Lister) SkillFile(ctx context.Context, repoURL, commit, dir, file string) (*skills.FileContent, error) {
	if _, err := l.Skill(ctx, repoURL, commit, dir); err != nil {
		return nil, err
	}
	if file != "SKILL.md" {
		return nil, fmt.Errorf("%w: %s", skills.ErrNotFound, file)
	}
	text := "---\nname: x\n---\n"
	return &skills.FileContent{File: skills.File{Path: file}, Content: &text}, nil
}
