// Package catalogtest holds the catalog's fakes for tests: a Pinner that
// answers like GitHub and a registry would, and a Lister over a fixed
// listing.
package catalogtest

import (
	"context"
	"fmt"
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
	if ref == "ghcr.io/giantswarm/skills/kubectl:1.4.0" {
		return "ghcr.io/giantswarm/skills/kubectl@" + Digest, nil
	}
	return "", fmt.Errorf("%w: %w: OCI skill %s", skills.ErrUnresolvable, skills.ErrNotFound, ref)
}

func (p *Pinner) RequireReadable(_ context.Context, repoURL, login string) error {
	p.record("readable " + repoURL + " " + login)
	if repoURL == PrivateRepo && login != "alice" {
		return fmt.Errorf("%w: %s is private and %q cannot read it", skills.ErrNotReadable, repoURL, login)
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

// Lister is the discoverer over a fixed listing.
type Lister struct {
	*skills.Discoverer
	Repos []skills.Repository
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
