package skills

import (
	"sort"
	"strings"
)

// PluginFormat is how kagent's plugin materializer reads a plugin directory.
type PluginFormat string

const (
	// FormatAgentPlugins is an Agent Plugins package: plugin.json at its
	// root, skills in skills/<name>/SKILL.md, loaded by selecting skills.
	FormatAgentPlugins PluginFormat = "agent-plugins"
	// FormatClaudeCode is a Claude Code plugin: .claude-plugin/plugin.json,
	// loaded whole (commands, hooks, subagents, skills); nothing is selected.
	FormatClaudeCode PluginFormat = "claude-code"
)

// The manifests that make a directory a plugin root. A directory with both
// is an Agent Plugins package: the materializer reads the root one first.
const (
	agentPluginsManifest = "plugin.json"
	claudeManifest       = ".claude-plugin/plugin.json"
)

// Plugin is a plugin directory of a repository, at the commit its repository
// was read at. An agent mounts it as {path, git: {url, commit}, skills}.
type Plugin struct {
	// Name is the directory's name, the repository's at its root.
	Name string `json:"name"`
	// Path is the plugin root; "" at the repository root.
	Path   string       `json:"path"`
	Format PluginFormat `json:"format"`
	// Skills are an Agent Plugins package's selectable skills; always empty
	// for a Claude Code plugin.
	Skills []PluginSkill `json:"skills"`
}

// PluginSkill is a package skill as an agent selects it.
type PluginSkill struct {
	// Name is the skill's directory under skills/.
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// manifestRoot is the directory a manifest at path makes a plugin root.
func manifestRoot(path, manifest string) (string, bool) {
	if path == manifest {
		return "", true
	}
	if strings.HasSuffix(path, "/"+manifest) {
		return strings.TrimSuffix(path, "/"+manifest), true
	}
	return "", false
}

// pluginsOf finds the plugins among a repository's file paths. A package's
// skills are the discovered skills directly under its skills/ directory, so
// no read beyond the listing is needed.
func pluginsOf(files []string, skills []Skill, repoName string) []Plugin {
	formats := map[string]PluginFormat{}
	for _, path := range files {
		if root, ok := manifestRoot(path, claudeManifest); ok {
			if _, seen := formats[root]; !seen {
				formats[root] = FormatClaudeCode
			}
		}
		// A hidden directory holding a plugin.json (.claude-plugin, and other
		// tools' .codex-plugin or .cursor-plugin) is a manifest of its parent,
		// never a package of its own.
		if root, ok := manifestRoot(path, agentPluginsManifest); ok && !strings.HasPrefix(root[strings.LastIndex(root, "/")+1:], ".") {
			formats[root] = FormatAgentPlugins
		}
	}
	out := make([]Plugin, 0, len(formats))
	for path, format := range formats {
		name := repoName
		if path != "" {
			name = path[strings.LastIndex(path, "/")+1:]
		}
		p := Plugin{Name: name, Path: path, Format: format, Skills: []PluginSkill{}}
		if format == FormatAgentPlugins {
			prefix := "skills/"
			if path != "" {
				prefix = path + "/skills/"
			}
			for _, s := range skills {
				rest, ok := strings.CutPrefix(s.Path, prefix)
				if ok && rest != "" && !strings.Contains(rest, "/") {
					p.Skills = append(p.Skills, PluginSkill{Name: rest, Description: s.Description})
				}
			}
			sort.Slice(p.Skills, func(i, j int) bool { return p.Skills[i].Name < p.Skills[j].Name })
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}
