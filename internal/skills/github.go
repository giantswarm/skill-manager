package skills

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// github is the GitHub REST client the discoverer and the resolver share: one
// base URL (tests override it), one token source, one HTTP client.
type github struct {
	apiURL string
	tokens TokenSource
	http   *http.Client
}

func newGitHub(apiURL string, tokens TokenSource, client *http.Client) *github {
	if apiURL == "" {
		apiURL = "https://api.github.com"
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	if tokens == nil {
		tokens = StaticToken("")
	}
	return &github{apiURL: strings.TrimRight(apiURL, "/"), tokens: tokens, http: client}
}

// repoURLRe takes the characters GitHub allows in an owner and a repository
// name only: both go into API paths, which must not carry a query, a
// fragment, an escape or a dot segment.
var repoURLRe = regexp.MustCompile(`^https://github\.com/([A-Za-z0-9][A-Za-z0-9-]*)/([A-Za-z0-9._-]+?)(?:\.git)?/?$`)

// parseRepoURL splits https://github.com/<owner>/<repo>.
func parseRepoURL(repoURL string) (owner, repo string, err error) {
	m := repoURLRe.FindStringSubmatch(strings.TrimSpace(repoURL))
	if m == nil || m[2] == "." || m[2] == ".." {
		return "", "", fmt.Errorf("not a github.com repository URL: %s (expected https://github.com/<owner>/<repo>)", repoURL)
	}
	return m[1], m[2], nil
}

// canonicalRepoURL is https://github.com/<owner>/<repo> without .git or a
// trailing slash.
func canonicalRepoURL(owner, repo string) string {
	return "https://github.com/" + owner + "/" + repo
}

// repoMeta is what the repository endpoint says about a repository.
type repoMeta struct {
	DefaultBranch string `json:"default_branch"`
	Private       bool   `json:"private"`
}

// repository reads the repository's default branch and visibility.
func (g *github) repository(ctx context.Context, owner, repo string) (repoMeta, error) {
	var meta repoMeta
	if err := g.getJSON(ctx, fmt.Sprintf("%s/repos/%s/%s", g.apiURL, owner, repo), &meta); err != nil {
		return repoMeta{}, err
	}
	if meta.DefaultBranch == "" {
		meta.DefaultBranch = "main"
	}
	return meta, nil
}

// permission is login's permission on the repository (admin, write, read or
// none); a login GitHub does not know as a collaborator is none.
func (g *github) permission(ctx context.Context, owner, repo, login string) (string, error) {
	var out struct {
		Permission string `json:"permission"`
	}
	err := g.getJSON(ctx, fmt.Sprintf("%s/repos/%s/%s/collaborators/%s/permission", g.apiURL, owner, repo, url.PathEscape(login)), &out)
	var api *apiError
	if errors.As(err, &api) && api.status == http.StatusNotFound {
		return "none", nil
	}
	if err != nil {
		return "", err
	}
	return out.Permission, nil
}

// readableBy asks GitHub whether the holder of userToken can read the
// repository: GET /repos/{owner}/{repo} as them answers 200 when they can.
func (g *github) readableBy(ctx context.Context, owner, repo, userToken string) error {
	_, err := g.send(ctx, http.MethodGet, fmt.Sprintf("%s/repos/%s/%s", g.apiURL, owner, repo), "application/vnd.github+json", userToken)
	return err
}

// head resolves ref to the commit it points at right now: empty is the
// default branch (read from meta, else from the repository), a branch or a
// tag its head, a commit id itself, once GitHub confirms it exists. The one
// place a ref becomes a commit.
func (g *github) head(ctx context.Context, owner, repo, ref string, meta *repoMeta) (string, error) {
	if ref == "" {
		if meta == nil {
			m, err := g.repository(ctx, owner, repo)
			if err != nil {
				return "", err
			}
			meta = &m
		}
		ref = meta.DefaultBranch
	}
	return g.headCommit(ctx, owner, repo, ref)
}

// headCommit resolves a ref (branch, tag or commit) to the commit it points
// at right now: the sha media type answers the id alone, not the commit. A
// commit id GitHub does not have is its 404 or 422.
func (g *github) headCommit(ctx context.Context, owner, repo, ref string) (string, error) {
	if err := CheckRef(ref); err != nil {
		return "", err
	}
	body, err := g.get(ctx, fmt.Sprintf("%s/repos/%s/%s/commits/%s", g.apiURL, owner, repo, escapeSegments(ref)), "application/vnd.github.sha")
	if err != nil {
		return "", err
	}
	sha := strings.TrimSpace(string(body))
	if !CommitPattern.MatchString(sha) {
		return "", fmt.Errorf("GitHub reported no commit for %s/%s@%s", owner, repo, ref)
	}
	return sha, nil
}

// exists asks whether path is a file or a directory at ref with a HEAD of
// the contents endpoint: no file body is transferred.
func (g *github) exists(ctx context.Context, owner, repo, path, ref string) (bool, error) {
	_, err := g.do(ctx, http.MethodHead, g.contentsURL(owner, repo, path, ref), "application/vnd.github.raw+json")
	var api *apiError
	switch {
	case err == nil:
		return true, nil
	case errors.As(err, &api) && api.status == http.StatusNotFound:
		return false, nil
	}
	return false, err
}

func (g *github) contentsURL(owner, repo, path, ref string) string {
	return fmt.Sprintf("%s/repos/%s/%s/contents/%s?ref=%s", g.apiURL, owner, repo, escapeSegments(path), url.QueryEscape(ref))
}

// escapeSegments escapes each segment of a slash-separated path or ref:
// GitHub reads release/v1 in a path, not release%2Fv1.
func escapeSegments(p string) string {
	segments := strings.Split(p, "/")
	for i, s := range segments {
		segments[i] = url.PathEscape(s)
	}
	return strings.Join(segments, "/")
}

func (g *github) getJSON(ctx context.Context, target string, out any) error {
	body, err := g.get(ctx, target, "application/vnd.github+json")
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode %s: %w", target, err)
	}
	return nil
}

func (g *github) raw(ctx context.Context, owner, repo, path, ref string) (string, error) {
	body, err := g.get(ctx, g.contentsURL(owner, repo, path, ref), "application/vnd.github.raw+json")
	return string(body), err
}

// apiError is a non-200 answer of the GitHub API, kept so callers can tell a
// missing or private repository (404) from other failures.
type apiError struct {
	status int
	target string
	body   string
	// rateLimited is a GitHub rate limit, not the caller's access.
	rateLimited bool
}

func (e *apiError) Error() string {
	return fmt.Sprintf("GitHub API %s returned %d: %s", e.target, e.status, e.body)
}

func (g *github) get(ctx context.Context, target, accept string) ([]byte, error) {
	return g.do(ctx, http.MethodGet, target, accept)
}

func (g *github) do(ctx context.Context, method, target, accept string) ([]byte, error) {
	token, err := g.tokens.Token(ctx)
	if err != nil {
		return nil, err
	}
	return g.send(ctx, method, target, accept, token)
}

// send is one request authenticated with token ("" for anonymous).
func (g *github) send(ctx context.Context, method, target, accept, token string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := g.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, target, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, target, err)
	}
	if resp.StatusCode != http.StatusOK {
		message := strings.TrimSpace(firstLine(string(body)))
		return nil, &apiError{status: resp.StatusCode, target: target, body: message, rateLimited: rateLimited(resp, message)}
	}
	return body, nil
}

// maxErrorBody bounds what of an upstream answer reaches an error.
const maxErrorBody = 200

// firstLine is s's first line, at most maxErrorBody bytes of it.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > maxErrorBody {
		s = s[:maxErrorBody]
	}
	return s
}

// rateLimited reports a GitHub rate limit: a 429, or a 403 with no requests
// left, a Retry-After header (the secondary limit) or a rate limit message.
func rateLimited(resp *http.Response, message string) bool {
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return true
	case resp.StatusCode != http.StatusForbidden:
		return false
	}
	return resp.Header.Get("X-RateLimit-Remaining") == "0" || resp.Header.Get("Retry-After") != "" || strings.Contains(strings.ToLower(message), "rate limit")
}
