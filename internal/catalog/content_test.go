package catalog

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/giantswarm/skill-manager/internal/catalog/catalogtest"
	"github.com/giantswarm/skill-manager/internal/identity"
	"github.com/giantswarm/skill-manager/internal/skills"
)

func contentService() *Service {
	return New(catalogtest.Lister{Repos: []skills.Repository{
		{RepoURL: catalogtest.SkillsRepo, Path: "skills"},
		{RepoURL: catalogtest.PrivateRepo},
	}}, &catalogtest.Pinner{})
}

// A skill's content is read only at a full commit, below a configured
// source, and from a private repository only by its readers.
func TestSkillContentRefusals(t *testing.T) {
	tests := []struct {
		name          string
		url, commit   string
		path          string
		wantErr       error
		wantCleanPath string
	}{
		{name: "public, in the source", url: catalogtest.SkillsRepo, commit: catalogtest.MainHead, path: "skills/ops", wantCleanPath: "skills/ops"},
		{name: "path cleaned", url: catalogtest.SkillsRepo, commit: catalogtest.MainHead, path: "./skills//ops/", wantCleanPath: "skills/ops"},
		{name: "a branch is no commit", url: catalogtest.SkillsRepo, commit: "main", path: "skills/ops", wantErr: ErrInvalid},
		{name: "outside the source path", url: catalogtest.SkillsRepo, commit: catalogtest.MainHead, path: "demo/x", wantErr: ErrInvalid},
		{name: "climbing", url: catalogtest.SkillsRepo, commit: catalogtest.MainHead, path: "skills/../demo", wantErr: ErrInvalid},
		{name: "not a configured repository", url: "https://github.com/acme/other", commit: catalogtest.MainHead, path: "", wantErr: ErrInvalid},
		{name: "private, no GitHub login", url: catalogtest.PrivateRepo, commit: catalogtest.MainHead, path: "x", wantErr: ErrForbidden},
		{name: "missing", url: catalogtest.SkillsRepo, commit: catalogtest.MainHead, path: "skills/missing", wantErr: ErrNotFound},
	}
	svc := contentService()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := svc.Skill(t.Context(), tt.url, tt.commit, tt.path)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				_, err = svc.SkillFile(t.Context(), tt.url, tt.commit, tt.path, "SKILL.md")
				require.ErrorIs(t, err, tt.wantErr, "the file read applies the same checks")
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.wantCleanPath, got.Path)
			f, err := svc.SkillFile(t.Context(), tt.url, tt.commit, tt.path, "SKILL.md")
			require.NoError(t, err)
			require.Equal(t, "SKILL.md", f.Path)
		})
	}
}

func TestSkillContentOfAPrivateRepositoryForItsReaders(t *testing.T) {
	svc := contentService()
	_, err := svc.Skill(asAlice(t.Context()), catalogtest.PrivateRepo, catalogtest.MainHead, "x")
	require.NoError(t, err)
	_, err = svc.Skill(identity.ContextWithService(t.Context()), catalogtest.PrivateRepo, catalogtest.MainHead, "x")
	require.NoError(t, err)
}

func TestSkillContentTakesTheCommitInLowerCase(t *testing.T) {
	svc := contentService()
	got, err := svc.Skill(t.Context(), catalogtest.SkillsRepo, strings.ToUpper(catalogtest.MainHead), "skills/ops")
	require.NoError(t, err)
	require.Equal(t, strings.ToLower(catalogtest.MainHead), got.Commit, "one cache entry and one answer per commit")
}

func TestSkillFileRefusesAMalformedFilePath(t *testing.T) {
	for _, file := range []string{"", ".", "../secret", "a/../b", "/etc/passwd", "a//b", "a/"} {
		_, err := contentService().SkillFile(t.Context(), catalogtest.SkillsRepo, catalogtest.MainHead, "skills/ops", file)
		require.ErrorIs(t, err, ErrInvalid, "%q", file)
	}
}
