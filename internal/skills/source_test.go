package skills

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseSourcesAcceptsURLsAndObjects(t *testing.T) {
	for name, tc := range map[string]struct {
		in   string
		want []Source
	}{
		"empty":           {in: "", want: nil},
		"comma-separated": {in: "https://github.com/a/b, https://github.com/c/d", want: []Source{{URL: "https://github.com/a/b"}, {URL: "https://github.com/c/d"}}},
		"JSON strings":    {in: `["https://github.com/a/b"]`, want: []Source{{URL: "https://github.com/a/b"}}},
		"JSON mixed": {
			in: ` [{"url":"https://github.com/giantswarm/agent-platform","ref":"v1.4.0","path":"./skills/","readOnly":true},"https://github.com/c/d"]`,
			want: []Source{
				{URL: "https://github.com/giantswarm/agent-platform", Ref: "v1.4.0", Path: "skills", ReadOnly: true},
				{URL: "https://github.com/c/d"},
			},
		},
		"root path": {in: `[{"url":"https://github.com/a/b","path":"."}]`, want: []Source{{URL: "https://github.com/a/b"}}},
		"same URL, separate paths": {
			in:   `[{"url":"https://github.com/a/b","path":"skills"},{"url":"https://github.com/a/b.git","path":"demo"}]`,
			want: []Source{{URL: "https://github.com/a/b", Path: "skills"}, {URL: "https://github.com/a/b.git", Path: "demo"}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := ParseSources(tc.in)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestParseSourcesRefusesMalformedEntries(t *testing.T) {
	for name, in := range map[string]string{
		"not JSON":        `[{"url":`,
		"no url":          `[{"ref":"main"}]`,
		"climbing path":   `[{"url":"https://github.com/a/b","path":"../x"}]`,
		"ref whitespace":  `[{"url":"https://github.com/a/b","ref":"a b"}]`,
		"ref climbing":    `[{"url":"https://github.com/a/b","ref":"../../c/d/commits/main"}]`,
		"absolute path":   `[{"url":"https://github.com/a/b","path":"/skills"}]`,
		"inner climb":     `[{"url":"https://github.com/a/b","path":"skills/../.."}]`,
		"duplicate URL":   "https://github.com/a/b,https://github.com/a/b/",
		"nested paths":    `[{"url":"https://github.com/a/b","path":"skills"},{"url":"https://github.com/a/b","path":"skills/x"}]`,
		"root and a path": `[{"url":"https://github.com/a/b"},{"url":"https://github.com/a/b","path":"skills"}]`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseSources(in)
			assert.Error(t, err)
		})
	}
}

func TestSourcesAreTheConfiguredOnes(t *testing.T) {
	d := New(Config{Repositories: []Source{
		{URL: "https://github.com/giantswarm/agent-platform", Ref: "v1.4.0", Path: "skills"},
		{URL: "https://github.com/giantswarm/agent-platform", Path: "demo"},
		{URL: "https://github.com/giantswarm/agent-skills"},
	}}, nil)
	assert.Len(t, d.Sources(), 3)
}

// A repository URL takes GitHub's owner and name characters only: both go
// into API paths.
func TestParseRepoURLRefusesWhatIsNoGitHubName(t *testing.T) {
	for in, want := range map[string]string{
		"https://github.com/giantswarm/agent-skills":      "giantswarm/agent-skills",
		"https://github.com/giantswarm/agent-skills.git/": "giantswarm/agent-skills",
		"https://github.com/Giant-Swarm/a.b_c":            "Giant-Swarm/a.b_c",
	} {
		owner, repo, err := parseRepoURL(in)
		assert.NoError(t, err, in)
		assert.Equal(t, want, owner+"/"+repo, in)
	}
	for _, bad := range []string{
		"https://github.com/../orgs", "https://github.com/a/..", "https://github.com/a/.",
		"https://github.com/a/b?x=1", "https://github.com/a/b#x", "https://github.com/a/b%2F..%2Fc",
		"https://github.com/-a/b", "http://github.com/a/b", "https://github.com/a",
	} {
		_, _, err := parseRepoURL(bad)
		assert.Error(t, err, bad)
	}
}

// An upstream answer reaches an error bounded, whatever its first line.
func TestFirstLineIsBounded(t *testing.T) {
	assert.Len(t, firstLine(strings.Repeat("x", 5000)+"\nmore"), maxErrorBody)
	assert.Equal(t, "short", firstLine("short\nrest"))
}

func TestCheckRef(t *testing.T) {
	for _, ref := range []string{"", "main", "release/v1", "v1.4.0", "feature/x-y_z", "0123456789abcdef0123456789abcdef01234567"} {
		assert.NoError(t, CheckRef(ref), ref)
	}
	for _, ref := range []string{"..", "../x", "a/../b", "a/./b", ".hidden", "a//b", "/a", "a/", "a.", "a.lock", "@", "a@{1}", "a b", "a\tb", "a~1", "a^", "a:b", "a?", "a*", "a[", "a\\b"} {
		assert.Error(t, CheckRef(ref), ref)
	}
}
