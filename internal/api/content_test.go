package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/giantswarm/skill-manager/internal/catalog"
	"github.com/giantswarm/skill-manager/internal/catalog/catalogtest"
	"github.com/giantswarm/skill-manager/internal/skills"
)

func contentCatalog() *catalog.Service {
	return catalog.New(catalogtest.Lister{Repos: []skills.Repository{
		{RepoURL: catalogtest.SkillsRepo, Path: "skills"},
		{RepoURL: catalogtest.PrivateRepo},
	}}, &catalogtest.Pinner{})
}

func get(t *testing.T, target string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	return resp.StatusCode, out
}

func TestRESTSkillContent(t *testing.T) {
	ts := serveREST(t, contentCatalog())
	q := func(repo, commit, path string, extra ...string) string {
		v := url.Values{"url": {repo}, "commit": {commit}, "path": {path}}
		if len(extra) == 2 {
			v.Set(extra[0], extra[1])
		}
		return v.Encode()
	}

	status, out := get(t, ts.URL+"/v1/skills/content?"+q(catalogtest.SkillsRepo, catalogtest.MainHead, "skills/ops"))
	require.Equal(t, http.StatusOK, status, out)
	require.Equal(t, "ops", out["name"])
	require.Equal(t, catalogtest.MainHead, out["commit"])

	status, out = get(t, ts.URL+"/v1/skills/content/file?"+q(catalogtest.SkillsRepo, catalogtest.MainHead, "skills/ops", "file", "SKILL.md"))
	require.Equal(t, http.StatusOK, status, out)
	require.Equal(t, "SKILL.md", out["path"])
	require.NotEmpty(t, out["content"])

	for _, tc := range []struct {
		target string
		status int
	}{
		{"/v1/skills/content?" + q(catalogtest.SkillsRepo, "main", "skills/ops"), http.StatusBadRequest},
		{"/v1/skills/content?" + q(catalogtest.SkillsRepo, catalogtest.MainHead, "demo"), http.StatusBadRequest},
		{"/v1/skills/content?" + q(catalogtest.PrivateRepo, catalogtest.MainHead, "x"), http.StatusForbidden},
		{"/v1/skills/content?" + q(catalogtest.SkillsRepo, catalogtest.MainHead, "skills/missing"), http.StatusNotFound},
		{"/v1/skills/content/file?" + q(catalogtest.SkillsRepo, catalogtest.MainHead, "skills/ops"), http.StatusBadRequest},
		{"/v1/skills/content/file?" + q(catalogtest.SkillsRepo, catalogtest.MainHead, "skills/ops", "file", "other.md"), http.StatusNotFound},
	} {
		status, out := get(t, ts.URL+tc.target)
		require.Equal(t, tc.status, status, "%s: %v", tc.target, out)
		require.NotEmpty(t, out["error"])
	}
}

func TestMCPGetSkillAndFile(t *testing.T) {
	svc := contentCatalog()
	text, isErr := callTool(t, svc, ToolGetSkill, map[string]any{"url": catalogtest.SkillsRepo, "commit": catalogtest.MainHead, "path": "skills/ops"})
	require.False(t, isErr, text)
	require.Contains(t, text, `"name":"ops"`)

	text, isErr = callTool(t, svc, ToolGetSkillFile, map[string]any{"url": catalogtest.SkillsRepo, "commit": catalogtest.MainHead, "path": "skills/ops", "file": "SKILL.md"})
	require.False(t, isErr, text)
	require.Contains(t, text, `"path":"SKILL.md"`)

	text, isErr = callTool(t, svc, ToolGetSkill, map[string]any{"url": catalogtest.PrivateRepo, "commit": catalogtest.MainHead, "path": "x"})
	require.True(t, isErr)
	require.Contains(t, text, "forbidden")
}
