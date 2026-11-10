package catalog

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/giantswarm/skill-manager/internal/catalog/catalogtest"
	"github.com/giantswarm/skill-manager/internal/identity"
	"github.com/giantswarm/skill-manager/internal/skills"
)

func newService(sources ...skills.Source) (*Service, *catalogtest.Pinner) {
	p := &catalogtest.Pinner{}
	return New(skills.New(skills.Config{Repositories: sources}, nil), p), p
}

func asAlice(ctx context.Context) context.Context {
	return identity.ContextWithGitHub(ctx, &identity.GitHub{Login: "alice", Token: "gh-alice"})
}

func gitSkill(name, url, ref, path string) Item {
	return Item{Name: name, Path: path, Git: &Git{URL: url, Ref: ref}}
}

// A git item is pinned to its ref's commit, its ref dropped; an OCI item to
// its digest. Names, paths and a plugin's skill selection pass through.
func TestResolvePinsGitAndOCIItems(t *testing.T) {
	svc, _ := newService()
	got, err := svc.Resolve(t.Context(), Request{
		Skills: []Item{
			gitSkill("x", catalogtest.SkillsRepo, "feature", "skills/x"),
			gitSkill("y", catalogtest.SkillsRepo, "", "skills/y"),
			{Name: "k", OCI: &OCI{Ref: "ghcr.io/giantswarm/skills/kubectl:1.4.0"}},
			{Name: "d", OCI: &OCI{Ref: "ghcr.io/giantswarm/skills/kubectl@" + catalogtest.Digest}},
			{Name: "c", Git: &Git{URL: catalogtest.SkillsRepo, Commit: catalogtest.TagHead}, Path: "skills/c"},
		},
		Plugins: []Item{{Path: "plugins/claude", Git: &Git{URL: catalogtest.SkillsRepo, Ref: "main"}, Skills: []string{"a", "b"}}},
	})
	require.NoError(t, err)
	assert.Equal(t, []Item{
		{Name: "x", Path: "skills/x", Git: &Git{URL: catalogtest.SkillsRepo, Commit: catalogtest.FeatureHead}},
		{Name: "y", Path: "skills/y", Git: &Git{URL: catalogtest.SkillsRepo, Commit: catalogtest.MainHead}},
		{Name: "k", OCI: &OCI{Ref: "ghcr.io/giantswarm/skills/kubectl@" + catalogtest.Digest}},
		{Name: "d", OCI: &OCI{Ref: "ghcr.io/giantswarm/skills/kubectl@" + catalogtest.Digest}},
		{Name: "c", Path: "skills/c", Git: &Git{URL: catalogtest.SkillsRepo, Commit: catalogtest.TagHead}},
	}, got.Skills)
	claude := true
	assert.Equal(t, []Item{{Path: "plugins/claude", Git: &Git{URL: catalogtest.SkillsRepo, Commit: catalogtest.MainHead}, Skills: []string{"a", "b"}, ClaudeFormat: &claude}}, got.Plugins)
}

// A git item without a ref below a configured source's path takes that
// source's ref (the deepest source holding it); else the default branch.
func TestResolveFollowsTheSourceRef(t *testing.T) {
	svc, _ := newService(skills.Source{URL: catalogtest.SkillsRepo, Ref: "v1.2.0", Path: "skills", ReadOnly: true})
	got, err := svc.Resolve(t.Context(), Request{
		Skills: []Item{
			gitSkill("overview", catalogtest.SkillsRepo, "", "skills/overview"),
			gitSkill("demo", catalogtest.SkillsRepo, "", "demo"),
			gitSkill("tools", catalogtest.SkillsRepo, "feature", "skills/tools"),
		},
		Plugins: []Item{{Path: "skills/plugin", Git: &Git{URL: catalogtest.SkillsRepo}}},
	})
	require.NoError(t, err)
	assert.Equal(t, catalogtest.TagHead, got.Skills[0].Git.Commit, "the source's ref, not the default branch")
	assert.Equal(t, catalogtest.MainHead, got.Skills[1].Git.Commit, "outside the source's path")
	assert.Equal(t, catalogtest.FeatureHead, got.Skills[2].Git.Commit, "an explicit ref wins")
	assert.Equal(t, catalogtest.TagHead, got.Plugins[0].Git.Commit, "a plugin follows its source too")
}

// One resolve looks each repository ref up once, however many items share it.
func TestResolveLooksEachRefUpOnce(t *testing.T) {
	svc, p := newService()
	_, err := svc.Resolve(t.Context(), Request{
		Skills:  []Item{gitSkill("a", catalogtest.SkillsRepo, "", "a"), gitSkill("b", catalogtest.SkillsRepo, "", "b"), gitSkill("c", catalogtest.SkillsRepo, "feature", "c")},
		Plugins: []Item{{Path: "plugins/agent", Git: &Git{URL: catalogtest.SkillsRepo}}},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"git " + catalogtest.SkillsRepo + "@", "git " + catalogtest.SkillsRepo + "@feature"}, p.GitCalls())
}

// Every failure names the item and says what kind it is.
func TestResolveRefusals(t *testing.T) {
	cases := map[string]struct {
		req   Request
		kind  error
		list  string
		index int
	}{
		"no source":                          {Request{Skills: []Item{{Name: "x"}}}, ErrInvalid, "skills", 0},
		"both sources":                       {Request{Skills: []Item{{Name: "x", Git: &Git{URL: catalogtest.SkillsRepo}, OCI: &OCI{Ref: "r/x:1"}}}}, ErrInvalid, "skills", 0},
		"ref and commit":                     {Request{Skills: []Item{{Name: "x", Git: &Git{URL: catalogtest.SkillsRepo, Ref: "main", Commit: catalogtest.MainHead}}}}, ErrInvalid, "skills", 0},
		"short commit":                       {Request{Skills: []Item{{Name: "x", Git: &Git{URL: catalogtest.SkillsRepo, Commit: "abc"}}}}, ErrInvalid, "skills", 0},
		"climbing path":                      {Request{Skills: []Item{gitSkill("x", catalogtest.SkillsRepo, "", "../x")}}, ErrInvalid, "skills", 0},
		"absolute path":                      {Request{Skills: []Item{gitSkill("x", catalogtest.SkillsRepo, "", "/x")}}, ErrInvalid, "skills", 0},
		"oci skill with path":                {Request{Skills: []Item{{Name: "x", Path: "x", OCI: &OCI{Ref: "ghcr.io/x/y:1"}}}}, ErrInvalid, "skills", 0},
		"bad oci reference":                  {Request{Skills: []Item{{Name: "x", OCI: &OCI{Ref: "not a ref"}}}}, ErrInvalid, "skills", 0},
		"not github":                         {Request{Skills: []Item{gitSkill("x", "https://gitlab.com/x/y", "", "")}}, ErrInvalid, "skills", 0},
		"unknown ref":                        {Request{Skills: []Item{gitSkill("a", catalogtest.SkillsRepo, "", "a"), gitSkill("x", catalogtest.SkillsRepo, "nope", "x")}}, ErrNotFound, "skills", 1},
		"missing path":                       {Request{Plugins: []Item{{Path: "missing", Git: &Git{URL: catalogtest.SkillsRepo}}}}, ErrNotFound, "plugins", 0},
		"unknown image tag":                  {Request{Skills: []Item{{Name: "x", OCI: &OCI{Ref: "ghcr.io/giantswarm/skills/kubectl:9"}}}}, ErrNotFound, "skills", 0},
		"private, not alice":                 {Request{Skills: []Item{gitSkill("x", catalogtest.PrivateRepo, "", "")}}, ErrForbidden, "skills", 0},
		"GitHub fails":                       {Request{Skills: []Item{gitSkill("x", "https://github.com/giantswarm/down", "", "")}}, ErrUpstream, "skills", 0},
		"plugin without source":              {Request{Plugins: []Item{{Path: "p"}}}, ErrInvalid, "plugins", 0},
		"ref climbing to another repository": {Request{Skills: []Item{gitSkill("x", catalogtest.SkillsRepo, "../../../../repos/giantswarm/private/commits/main", "")}}, ErrInvalid, "skills", 0},
		"ref with whitespace":                {Request{Skills: []Item{gitSkill("x", catalogtest.SkillsRepo, "a b", "")}}, ErrInvalid, "skills", 0},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			svc, _ := newService()
			_, err := svc.Resolve(t.Context(), tc.req)
			require.ErrorIs(t, err, tc.kind)
			var itemErr *ItemError
			require.ErrorAs(t, err, &itemErr)
			assert.Equal(t, tc.list, itemErr.List)
			assert.Equal(t, tc.index, itemErr.Index)
		})
	}
}

// A private repository whose read failed is withheld too, and a repository a
// caller names answers the same whether it is private or missing.
func TestListWithholdsFailedPrivateReadsAndHidesWhichRepositoriesExist(t *testing.T) {
	repos := []skills.Repository{
		{RepoURL: catalogtest.PrivateRepo, Private: true, Skills: []skills.Skill{}, Error: `path "x" does not exist at commit ` + catalogtest.MainHead},
		{RepoURL: catalogtest.MissingRepo, Skills: []skills.Skill{}, Error: "GitHub answered 404"},
	}
	configured := New(catalogtest.Lister{Repos: repos}, &catalogtest.Pinner{})
	res, err := configured.List(t.Context(), "", false)
	require.NoError(t, err)
	assert.NotContains(t, res.Repositories[0].Error, catalogtest.MainHead)
	assert.Contains(t, res.Repositories[0].Error, "cannot read it")

	named := New(catalogtest.Lister{Repos: repos, Configured: []skills.Source{}}, &catalogtest.Pinner{})
	res, err = named.List(t.Context(), "", false)
	require.NoError(t, err)
	assert.Equal(t, catalogtest.PrivateRepo+" does not exist or you cannot read it", res.Repositories[0].Error)
	assert.Equal(t, catalogtest.MissingRepo+" does not exist or you cannot read it", res.Repositories[1].Error)
}

// A digest is pinned only for an allowed registry, in the pinned form.
func TestResolveChecksADigestsRegistry(t *testing.T) {
	svc, _ := newService()
	_, err := svc.Resolve(t.Context(), Request{Skills: []Item{{Name: "e", OCI: &OCI{Ref: "evil.example.com/x:1@" + catalogtest.Digest}}}})
	assert.ErrorIs(t, err, ErrInvalid)
}

// A private repository is pinned for a person who can read it.
func TestResolvePinsAPrivateRepositoryForItsReader(t *testing.T) {
	svc, _ := newService()
	got, err := svc.Resolve(asAlice(t.Context()), Request{Skills: []Item{gitSkill("x", catalogtest.PrivateRepo, "", "")}})
	require.NoError(t, err)
	assert.Equal(t, catalogtest.MainHead, got.Skills[0].Git.Commit)
}

// A private repository is listed only to a person who can read it; anyone
// else sees that it exists and why its skills are withheld.
func TestListWithholdsPrivateRepositories(t *testing.T) {
	p := &catalogtest.Pinner{}
	repos := []skills.Repository{
		{RepoURL: catalogtest.SkillsRepo, Ref: "v1.2.0", Path: "skills", ReadOnly: true, Commit: catalogtest.TagHead, Skills: []skills.Skill{{Name: "overview", Path: "skills/overview"}}},
		{RepoURL: catalogtest.PrivateRepo, Commit: catalogtest.MainHead, Private: true, Skills: []skills.Skill{{Name: "secret", Path: "secret"}}},
	}
	svc := New(catalogtest.Lister{Repos: repos}, p)

	res, err := svc.List(t.Context(), "", false)
	require.NoError(t, err)
	require.Len(t, res.Repositories, 2)
	assert.Equal(t, repos[0], res.Repositories[0])
	assert.Empty(t, res.Repositories[1].Skills)
	assert.Empty(t, res.Repositories[1].Commit)
	assert.Contains(t, res.Repositories[1].Error, "cannot read it")

	res, err = svc.List(asAlice(t.Context()), catalogtest.PrivateRepo, true)
	require.NoError(t, err)
	assert.Equal(t, []skills.Repository{repos[1]}, res.Repositories)
}

// The sources are the configured ones, each with the commit its ref
// resolves to now; a private one's commit only for its readers.
func TestSourcesReportTheirCommit(t *testing.T) {
	p := &catalogtest.Pinner{}
	repos := []skills.Repository{
		{RepoURL: catalogtest.SkillsRepo, Ref: "v1.2.0", Path: "skills", ReadOnly: true, Commit: catalogtest.TagHead},
		{RepoURL: catalogtest.PrivateRepo, Commit: catalogtest.MainHead, Private: true},
		{RepoURL: "https://github.com/giantswarm/gone", Error: "GitHub answered 404"},
	}
	svc := New(catalogtest.Lister{Repos: repos}, p)
	got, err := svc.Sources(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []Source{
		{URL: catalogtest.SkillsRepo, Ref: "v1.2.0", Path: "skills", ReadOnly: true, Commit: catalogtest.TagHead},
		{URL: catalogtest.PrivateRepo, Error: got[1].Error},
		{URL: "https://github.com/giantswarm/gone", Error: "GitHub answered 404"},
	}, got)
	assert.Contains(t, got[1].Error, "cannot read it")

	got, err = svc.Sources(asAlice(t.Context()))
	require.NoError(t, err)
	assert.Equal(t, catalogtest.MainHead, got[1].Commit)
}

// A plugin item that asks for claudeFormat must be a Claude Code plugin.
func TestResolveRequiresAClaudeFormatPluginWhenAsked(t *testing.T) {
	svc, _ := newService()
	yes := true
	got, err := svc.Resolve(t.Context(), Request{Plugins: []Item{{Path: "plugins/claude", Git: &Git{URL: catalogtest.SkillsRepo}, ClaudeFormat: &yes}}})
	require.NoError(t, err)
	assert.True(t, *got.Plugins[0].ClaudeFormat)

	_, err = svc.Resolve(t.Context(), Request{Plugins: []Item{{Path: "plugins/agent", Git: &Git{URL: catalogtest.SkillsRepo}, ClaudeFormat: &yes}}})
	require.ErrorIs(t, err, ErrInvalid)
	assert.ErrorContains(t, err, "not a Claude Code plugin")

	_, err = svc.Resolve(t.Context(), Request{Skills: []Item{{Name: "x", Git: &Git{URL: catalogtest.SkillsRepo}, ClaudeFormat: &yes}}})
	require.ErrorIs(t, err, ErrInvalid, "claudeFormat is for plugins")
}

// The platform's own service caller (agent-manager's migrate) rewrites
// existing pins: it is not a person, so no readability check applies.
func TestResolveForTheServiceCallerSkipsTheReadabilityCheck(t *testing.T) {
	svc, p := newService()
	got, err := svc.Resolve(identity.ContextWithService(t.Context()), Request{Skills: []Item{gitSkill("x", catalogtest.PrivateRepo, "", "")}})
	require.NoError(t, err)
	assert.Equal(t, catalogtest.MainHead, got.Skills[0].Git.Commit)
	for _, c := range p.Calls() {
		assert.NotContains(t, c, "readable")
	}
}

// Paths are cleaned as source paths are: "." is the root, a trailing or
// doubled slash goes, and the cleaned path is what comes back.
func TestResolveCleansPaths(t *testing.T) {
	svc, _ := newService()
	got, err := svc.Resolve(t.Context(), Request{Skills: []Item{
		gitSkill("x", catalogtest.SkillsRepo, "", "./skills//x/"),
		gitSkill("root", catalogtest.SkillsRepo, "", "."),
	}})
	require.NoError(t, err)
	assert.Equal(t, "skills/x", got.Skills[0].Path)
	assert.Equal(t, "", got.Skills[1].Path)
	_, err = svc.Resolve(t.Context(), Request{Skills: []Item{gitSkill("x", catalogtest.SkillsRepo, "", "skills/../../x")}})
	assert.ErrorIs(t, err, ErrInvalid)
}

// A commit given with no path to check is checked to exist; a plugin's format
// is read only at a commit that exists.
func TestResolveChecksAGivenCommit(t *testing.T) {
	svc, p := newService()
	got, err := svc.Resolve(t.Context(), Request{Skills: []Item{{Name: "x", Git: &Git{URL: catalogtest.SkillsRepo, Commit: catalogtest.TagHead}}}})
	require.NoError(t, err)
	assert.Equal(t, catalogtest.TagHead, got.Skills[0].Git.Commit)
	assert.Contains(t, p.Calls(), "commit "+catalogtest.SkillsRepo+"@"+catalogtest.TagHead)

	unknown := strings.Repeat("b", 40)
	_, err = svc.Resolve(t.Context(), Request{Plugins: []Item{{Git: &Git{URL: catalogtest.SkillsRepo, Commit: unknown}}}})
	assert.ErrorIs(t, err, ErrNotFound)
}
