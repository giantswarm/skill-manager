package skills

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/giantswarm/skill-manager/internal/oci"
)

// Immutable skill sources as the api.kagent.dev/v1alpha3 template requires
// them: a full git commit id, an OCI reference by digest.
var (
	// CommitPattern is a full commit id (40 or 64 hex characters).
	CommitPattern = regexp.MustCompile(`^([0-9a-fA-F]{40}|[0-9a-fA-F]{64})$`)
	// DigestRefPattern is <repository>@sha256:<digest>.
	DigestRefPattern = regexp.MustCompile(`^[^\s@]+@sha256:[0-9a-f]{64}$`)
)

// ErrUnresolvable marks a reference that could not be resolved to a pin; the
// message names the repository or image and the reason.
var ErrUnresolvable = errors.New("unresolvable skill reference")

// ErrNotFound marks an ErrUnresolvable whose repository, ref, path or image
// the source does not know; ErrUpstream one the source failed to answer.
var (
	ErrNotFound = errors.New("not found")
	ErrUpstream = errors.New("upstream failure")
	// ErrNotAllowed marks an ErrUnresolvable whose source skill-manager may
	// not ask (an OCI registry off the allowlist).
	ErrNotAllowed = errors.New("not allowed")
)

// Resolver pins skill references: a git branch or tag to its head commit, a
// repository to its default branch, an OCI tag to its digest. It is the
// composer's path to immutable sources and the migration's.
type Resolver struct {
	gh       *github
	registry *oci.Registry

	mu     sync.Mutex
	access map[string]accessEntry
}

// ErrNotReadable marks a private skill repository the person asking cannot
// read: no agent of theirs may reference it.
var ErrNotReadable = errors.New("skill repository not readable")

// NewResolver builds a resolver for the GitHub API at apiURL (tokens nil for
// anonymous) and the OCI registries registry allows; nil allows
// oci.DefaultHosts.
func NewResolver(apiURL string, tokens TokenSource, client *http.Client, registry *oci.Registry) *Resolver {
	if registry == nil {
		registry = oci.NewRegistry(nil, nil)
	}
	return &Resolver{gh: newGitHub(apiURL, tokens, client), registry: registry, access: map[string]accessEntry{}}
}

// accessTTL is how long a person's answer for a repository is reused.
const accessTTL = 5 * time.Minute

type accessEntry struct {
	err   error
	until time.Time
}

// RequireReadable refuses repoURL unless login can read it. A public
// repository is readable by everyone; a private one needs login's read
// permission or more, which GitHub reports for the configured token's view of
// the repository. An empty login (a caller without a verified GitHub
// identity) can use public repositories only.
func (r *Resolver) RequireReadable(ctx context.Context, repoURL, login string) error {
	owner, repo, err := parseRepoURL(repoURL)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnresolvable, err)
	}
	key := strings.ToLower(login + " " + owner + "/" + repo)
	r.mu.Lock()
	e, ok := r.access[key]
	r.mu.Unlock()
	if ok && time.Now().Before(e.until) {
		return e.err
	}
	err = r.readable(ctx, repoURL, owner, repo, login)
	if err != nil && !errors.Is(err, ErrNotReadable) {
		return err // a failed lookup is not cached
	}
	r.mu.Lock()
	r.access[key] = accessEntry{err: err, until: time.Now().Add(accessTTL)}
	r.mu.Unlock()
	return err
}

func (r *Resolver) readable(ctx context.Context, repoURL, owner, repo, login string) error {
	meta, err := r.gh.repository(ctx, owner, repo)
	if err != nil {
		return r.unresolvable(ctx, repoURL, "the visibility", err)
	}
	if !meta.Private {
		return nil
	}
	if login == "" {
		return fmt.Errorf("%w: %s is private and your GitHub identity is unknown, so skill-manager cannot check that you may read it; use it through muster with skill-manager connected to GitHub (core_auth_login server=skill-manager)", ErrNotReadable, repoURL)
	}
	perm, err := r.gh.permission(ctx, owner, repo, login)
	if err != nil {
		return fmt.Errorf("%w: %w: the permission of %s on %s could not be read: %v", ErrUnresolvable, ErrUpstream, login, repoURL, err)
	}
	switch perm {
	case "admin", "maintain", "write", "triage", "read":
		return nil
	}
	return fmt.Errorf("%w: %s is private and the GitHub user %s cannot read it (permission %s); ask its owners for read access", ErrNotReadable, repoURL, login, perm)
}

// GitHead resolves ref (a branch, tag or commit; empty for the default
// branch) of repoURL to the commit it points at. A ref that is already a full
// commit id is returned as it is without a lookup. A repository that cannot be
// read — private without a token, or missing — is ErrUnresolvable naming it.
func (r *Resolver) GitHead(ctx context.Context, repoURL, ref string) (string, error) {
	if CommitPattern.MatchString(ref) {
		return ref, nil
	}
	owner, repo, err := parseRepoURL(repoURL)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrUnresolvable, err)
	}
	if ref == "" {
		if ref, err = r.gh.defaultBranch(ctx, owner, repo); err != nil {
			return "", r.unresolvable(ctx, repoURL, "its default branch", err)
		}
	}
	sha, err := r.gh.headCommit(ctx, owner, repo, ref)
	if err != nil {
		return "", r.unresolvable(ctx, repoURL, "ref "+ref, err)
	}
	return sha, nil
}

// ClaudeFormatPlugin reports whether the plugin at path of repoURL at commit
// is Claude-format: a .claude-plugin/plugin.json manifest and no root
// plugin.json, the rule kagent's plugin materializer applies. A plugin with
// neither manifest is not Claude-format.
func (r *Resolver) ClaudeFormatPlugin(ctx context.Context, repoURL, commit, path string) (bool, error) {
	owner, repo, err := parseRepoURL(repoURL)
	if err != nil {
		return false, fmt.Errorf("%w: %v", ErrUnresolvable, err)
	}
	for _, manifest := range []string{"plugin.json", ".claude-plugin/plugin.json"} {
		_, err := r.gh.raw(ctx, owner, repo, joinPluginPath(path, manifest), commit)
		var api *apiError
		switch {
		case err == nil:
			return manifest != "plugin.json", nil
		case errors.As(err, &api) && api.status == http.StatusNotFound:
			continue
		default:
			return false, r.unresolvable(ctx, repoURL, "the plugin manifest at "+commit, err)
		}
	}
	return false, nil
}

// PathExists reports whether repoURL has a file or a directory at path at
// commit; "" is the repository root, which always exists.
func (r *Resolver) PathExists(ctx context.Context, repoURL, commit, path string) (bool, error) {
	owner, repo, err := parseRepoURL(repoURL)
	if err != nil {
		return false, fmt.Errorf("%w: %v", ErrUnresolvable, err)
	}
	path = strings.Trim(path, "/")
	if path == "" {
		return true, nil
	}
	// For a directory GitHub answers its listing whatever the Accept.
	_, err = r.gh.raw(ctx, owner, repo, path, commit)
	var api *apiError
	switch {
	case err == nil:
		return true, nil
	case errors.As(err, &api) && api.status == http.StatusNotFound:
		return false, nil
	}
	return false, r.unresolvable(ctx, repoURL, "path "+path+" at "+commit, err)
}

func joinPluginPath(dir, file string) string {
	dir = strings.Trim(dir, "/")
	if dir == "" {
		return file
	}
	return dir + "/" + file
}

// unresolvable explains a failed GitHub lookup: a 404 is a private or missing
// repository, which without a token cannot be told apart, and a 422 a ref
// GitHub does not know; both are ErrNotFound, every other failure
// ErrUpstream.
func (r *Resolver) unresolvable(ctx context.Context, repoURL, what string, err error) error {
	var api *apiError
	switch {
	case errors.As(err, &api) && api.status == http.StatusNotFound:
		if token, _ := r.gh.tokens.Token(ctx); token == "" {
			return fmt.Errorf("%w: %w: %s of %s could not be resolved: GitHub answered 404 without a token — the repository is private or does not exist; a private skill repository needs the skills GitHub App installed on it (or a GITHUB_TOKEN that can read it)", ErrUnresolvable, ErrNotFound, what, repoURL)
		}
		return fmt.Errorf("%w: %w: %s of %s could not be resolved: GitHub answered 404 — the repository does not exist, the ref is unknown, or the skills GitHub App is not installed on it", ErrUnresolvable, ErrNotFound, what, repoURL)
	case errors.As(err, &api) && api.status == http.StatusUnprocessableEntity:
		return fmt.Errorf("%w: %w: %s of %s could not be resolved: %v", ErrUnresolvable, ErrNotFound, what, repoURL, err)
	}
	return fmt.Errorf("%w: %w: %s of %s could not be resolved: %v", ErrUnresolvable, ErrUpstream, what, repoURL, err)
}

// OCIDigest pins an OCI reference: one already carrying a digest is returned
// in its pinned form; a tagged one (no tag: latest) is resolved against the
// registry.
func (r *Resolver) OCIDigest(ctx context.Context, ref string) (string, error) {
	img, err := oci.ParseImageReference(ref)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrUnresolvable, err)
	}
	if img.Digest != "" {
		return img.Pinned(), nil
	}
	tag := img.Tag
	if tag == "" {
		tag = "latest"
	}
	digest, err := r.registry.ManifestDigest(ctx, img.Reference, tag)
	if err != nil {
		cause := ErrUpstream
		switch {
		case errors.Is(err, oci.ErrNotFound):
			cause = ErrNotFound
		case errors.Is(err, oci.ErrHostNotAllowed):
			cause = ErrNotAllowed
		}
		return "", fmt.Errorf("%w: %w: OCI skill %s could not be resolved to a digest: %v", ErrUnresolvable, cause, ref, err)
	}
	img.Digest = digest
	return img.Pinned(), nil
}
