package skills

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPluginsOf(t *testing.T) {
	tests := []struct {
		name   string
		files  []string
		skills []Skill
		want   []Plugin
	}{
		{
			name: "both formats, package skills one level below skills/",
			files: []string{
				"README.md",
				"plugins/review/plugin.json",
				"plugins/review/skills/go-review/SKILL.md",
				"plugins/review/skills/go-review/references/style.md",
				"plugins/review/skills/helm-review/SKILL.md",
				"plugins/review/skills/helm-review/nested/deep/SKILL.md",
				"plugins/commits/.claude-plugin/plugin.json",
				"plugins/commits/commands/commit.md",
				"plugins/commits/skills/commit-style/SKILL.md",
			},
			skills: []Skill{
				{Name: "go-review", Path: "plugins/review/skills/go-review", Description: "Go"},
				{Name: "helm-review", Path: "plugins/review/skills/helm-review"},
				{Name: "deep", Path: "plugins/review/skills/helm-review/nested/deep", Description: "not a package skill"},
				{Name: "commit-style", Path: "plugins/commits/skills/commit-style", Description: "C"},
			},
			want: []Plugin{
				{Name: "commits", Path: "plugins/commits", Format: FormatClaudeCode, Skills: []PluginSkill{}},
				{Name: "review", Path: "plugins/review", Format: FormatAgentPlugins, Skills: []PluginSkill{
					{Name: "go-review", Description: "Go"},
					{Name: "helm-review"},
				}},
			},
		},
		{
			name:   "package at the repository root takes the repository name",
			files:  []string{"plugin.json", "skills/triage/SKILL.md"},
			skills: []Skill{{Name: "triage", Path: "skills/triage", Description: "Triage"}},
			want: []Plugin{
				{Name: "agents", Path: "", Format: FormatAgentPlugins, Skills: []PluginSkill{{Name: "triage", Description: "Triage"}}},
			},
		},
		{
			name:  "claude plugin at the repository root",
			files: []string{".claude-plugin/plugin.json", "skills/a/SKILL.md"},
			want: []Plugin{
				{Name: "agents", Path: "", Format: FormatClaudeCode, Skills: []PluginSkill{}},
			},
		},
		{
			name:  "both manifests read as an Agent Plugins package",
			files: []string{"p/.claude-plugin/plugin.json", "p/plugin.json"},
			want: []Plugin{
				{Name: "p", Path: "p", Format: FormatAgentPlugins, Skills: []PluginSkill{}},
			},
		},
		{
			name: "another tool's manifest folder is no package",
			files: []string{
				"plugin.json",
				".claude-plugin/plugin.json",
				".codex-plugin/plugin.json",
				".cursor-plugin/plugin.json",
				"p/.codex-plugin/plugin.json",
				"skills/keep-the-why/SKILL.md",
			},
			skills: []Skill{{Name: "keep-the-why", Path: "skills/keep-the-why"}},
			want: []Plugin{
				{Name: "agents", Path: "", Format: FormatAgentPlugins, Skills: []PluginSkill{{Name: "keep-the-why"}}},
			},
		},
		{
			name:   "plain skills are no plugin",
			files:  []string{"skills/a/SKILL.md"},
			skills: []Skill{{Name: "a", Path: "skills/a"}},
			want:   []Plugin{},
		},
		{
			name:  "a file merely named like a manifest is no plugin",
			files: []string{"docs/myplugin.json", "x/.claude-plugin/other.json"},
			want:  []Plugin{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, pluginsOf(tt.files, tt.skills, "agents"))
		})
	}
}

// Plugins come from the same tree read as the skills, at the same commit.
func TestDiscoverListsPluginsWithTheirFormat(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/acme/plugins", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"default_branch": "main"})
	})
	mux.HandleFunc("/repos/acme/plugins/commits/main", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(headCommit))
	})
	mux.HandleFunc("/repos/acme/plugins/git/trees/"+headCommit, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"tree": []map[string]any{
			{"path": "plugins", "type": "tree"},
			{"path": "plugins/review/plugin.json", "type": "blob"},
			{"path": "plugins/review/skills/go-review/SKILL.md", "type": "blob"},
			{"path": "plugins/gs-base/.claude-plugin/plugin.json", "type": "blob"},
			{"path": "plugins/gs-base/skills/fluxcd/SKILL.md", "type": "blob"},
		}})
	})
	mux.HandleFunc("/repos/acme/plugins/contents/", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, headCommit, r.URL.Query().Get("ref"))
		_, _ = w.Write([]byte("---\ndescription: d\n---\n"))
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	d := New(Config{Repositories: []Source{{URL: "https://github.com/acme/plugins"}}, APIURL: ts.URL}, nil)
	res, err := d.List(t.Context(), "", "", false)
	require.NoError(t, err)
	require.Len(t, res.Repositories, 1)
	repo := res.Repositories[0]
	require.Empty(t, repo.Error)
	require.Equal(t, headCommit, repo.Commit)
	require.Equal(t, []Plugin{
		{Name: "gs-base", Path: "plugins/gs-base", Format: FormatClaudeCode, Skills: []PluginSkill{}},
		{Name: "review", Path: "plugins/review", Format: FormatAgentPlugins, Skills: []PluginSkill{{Name: "go-review", Description: "d"}}},
	}, repo.Plugins)
	require.Len(t, repo.Skills, 2, "a plugin's skills stay listed as skills too")
}
