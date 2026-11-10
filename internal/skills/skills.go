// Package skills discovers agent skills in git repositories the way the
// portal backend's /agent-skills endpoint does: every SKILL.md in a GitHub
// repository is one skill, its frontmatter name and description describe it,
// and its directory is the skill an agent mounts (url + path). kagent API v2
// pins skills to immutable sources, so the ref that was read is resolved to
// its head commit and reported on the repository: the commit a skills entry
// of create_agent pins. Results are cached per repository for a short time
// so a meta agent listing skills repeatedly does not exhaust GitHub's rate
// limit. The Resolver (resolve.go) is the same GitHub path for the composer:
// a branch or tag to its head commit, a repository to its default branch.
package skills

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
	"sigs.k8s.io/yaml"

	"github.com/giantswarm/skill-manager/internal/lru"
	"github.com/giantswarm/skill-manager/internal/tags"
)

// Skill is one discovered skill. Its repository carries the URL, the ref and
// the commit it was read at.
type Skill struct {
	// Name is the frontmatter name, else the directory (or repository) name.
	Name string `json:"name"`
	// Description is the frontmatter description ("" when absent).
	Description string `json:"description"`
	// Path is the skill directory; "" at the repository root.
	Path string `json:"path"`
	// Tags are the frontmatter's metadata.tags, comma-joined there; a tag
	// the agent chart's tag rule refuses is dropped.
	Tags []string `json:"tags,omitempty"`
}

// Repository is the discovery result of one configured repository.
type Repository struct {
	RepoURL string `json:"repoUrl"`
	Ref     string `json:"ref,omitempty"`
	// Path is the directory the skills were discovered below; "" for the
	// whole repository. Skill paths stay repository-relative.
	Path string `json:"path,omitempty"`
	// ReadOnly is the source's readOnly, reported for consumers that write
	// skills; skill-manager has no write path into skill repositories.
	ReadOnly bool `json:"readOnly,omitempty"`
	// Commit is what Ref resolved to when the repository was read.
	Commit string  `json:"commit,omitempty"`
	Skills []Skill `json:"skills"`
	// Plugins are the plugin directories found in the same read.
	Plugins []Plugin `json:"plugins"`
	// Private is true for a repository only its collaborators can read:
	// list_skills shows it only to a caller who can read it.
	Private bool `json:"private,omitempty"`
	// Truncated is true when GitHub capped the tree, the repository holds
	// more than maxSkillFiles SKILL.md files, or a SKILL.md read failed: some
	// skills may be missing.
	Truncated bool `json:"truncated"`
	// Error is set when the repository could not be read at all.
	Error string `json:"error,omitempty"`
	// Stale is true when the last read failed (Error) and the skills,
	// commit and FetchedAt are those of the last good read.
	Stale     bool       `json:"stale,omitempty"`
	FetchedAt *time.Time `json:"fetchedAt,omitempty"`
}

// Result is what list_skills returns: every skill once, under its repository.
type Result struct {
	Repositories []Repository `json:"repositories"`
}

// Config tunes the discoverer and the resolver.
type Config struct {
	// Repositories are the configured skill repositories (github.com URLs,
	// each with an optional ref, path and readOnly).
	Repositories []Source
	// APIURL is the GitHub API base (https://api.github.com; tests override).
	APIURL string
	// Tokens authenticates GitHub requests (private repositories, higher
	// rate limit): a GitHub App's installation tokens or a StaticToken; nil
	// is anonymous.
	Tokens TokenSource
	// CacheTTL keeps a repository's result before it is re-read.
	CacheTTL time.Duration
	// ErrorTTL keeps a failed read before it is retried (default 30s).
	ErrorTTL time.Duration
	// HTTPClient overrides the client (tests).
	HTTPClient *http.Client
	// Tags is the agent chart's tag rule, which a skill's tags follow as an
	// agent's do; nil: skills carry no tags.
	Tags *tags.Rule
}

// Discoverer reads skills from GitHub with a per-repository cache.
type Discoverer struct {
	cfg Config
	gh  *github
	log *slog.Logger

	// cache holds the listings of repositories a caller names; own those of
	// the configured sources, which no caller can evict.
	cache *lru.Cache[string, cacheEntry]
	own   *lru.Cache[string, cacheEntry]
	heads *lru.Cache[string, headEntry]

	contents     *lru.Cache[string, skillEntry]
	contentSlots chan struct{}
	reads        singleflight.Group
}

// cacheMax bounds the discovery and head caches: a source named in a
// request may be any github.com repository.
const cacheMax = 512

// defaultErrorTTL is how long a failed read is answered before it is
// retried.
const defaultErrorTTL = 30 * time.Second

// cacheEntry is a repository's listing until expires, and its last good
// listing.
type cacheEntry struct {
	repo    Repository
	expires time.Time
	good    *Repository
}

// Head is what a source's ref resolves to now.
type Head struct {
	Commit  string
	Private bool
}

type headEntry struct {
	head    Head
	err     error
	expires time.Time
}

// New builds a discoverer.
func New(cfg Config, log *slog.Logger) *Discoverer {
	if cfg.CacheTTL <= 0 {
		cfg.CacheTTL = 5 * time.Minute
	}
	if cfg.ErrorTTL <= 0 {
		cfg.ErrorTTL = defaultErrorTTL
	}
	if log == nil {
		log = slog.Default()
	}
	return &Discoverer{cfg: cfg, gh: newGitHub(cfg.APIURL, cfg.Tokens, cfg.HTTPClient), log: log,
		cache: lru.New[string, cacheEntry](cacheMax), own: lru.New[string, cacheEntry](len(cfg.Repositories)),
		heads:    lru.New[string, headEntry](cacheMax),
		contents: lru.New[string, skillEntry](cacheMax), contentSlots: make(chan struct{}, contentReadSlots)}
}

// cacheFor is the cache of src's listing: the configured sources' own, or
// the one of the repositories callers name.
func (d *Discoverer) cacheFor(src Source) *lru.Cache[string, cacheEntry] {
	key := cacheKey(src)
	for _, c := range d.cfg.Repositories {
		if cacheKey(c) == key {
			return d.own
		}
	}
	return d.cache
}

// minRefresh is how old a listing must be before refresh reads it again:
// repeated refreshes cannot spend the GitHub rate limit. A variable for the
// tests.
var minRefresh = 10 * time.Second

// cacheKey is a source's key: its canonical repository URL, ref and path.
func cacheKey(src Source) string {
	return canonicalOrRaw(src.URL) + "@" + src.Ref + "#" + src.Path
}

// Head resolves src's ref (empty: the default branch) to its commit, with
// the repository's visibility, without reading its skills. The answer
// depends on the repository and ref only and is cached for the cache TTL, a
// failure for the error TTL.
func (d *Discoverer) Head(ctx context.Context, src Source) (Head, error) {
	key := canonicalOrRaw(src.URL) + "@" + src.Ref
	if e, ok := d.heads.Get(key); ok && time.Now().Before(e.expires) {
		return e.head, e.err
	}
	v, err, _ := d.reads.Do("head "+key, func() (any, error) {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), readTimeout)
		defer cancel()
		h, err := d.readHead(ctx, src)
		ttl := d.cfg.CacheTTL
		if err != nil {
			ttl = d.cfg.ErrorTTL
		}
		d.heads.Add(key, headEntry{head: h, err: err, expires: time.Now().Add(ttl)})
		return h, err
	})
	return v.(Head), err
}

func (d *Discoverer) readHead(ctx context.Context, src Source) (Head, error) {
	owner, name, err := parseRepoURL(src.URL)
	if err != nil {
		return Head{}, err
	}
	meta, err := d.gh.repository(ctx, owner, name)
	if err != nil {
		return Head{}, err
	}
	commit, err := d.gh.head(ctx, owner, name, src.Ref, &meta)
	if err != nil {
		return Head{}, err
	}
	return Head{Commit: commit, Private: meta.Private}, nil
}

// Sources returns the configured repositories.
func (d *Discoverer) Sources() []Source { return append([]Source(nil), d.cfg.Repositories...) }

// configured returns the configured sources of repoURL, compared by their
// canonical URL.
func (d *Discoverer) configured(repoURL string) []Source {
	owner, name, err := parseRepoURL(repoURL)
	if err != nil {
		return nil
	}
	want := strings.ToLower(canonicalRepoURL(owner, name))
	var out []Source
	for _, s := range d.cfg.Repositories {
		if o, n, err := parseRepoURL(s.URL); err == nil && strings.ToLower(canonicalRepoURL(o, n)) == want {
			out = append(out, s)
		}
	}
	return out
}

// SourceRef is the ref of the configured source a skill at path of repoURL
// comes from (the deepest source path holding it); "" when no configured
// source holds it or that source follows the default branch.
func (d *Discoverer) SourceRef(repoURL, path string) string {
	path = strings.Trim(path, "/")
	best, ref := -1, ""
	for _, s := range d.configured(repoURL) {
		if (path == s.Path || within(path, s.Path)) && len(s.Path) > best {
			best, ref = len(s.Path), s.Ref
		}
	}
	return ref
}

// List discovers skills. repository narrows to one repository (configured or
// not — any github.com URL is accepted; a configured one keeps its ref and
// path); ref overrides the source's ref (default: the default branch);
// refresh re-reads a listing older than minRefresh.
func (d *Discoverer) List(ctx context.Context, repository, ref string, refresh bool) (*Result, error) {
	sources := d.cfg.Repositories
	if repository != "" {
		if _, _, err := parseRepoURL(repository); err != nil {
			return nil, err
		}
		sources = d.configured(repository)
		if len(sources) == 0 {
			sources = []Source{{URL: repository}}
		}
	}
	found := make([]Repository, len(sources))
	var wg sync.WaitGroup
	for i, src := range sources {
		if ref != "" {
			src.Ref = ref
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			found[i] = d.discover(ctx, src, refresh)
		}()
	}
	wg.Wait()
	return &Result{Repositories: found}, nil
}

func (d *Discoverer) discover(ctx context.Context, src Source, refresh bool) Repository {
	key := cacheKey(src)
	cached, ok := d.cacheFor(src).Get(key)
	if ok && time.Now().Before(cached.expires) {
		recent := cached.repo.FetchedAt != nil && time.Since(*cached.repo.FetchedAt) < minRefresh
		if !refresh || recent {
			return cached.repo
		}
	}
	// One read per source at a time: concurrent callers share it.
	v, _, _ := d.reads.Do("read "+key, func() (any, error) {
		return d.reread(ctx, key, src, cached, ok && !refresh), nil
	})
	return v.(Repository)
}

// reread reads src again; renew allows an expired good entry whose ref
// still points at the same commit to be renewed with two requests instead
// of re-reading every SKILL.md. A failed read keeps the last good listing,
// stale, and is retried after the error TTL.
func (d *Discoverer) reread(ctx context.Context, key string, src Source, cached cacheEntry, renew bool) Repository {
	repoURL, ref := src.URL, src.Ref
	// The read outlives the caller: a client that gives up (muster answers
	// after 30 s) must not leave a half-read listing in the cache, and the
	// next call finds the finished one.
	readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), readTimeout)
	defer cancel()
	if renew && cached.good != nil && cached.repo.Error == "" && !cached.repo.Truncated && cached.repo.Commit != "" {
		if head, err := d.headOf(readCtx, repoURL, ref); err == nil && head == cached.repo.Commit {
			now := time.Now()
			repo := cached.repo
			repo.FetchedAt = &now
			d.cacheFor(src).Add(key, cacheEntry{repo: repo, expires: now.Add(d.cfg.CacheTTL), good: &repo})
			return repo
		}
	}

	repo, err := d.read(readCtx, src)
	failed := repo
	now := time.Now()
	if err == nil {
		repo.FetchedAt = &now
		d.cacheFor(src).Add(key, cacheEntry{repo: repo, expires: now.Add(d.cfg.CacheTTL), good: &repo})
		return repo
	}
	d.log.Warn("skill discovery failed", "repository", repoURL, "error", err)
	if prev, ok := d.cacheFor(src).Get(key); ok && prev.good != nil {
		cached = prev
	}
	if cached.good != nil {
		repo = *cached.good
		repo.Stale = true
	} else {
		repo = Repository{RepoURL: repoURL, Ref: ref, Path: src.Path, ReadOnly: src.ReadOnly, Skills: []Skill{}, Plugins: []Plugin{}, FetchedAt: &now}
	}
	repo.Private = repo.Private || failed.Private
	repo.Error = err.Error()
	d.cacheFor(src).Add(key, cacheEntry{repo: repo, expires: now.Add(d.cfg.ErrorTTL), good: cached.good})
	return repo
}

const skillFile = "SKILL.md"

// readTimeout bounds one repository read; readParallelism bounds the SKILL.md
// requests in flight per repository.
const (
	readTimeout     = 2 * time.Minute
	readParallelism = 8
)

// maxSkillFiles bounds the SKILL.md files one read fetches: each is a GitHub
// request at the App's rate limit, and a caller may name any repository. A
// variable for the tests.
var maxSkillFiles = 500

// headOf resolves ref (the default branch when empty) to its head commit.
func (d *Discoverer) headOf(ctx context.Context, repoURL, ref string) (string, error) {
	owner, name, err := parseRepoURL(repoURL)
	if err != nil {
		return "", err
	}
	return d.gh.head(ctx, owner, name, ref, nil)
}

func isSkillFile(path string) bool {
	return path == skillFile || strings.HasSuffix(path, "/"+skillFile)
}

// within reports whether path lies below dir ("" is the whole repository).
func within(path, dir string) bool {
	return dir == "" || strings.HasPrefix(path, dir+"/")
}

func skillDir(path string) string {
	if path == skillFile {
		return ""
	}
	return strings.TrimSuffix(path, "/"+skillFile)
}

func (d *Discoverer) read(ctx context.Context, src Source) (Repository, error) {
	owner, name, err := parseRepoURL(src.URL)
	if err != nil {
		return Repository{}, err
	}
	canonical := canonicalRepoURL(owner, name)
	meta, err := d.gh.repository(ctx, owner, name)
	if err != nil {
		return Repository{}, err
	}
	branch := src.Ref
	if branch == "" {
		branch = meta.DefaultBranch
	}
	// From here on the visibility is known: a failed read still says
	// whether the repository is private.
	failed := Repository{Private: meta.Private}
	// The commit the ref points at right now: what an agent pins. The tree
	// and the files are read at that commit, not the branch, so the listing
	// stays consistent even when the branch moves between the calls.
	head, err := d.gh.head(ctx, owner, name, branch, &meta)
	if err != nil {
		return failed, err
	}
	tree, err := d.tree(ctx, owner, name, head, src.Path)
	if err != nil {
		return failed, err
	}
	repo := Repository{RepoURL: canonical, Ref: branch, Path: src.Path, ReadOnly: src.ReadOnly, Commit: head, Private: meta.Private, Skills: []Skill{}, Truncated: tree.Truncated}
	if tree.Truncated {
		d.log.Warn("skill repository tree truncated by GitHub: some skills are missing from the listing; give the source a path to narrow it", "repository", canonical, "path", src.Path, "commit", head)
	}
	var paths, files []string
	for _, entry := range tree.Tree {
		if entry.Type != "blob" {
			continue
		}
		files = append(files, entry.Path)
		if isSkillFile(entry.Path) {
			paths = append(paths, entry.Path)
		}
	}
	if len(paths) > maxSkillFiles {
		d.log.Warn("skill repository holds more SKILL.md files than one read fetches: some skills are missing from the listing; give the source a path to narrow it", "repository", canonical, "path", src.Path, "files", len(paths), "max", maxSkillFiles)
		paths = paths[:maxSkillFiles]
		repo.Truncated = true
	}
	contents := make([]string, len(paths))
	errs := make([]error, len(paths))
	sem := make(chan struct{}, readParallelism)
	var wg sync.WaitGroup
	for i, path := range paths {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			contents[i], errs[i] = d.gh.raw(ctx, owner, name, path, head)
		}()
	}
	wg.Wait()
	for i, path := range paths {
		if errs[i] != nil {
			d.log.Warn("skipping unreadable SKILL.md", "repository", canonical, "path", path, "error", errs[i])
			repo.Truncated = true
			continue
		}
		content := contents[i]
		dir := skillDir(path)
		doc := frontmatter(content)
		fm := scalars(doc)
		repo.Skills = append(repo.Skills, Skill{
			Name:        skillName(fm, dir, name),
			Description: strings.TrimSpace(fm["description"]),
			Path:        dir,
			Tags:        metadataTags(doc, d.cfg.Tags),
		})
	}
	sort.Slice(repo.Skills, func(i, j int) bool {
		if repo.Skills[i].Path != repo.Skills[j].Path {
			return repo.Skills[i].Path < repo.Skills[j].Path
		}
		return repo.Skills[i].Name < repo.Skills[j].Name
	})
	repo.Plugins = pluginsOf(files, repo.Skills, name)
	return repo, nil
}

// gitTree is a git/trees answer.
type gitTree struct {
	Tree []struct {
		Path string `json:"path"`
		Type string `json:"type"`
		SHA  string `json:"sha"`
		Size int64  `json:"size"`
	} `json:"tree"`
	Truncated bool `json:"truncated"`
}

// tree lists the files below dir at commit, recursively, with
// repository-relative paths. Without a dir it is the whole tree; with one,
// the directories down to it are read one level each and only dir
// recursively, so a large repository's other directories are not listed.
func (d *Discoverer) tree(ctx context.Context, owner, name, commit, dir string) (gitTree, error) {
	base := d.gh.apiURL + "/repos/" + owner + "/" + name + "/git/trees/"
	sha := commit
	if dir != "" {
		for _, seg := range strings.Split(dir, "/") {
			var level gitTree
			if err := d.gh.getJSON(ctx, base+sha, &level); err != nil {
				return gitTree{}, err
			}
			next := ""
			for _, entry := range level.Tree {
				if entry.Type == "tree" && entry.Path == seg {
					next = entry.SHA
				}
			}
			if next == "" {
				return gitTree{}, fmt.Errorf("path %q does not exist at commit %s: %w", dir, commit, ErrNotFound)
			}
			sha = next
		}
	}
	var out gitTree
	if err := d.gh.getJSON(ctx, base+sha+"?recursive=1", &out); err != nil {
		return gitTree{}, err
	}
	if dir != "" {
		for i := range out.Tree {
			out.Tree[i].Path = dir + "/" + out.Tree[i].Path
		}
	}
	return out, nil
}

// frontmatter parses the YAML block between the leading `---` lines; nil
// when there is none or it does not parse.
func frontmatter(content string) map[string]any {
	doc, _ := splitFrontmatter(content)
	return doc
}

// splitFrontmatter is the parsed frontmatter and the body below it; without
// a closed frontmatter block, nil and the whole content. A block that does
// not parse as YAML is nil with the body still split off.
func splitFrontmatter(content string) (map[string]any, string) {
	content = strings.TrimPrefix(content, "\uFEFF")
	lines := strings.Split(content, "\n")
	if strings.TrimSpace(lines[0]) != "---" {
		return nil, content
	}
	for end := 1; end < len(lines); end++ {
		if strings.TrimSpace(lines[end]) != "---" {
			continue
		}
		body := strings.Join(lines[end+1:], "\n")
		var doc map[string]any
		if err := yaml.Unmarshal([]byte(strings.Join(lines[1:end], "\n")), &doc); err != nil {
			return nil, body
		}
		return doc, body
	}
	return nil, content
}

// skillName is the frontmatter name, else the skill directory's name, else
// (a skill at the repository root) the repository's.
func skillName(fm map[string]string, dir, repoName string) string {
	if name := strings.TrimSpace(fm["name"]); name != "" {
		return name
	}
	if dir != "" {
		return path.Base(dir)
	}
	return repoName
}

// scalars keeps the frontmatter's string fields.
func scalars(doc map[string]any) map[string]string {
	out := map[string]string{}
	for k, v := range doc {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	return out
}

// metadataTags reads metadata.tags, a comma-joined string (the Agent Skills
// spec allows only string values in metadata), with the chart's tag rule: a
// malformed tag is dropped, never the skill.
func metadataTags(doc map[string]any, rule *tags.Rule) []string {
	metadata, _ := doc["metadata"].(map[string]any)
	raw, _ := metadata["tags"].(string)
	return rule.Parse(raw)
}
