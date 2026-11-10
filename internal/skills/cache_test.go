package skills

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// wrapped serves the fake GitHub behind a switch that fails every request
// with 500, and records the method and path of every request.
type wrapped struct {
	*httptest.Server
	failing atomic.Bool
	mu      sync.Mutex
	seen    []string
}

func wrapGitHub(t *testing.T) (*wrapped, *int) {
	inner, hits := fakeGitHub(t)
	w := &wrapped{}
	w.Server = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		w.mu.Lock()
		w.seen = append(w.seen, r.Method+" "+r.URL.Path)
		w.mu.Unlock()
		if w.failing.Load() {
			http.Error(rw, `{"message":"boom"}`, http.StatusInternalServerError)
			return
		}
		inner.Config.Handler.ServeHTTP(rw, r)
	}))
	t.Cleanup(w.Close)
	return w, hits
}

func (w *wrapped) requests() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.seen...)
}

const agentSkills = "https://github.com/giantswarm/agent-skills"

// A failed read keeps the last good listing, flagged stale with the error.
func TestAFailedReadKeepsTheLastGoodListing(t *testing.T) {
	gh, _ := wrapGitHub(t)
	d := New(Config{Repositories: []Source{{URL: agentSkills}}, APIURL: gh.URL, Tokens: StaticToken("secret"), CacheTTL: time.Minute}, nil)
	good, err := d.List(t.Context(), "", "", false)
	require.NoError(t, err)
	require.Len(t, good.Repositories[0].Skills, 3)

	gh.failing.Store(true)
	res, err := d.List(t.Context(), "", "", true)
	require.NoError(t, err)
	repo := res.Repositories[0]
	assert.True(t, repo.Stale)
	assert.NotEmpty(t, repo.Error)
	assert.Equal(t, good.Repositories[0].Skills, repo.Skills, "the last good skills")
	assert.Equal(t, headCommit, repo.Commit)

	gh.failing.Store(false)
	res, err = d.List(t.Context(), "", "", true)
	require.NoError(t, err)
	assert.False(t, res.Repositories[0].Stale)
	assert.Empty(t, res.Repositories[0].Error)
}

// A failed read with no good listing before is kept only briefly.
func TestAFailedReadIsCachedBriefly(t *testing.T) {
	gh, _ := wrapGitHub(t)
	gh.failing.Store(true)
	d := New(Config{Repositories: []Source{{URL: agentSkills}}, APIURL: gh.URL, Tokens: StaticToken("secret"), CacheTTL: time.Hour, ErrorTTL: time.Nanosecond}, nil)
	res, err := d.List(t.Context(), "", "", false)
	require.NoError(t, err)
	require.NotEmpty(t, res.Repositories[0].Error)
	gh.failing.Store(false)
	res, err = d.List(t.Context(), "", "", false)
	require.NoError(t, err)
	assert.Empty(t, res.Repositories[0].Error, "past the error TTL the next list reads again, the cache TTL notwithstanding")
	assert.Len(t, res.Repositories[0].Skills, 3)
}

// One repository spelt differently is one cache entry.
func TestRepositorySpellingsShareTheCache(t *testing.T) {
	gh, hits := wrapGitHub(t)
	d := New(Config{APIURL: gh.URL, Tokens: StaticToken("secret"), CacheTTL: time.Minute}, nil)
	_, err := d.List(t.Context(), "https://github.com/giantswarm/agent-skills.git/", "", false)
	require.NoError(t, err)
	before := *hits
	_, err = d.List(t.Context(), "https://github.com/GiantSwarm/Agent-Skills", "", false)
	require.NoError(t, err)
	assert.Equal(t, before, *hits)
}

// Concurrent lists of a cold source read it once.
func TestConcurrentListsReadOnce(t *testing.T) {
	gh, _ := wrapGitHub(t)
	d := New(Config{Repositories: []Source{{URL: agentSkills}}, APIURL: gh.URL, Tokens: StaticToken("secret"), CacheTTL: time.Minute}, nil)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			_, err := d.List(t.Context(), "", "", false)
			assert.NoError(t, err)
		})
	}
	wg.Wait()
	trees := 0
	for _, r := range gh.requests() {
		if strings.Contains(r, "/git/trees/") {
			trees++
		}
	}
	assert.Equal(t, 1, trees)
}

// A source's head is its ref's commit, without reading its skills.
func TestHeadReadsNoSkills(t *testing.T) {
	gh, _ := wrapGitHub(t)
	d := New(Config{APIURL: gh.URL, Tokens: StaticToken("secret"), CacheTTL: time.Minute}, nil)
	head, err := d.Head(t.Context(), Source{URL: agentSkills, Ref: "v1.2.0", Path: "skills"})
	require.NoError(t, err)
	assert.Equal(t, tagCommit, head.Commit)
	assert.False(t, head.Private)
	_, err = d.Head(t.Context(), Source{URL: agentSkills, Ref: "v1.2.0", Path: "skills"})
	require.NoError(t, err)
	for _, r := range gh.requests() {
		assert.NotContains(t, r, "/git/trees/")
		assert.NotContains(t, r, "/contents/")
	}
	assert.Len(t, gh.requests(), 2, "the repository and the ref, once")
}

// A path check downloads no file: it is a HEAD.
func TestPathExistsIsAHead(t *testing.T) {
	gh, _ := wrapGitHub(t)
	r := NewResolver(gh.URL, StaticToken("secret"), nil, nil)
	ok, err := r.PathExists(t.Context(), agentSkills, headCommit, "agent-self-awareness/SKILL.md")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, []string{"HEAD /repos/giantswarm/agent-skills/contents/agent-self-awareness/SKILL.md"}, gh.requests())
}

// A given commit is checked to exist.
func TestCommitExists(t *testing.T) {
	gh, _ := wrapGitHub(t)
	r := NewResolver(gh.URL, StaticToken("secret"), nil, nil)
	require.NoError(t, r.CommitExists(t.Context(), agentSkills, tagCommit))
	err := r.CommitExists(t.Context(), agentSkills, strings.Repeat("a", 40))
	assert.ErrorIs(t, err, ErrNotFound)
}

// One resolve's readability check and default-branch lookup read the
// repository once.
func TestResolverReusesRepositoryMetadata(t *testing.T) {
	gh, _ := wrapGitHub(t)
	r := NewResolver(gh.URL, StaticToken("secret"), nil, nil)
	require.NoError(t, r.RequireReadable(t.Context(), agentSkills, "alice"))
	_, err := r.GitHead(t.Context(), agentSkills, "")
	require.NoError(t, err)
	n := 0
	for _, req := range gh.requests() {
		if req == "GET /repos/giantswarm/agent-skills" {
			n++
		}
	}
	assert.Equal(t, 1, n)
}

// A commit-shaped ref is a commit GitHub must have; a ref with a slash
// reaches GitHub with its slash.
func TestRefsAreAskedAsGiven(t *testing.T) {
	var paths []string
	var mu sync.Mutex
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.EscapedPath())
		mu.Unlock()
		switch r.URL.EscapedPath() {
		case "/repos/o/r/commits/release/v1", "/repos/o/r/commits/" + headCommit:
			_, _ = w.Write([]byte(headCommit))
		default:
			http.Error(w, `{"message":"No commit found"}`, http.StatusUnprocessableEntity)
		}
	}))
	t.Cleanup(gh.Close)
	r := NewResolver(gh.URL, nil, nil, nil)
	sha, err := r.GitHead(t.Context(), "https://github.com/o/r", "release/v1")
	require.NoError(t, err)
	assert.Equal(t, headCommit, sha)
	sha, err = r.GitHead(t.Context(), "https://github.com/o/r", headCommit)
	require.NoError(t, err)
	assert.Equal(t, headCommit, sha)
	_, err = r.GitHead(t.Context(), "https://github.com/o/r", featureCommit)
	assert.ErrorIs(t, err, ErrNotFound, "a commit id GitHub does not have")
	assert.Contains(t, paths, "/repos/o/r/commits/release/v1")
}

// Repeated refreshes within minRefresh are served from the cache.
func TestRefreshIsThrottled(t *testing.T) {
	defer func(prev time.Duration) { minRefresh = prev }(minRefresh)
	minRefresh = time.Hour
	gh, hits := wrapGitHub(t)
	d := New(Config{Repositories: []Source{{URL: agentSkills}}, APIURL: gh.URL, Tokens: StaticToken("secret"), CacheTTL: time.Minute}, nil)
	_, err := d.List(t.Context(), "", "", false)
	require.NoError(t, err)
	before := *hits
	_, err = d.List(t.Context(), "", "", true)
	require.NoError(t, err)
	assert.Equal(t, before, *hits)
}

// Repositories callers name cannot evict the configured sources' listings.
func TestNamedRepositoriesDoNotEvictTheConfiguredSources(t *testing.T) {
	gh, hits := wrapGitHub(t)
	d := New(Config{Repositories: []Source{{URL: agentSkills}}, APIURL: gh.URL, Tokens: StaticToken("secret"), CacheTTL: time.Minute}, nil)
	_, err := d.List(t.Context(), "", "", false)
	require.NoError(t, err)
	for i := range cacheMax + 1 {
		_, err := d.List(t.Context(), fmt.Sprintf("https://github.com/giantswarm/gone-%d", i), "", false)
		require.NoError(t, err)
	}
	before := *hits
	_, err = d.List(t.Context(), "", "", false)
	require.NoError(t, err)
	assert.Equal(t, before, *hits, "still cached")
}

// A read that fails after the visibility is known keeps it: a private
// repository's error is withheld from those who cannot read it.
func TestAFailedReadKeepsTheVisibility(t *testing.T) {
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/o/secret" {
			_, _ = w.Write([]byte(`{"default_branch":"main","private":true}`))
			return
		}
		http.Error(w, `{"message":"boom"}`, http.StatusInternalServerError)
	}))
	t.Cleanup(gh.Close)
	d := New(Config{Repositories: []Source{{URL: "https://github.com/o/secret"}}, APIURL: gh.URL}, nil)
	res, err := d.List(t.Context(), "", "", false)
	require.NoError(t, err)
	assert.NotEmpty(t, res.Repositories[0].Error)
	assert.True(t, res.Repositories[0].Private)
}

// Head is cached per repository and ref, its failure for the error TTL.
func TestHeadIsCachedPerRepositoryAndRef(t *testing.T) {
	gh, hits := wrapGitHub(t)
	d := New(Config{APIURL: gh.URL, Tokens: StaticToken("secret"), CacheTTL: time.Minute, ErrorTTL: time.Minute}, nil)
	for _, p := range []string{"", "a", "b"} {
		_, err := d.Head(t.Context(), Source{URL: agentSkills, Path: p})
		require.NoError(t, err)
	}
	assert.Equal(t, 2, *hits, "one repository and one commit lookup")
	for range 2 {
		_, err := d.Head(t.Context(), Source{URL: agentSkills, Ref: "nope"})
		require.Error(t, err)
	}
	assert.Equal(t, 4, *hits, "a failure is not asked again within the error TTL")
}
