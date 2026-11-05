package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/giantswarm/skill-manager/internal/catalog"
	"github.com/giantswarm/skill-manager/internal/catalog/catalogtest"
	"github.com/giantswarm/skill-manager/internal/identity"
	"github.com/giantswarm/skill-manager/internal/skills"
)

func newCatalog() *catalog.Service {
	return catalog.New(skills.New(skills.Config{Repositories: []skills.Source{{URL: catalogtest.SkillsRepo, Ref: "v1.2.0", Path: "skills", ReadOnly: true}}}, nil), &catalogtest.Pinner{})
}

func serveREST(t *testing.T, svc *catalog.Service) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	NewREST(svc, nil).Register(mux)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

func post(t *testing.T, url, body string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, url, strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	return resp.StatusCode, out
}

// POST /v1/resolve answers the items pinned: git.commit set, git.ref
// dropped, an OCI reference by digest.
func TestRESTResolvePinsTheItems(t *testing.T) {
	ts := serveREST(t, newCatalog())
	status, out := post(t, ts.URL+"/v1/resolve", `{
		"skills": [
			{"name": "x", "path": "skills/x", "git": {"url": "`+catalogtest.SkillsRepo+`", "ref": "feature"}},
			{"name": "o", "path": "skills/o", "git": {"url": "`+catalogtest.SkillsRepo+`"}},
			{"name": "y", "oci": {"ref": "ghcr.io/giantswarm/skills/kubectl:1.4.0"}}
		],
		"plugins": [{"path": "plugins/claude", "skills": ["a"], "git": {"url": "`+catalogtest.SkillsRepo+`", "ref": "main"}}]
	}`)
	require.Equal(t, http.StatusOK, status, out)
	assert.Equal(t, map[string]any{
		"skills": []any{
			map[string]any{"name": "x", "path": "skills/x", "git": map[string]any{"url": catalogtest.SkillsRepo, "commit": catalogtest.FeatureHead}},
			map[string]any{"name": "o", "path": "skills/o", "git": map[string]any{"url": catalogtest.SkillsRepo, "commit": catalogtest.TagHead}},
			map[string]any{"name": "y", "oci": map[string]any{"ref": "ghcr.io/giantswarm/skills/kubectl@" + catalogtest.Digest}},
		},
		"plugins": []any{
			map[string]any{"path": "plugins/claude", "skills": []any{"a"}, "claudeFormat": true, "git": map[string]any{"url": catalogtest.SkillsRepo, "commit": catalogtest.MainHead}},
		},
	}, out)
}

// A refused item answers its status and names the item.
func TestRESTResolveRefusals(t *testing.T) {
	ts := serveREST(t, newCatalog())
	for name, tc := range map[string]struct {
		body   string
		status int
		list   string
		item   float64
	}{
		"invalid item":  {`{"skills":[{"name":"x"}]}`, http.StatusBadRequest, "skills", 0},
		"not readable":  {`{"skills":[{"name":"x","git":{"url":"` + catalogtest.PrivateRepo + `"}}]}`, http.StatusForbidden, "skills", 0},
		"unknown ref":   {`{"plugins":[{"git":{"url":"` + catalogtest.SkillsRepo + `","ref":"main"}},{"git":{"url":"` + catalogtest.SkillsRepo + `","ref":"nope"}}]}`, http.StatusNotFound, "plugins", 1},
		"missing path":  {`{"skills":[{"name":"x","path":"missing","git":{"url":"` + catalogtest.SkillsRepo + `"}}]}`, http.StatusNotFound, "skills", 0},
		"GitHub failed": {`{"skills":[{"name":"x","git":{"url":"https://github.com/giantswarm/down"}}]}`, http.StatusBadGateway, "skills", 0},
	} {
		t.Run(name, func(t *testing.T) {
			status, out := post(t, ts.URL+"/v1/resolve", tc.body)
			assert.Equal(t, tc.status, status, out)
			assert.NotEmpty(t, out["error"])
			assert.Equal(t, tc.item, out["item"])
			assert.Equal(t, tc.list, out["list"])
		})
	}

	status, out := post(t, ts.URL+"/v1/resolve", `{"skills": [`)
	assert.Equal(t, http.StatusBadRequest, status)
	assert.NotEmpty(t, out["error"])
	assert.NotContains(t, out, "item", "a body that does not parse names no item")
	status, _ = post(t, ts.URL+"/v1/resolve", `{"skills": [], "unknown": 1}`)
	assert.Equal(t, http.StatusBadRequest, status, "an unknown field is refused")
}

// The caller's GitHub login opens a private repository they can read.
func TestRESTResolveActsAsTheCaller(t *testing.T) {
	mux := http.NewServeMux()
	NewREST(newCatalog(), nil).Register(mux)
	asAlice := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r.WithContext(identity.ContextWithGitHub(r.Context(), &identity.GitHub{Login: "alice", Token: "gh"})))
	})
	ts := httptest.NewServer(asAlice)
	defer ts.Close()
	status, out := post(t, ts.URL+"/v1/resolve", `{"skills":[{"name":"x","git":{"url":"`+catalogtest.PrivateRepo+`"}}]}`)
	assert.Equal(t, http.StatusOK, status, out)
}

// GET /v1/sources and GET /v1/skills read the discovery.
func TestRESTListsSourcesAndSkills(t *testing.T) {
	var gotSource string
	var gotRefresh bool
	lister := catalogtest.Lister{Repos: []skills.Repository{
		{RepoURL: catalogtest.SkillsRepo, Ref: "v1.2.0", Path: "skills", ReadOnly: true, Commit: catalogtest.TagHead, Skills: []skills.Skill{{Name: "overview", Description: "The platform.", Path: "skills/overview"}}},
	}, Observe: func(source string, refresh bool) { gotSource, gotRefresh = source, refresh }}
	ts := serveREST(t, catalog.New(lister, &catalogtest.Pinner{}))

	get := func(path string) (int, map[string]any) {
		resp, err := http.Get(ts.URL + path)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		var out map[string]any
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
		return resp.StatusCode, out
	}
	status, out := get("/v1/sources")
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, map[string]any{"sources": []any{map[string]any{"url": catalogtest.SkillsRepo, "ref": "v1.2.0", "path": "skills", "readOnly": true, "commit": catalogtest.TagHead}}}, out)

	status, out = get("/v1/skills?source=" + catalogtest.SkillsRepo + "&refresh=true")
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, catalogtest.SkillsRepo, gotSource)
	assert.True(t, gotRefresh)
	repos := out["repositories"].([]any)
	require.Len(t, repos, 1)
	repo := repos[0].(map[string]any)
	assert.Equal(t, catalogtest.TagHead, repo["commit"])
	assert.Equal(t, []any{map[string]any{"name": "overview", "description": "The platform.", "path": "skills/overview"}}, repo["skills"])
}
