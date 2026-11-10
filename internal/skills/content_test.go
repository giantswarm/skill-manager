package skills

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/giantswarm/skill-manager/internal/tags/tagstest"
)

const contentCommit = "1111111111111111111111111111111111111111"

// fakeContentRepo serves acme/content at contentCommit: a skill at
// skills/ops with a reference, a binary, a Latin-1 file, an oversize file and
// a nested skill of its own. A tree read waits for treeGate when it is set.
func fakeContentRepo(t *testing.T, treeGate ...chan struct{}) (*httptest.Server, *atomic.Int32) {
	var reads atomic.Int32
	files := map[string]string{
		"skills/ops/SKILL.md":              "---\nname: ops\ndescription: Runs things.\nmetadata:\n  tags: \"triage\"\n---\n\n# Ops\n\nDo it.\n",
		"skills/ops/references/style.md":   "be terse\n",
		"skills/ops/data.txt":              "a\x00b",
		"skills/ops/latin1.txt":            "caf\xe9",
		"skills/ops/nested/inner/SKILL.md": "---\nname: inner\n---\n",
		"skills/plain/SKILL.md":            "# no frontmatter\n",
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/acme/content/git/trees/", func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		for _, gate := range treeGate {
			<-gate
		}
		sha := strings.TrimPrefix(r.URL.Path, "/repos/acme/content/git/trees/")
		type entry = map[string]any
		var tree []entry
		switch sha {
		case contentCommit:
			tree = []entry{{"path": "skills", "type": "tree", "sha": "skillstree"}}
		case "skillstree":
			tree = []entry{{"path": "ops", "type": "tree", "sha": "opstree"}, {"path": "plain", "type": "tree", "sha": "plaintree"}}
		case "opstree":
			tree = []entry{
				{"path": "SKILL.md", "type": "blob", "size": 60, "sha": "s1"},
				{"path": "references", "type": "tree"},
				{"path": "references/style.md", "type": "blob", "size": 9, "sha": "s2"},
				{"path": "data.txt", "type": "blob", "size": 3, "sha": "s3"},
				{"path": "latin1.txt", "type": "blob", "size": 4, "sha": "s7"},
				{"path": "logo.png", "type": "blob", "size": 10, "sha": "s4"},
				{"path": "huge.md", "type": "blob", "size": MaxInlineFileBytes + 1, "sha": "s5"},
				{"path": "nested/inner/SKILL.md", "type": "blob", "size": 20, "sha": "s6"},
			}
		case "plaintree":
			tree = []entry{{"path": "SKILL.md", "type": "blob", "size": 17, "sha": "p1"}}
		default:
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"tree": tree})
	})
	mux.HandleFunc("/repos/acme/content/contents/", func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		require.Equal(t, contentCommit, r.URL.Query().Get("ref"))
		body, ok := files[strings.TrimPrefix(r.URL.Path, "/repos/acme/content/contents/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(body))
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts, &reads
}

func TestSkillReadsSkillMarkdownAndItsOwnFiles(t *testing.T) {
	ts, reads := fakeContentRepo(t)
	d := New(Config{APIURL: ts.URL, Tags: tagstest.Rule()}, nil)

	got, err := d.Skill(t.Context(), "https://github.com/acme/content", contentCommit, "skills/ops")
	require.NoError(t, err)
	require.Equal(t, &SkillContent{
		RepoURL:      "https://github.com/acme/content",
		Path:         "skills/ops",
		Commit:       contentCommit,
		Name:         "ops",
		Description:  "Runs things.",
		Tags:         []string{"triage"},
		Instructions: "# Ops\n\nDo it.",
		Files: []File{
			{Path: "SKILL.md", Size: 60, SHA: "s1"},
			{Path: "data.txt", Size: 3, SHA: "s3"},
			{Path: "huge.md", Size: MaxInlineFileBytes + 1, SHA: "s5"},
			{Path: "latin1.txt", Size: 4, SHA: "s7"},
			{Path: "logo.png", Size: 10, SHA: "s4"},
			{Path: "references/style.md", Size: 9, SHA: "s2"},
		},
	}, got, "SKILL.md first; a nested skill's files are its own")

	before := reads.Load()
	_, err = d.Skill(t.Context(), "https://github.com/acme/content", contentCommit, "skills/ops")
	require.NoError(t, err)
	require.Equal(t, before, reads.Load(), "a commit's content is read once")

	plain, err := d.Skill(t.Context(), "https://github.com/acme/content", contentCommit, "skills/plain")
	require.NoError(t, err)
	require.Equal(t, "plain", plain.Name, "no frontmatter: the directory names it")
	require.Equal(t, "# no frontmatter", plain.Instructions)
}

func TestSkillOfAMissingPathIsNotFound(t *testing.T) {
	ts, reads := fakeContentRepo(t)
	d := New(Config{APIURL: ts.URL}, nil)
	_, err := d.Skill(t.Context(), "https://github.com/acme/content", contentCommit, "skills/gone")
	require.ErrorIs(t, err, ErrNotFound)
	require.Equal(t, int32(1), reads.Load(), "a directory without SKILL.md costs one request, not a tree read")

	_, err = d.Skill(t.Context(), "https://github.com/acme/content", contentCommit, "skills/gone")
	require.ErrorIs(t, err, ErrNotFound)
	require.Equal(t, int32(1), reads.Load(), "a definite miss is kept")
}

// The read is shared by every caller of the same skill: the first caller
// giving up neither fails it nor keeps it out of the cache.
func TestSkillReadOutlivesTheCallerThatStartedIt(t *testing.T) {
	gate := make(chan struct{})
	ts, reads := fakeContentRepo(t, gate)
	d := New(Config{APIURL: ts.URL}, nil)

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := d.Skill(ctx, "https://github.com/acme/content", contentCommit, "skills/ops")
		done <- err
	}()
	require.Eventually(t, func() bool { return reads.Load() == 2 }, 5*time.Second, 5*time.Millisecond, "SKILL.md read, tree read waiting")
	cancel()
	close(gate)
	require.NoError(t, <-done)

	before := reads.Load()
	_, err := d.Skill(t.Context(), "https://github.com/acme/content", contentCommit, "skills/ops")
	require.NoError(t, err)
	require.Equal(t, before, reads.Load(), "the finished read is cached")
}

func TestSkillFileAnswersTextAndWithholdsBinaryAndOversize(t *testing.T) {
	ts, reads := fakeContentRepo(t)
	d := New(Config{APIURL: ts.URL}, nil)
	read := func(file string) (*FileContent, error) {
		return d.SkillFile(t.Context(), "https://github.com/acme/content", contentCommit, "skills/ops", file)
	}

	text := "be terse\n"
	got, err := read("references/style.md")
	require.NoError(t, err)
	require.Equal(t, &FileContent{File: File{Path: "references/style.md", Size: 9, SHA: "s2"}, Content: &text}, got)

	got, err = read("data.txt")
	require.NoError(t, err)
	require.True(t, got.Binary, "a NUL byte makes it binary")
	require.Nil(t, got.Content)

	got, err = read("latin1.txt")
	require.NoError(t, err)
	require.True(t, got.Binary, "invalid UTF-8 is not answered as text")
	require.Nil(t, got.Content)

	before := reads.Load()
	got, err = read("logo.png")
	require.NoError(t, err)
	require.True(t, got.Binary)
	got, err = read("huge.md")
	require.NoError(t, err)
	require.True(t, got.Truncated)
	require.Nil(t, got.Content)
	require.Equal(t, before, reads.Load(), "binary by name and oversize files are answered from the listing")

	for _, file := range []string{"missing.md", "nested/inner/SKILL.md", "../ops/SKILL.md", "/etc/passwd", ""} {
		_, err = read(file)
		require.ErrorIs(t, err, ErrNotFound, "only a listed file is read: %q", file)
	}
}

func TestHoldsOnlyBelowAConfiguredSource(t *testing.T) {
	d := New(Config{Repositories: []Source{{URL: "https://github.com/acme/content.git", Path: "skills"}, {URL: "https://github.com/acme/whole"}}}, nil)
	require.True(t, d.Holds("https://github.com/acme/content", "skills/ops"))
	require.True(t, d.Holds("https://github.com/ACME/content", "skills"))
	require.False(t, d.Holds("https://github.com/acme/content", "demo"))
	require.False(t, d.Holds("https://github.com/acme/content", ""))
	require.True(t, d.Holds("https://github.com/acme/whole", ""))
	require.False(t, d.Holds("https://github.com/acme/other", "skills"))
}
