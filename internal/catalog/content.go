package catalog

import (
	"context"
	"fmt"
	"io/fs"

	"github.com/giantswarm/skill-manager/internal/identity"
	"github.com/giantswarm/skill-manager/internal/skills"
)

// Skill reads the skill at dir of repoURL at commit: its SKILL.md split into
// frontmatter and instructions, and its file list.
func (s *Service) Skill(ctx context.Context, repoURL, commit, dir string) (*skills.SkillContent, error) {
	dir, err := s.allowContent(ctx, repoURL, commit, dir)
	if err != nil {
		return nil, err
	}
	c, err := s.skills.Skill(ctx, repoURL, commit, dir)
	if err != nil {
		return nil, classify(err)
	}
	return c, nil
}

// SkillFile reads one file of the skill at dir; file is relative to dir.
func (s *Service) SkillFile(ctx context.Context, repoURL, commit, dir, file string) (*skills.FileContent, error) {
	dir, err := s.allowContent(ctx, repoURL, commit, dir)
	if err != nil {
		return nil, err
	}
	if file == "." || !fs.ValidPath(file) {
		return nil, fmt.Errorf("%w: file %q must be a path relative to the skill directory", ErrInvalid, file)
	}
	f, err := s.skills.SkillFile(ctx, repoURL, commit, dir, file)
	if err != nil {
		return nil, classify(err)
	}
	return f, nil
}

// allowContent refuses a content read the catalogue does not list: not at a
// full commit, outside every configured source, or from a private repository
// the caller cannot read. It returns dir cleaned.
func (s *Service) allowContent(ctx context.Context, repoURL, commit, dir string) (string, error) {
	if !skills.CommitPattern.MatchString(commit) {
		return "", fmt.Errorf("%w: commit %q must be a full commit id", ErrInvalid, commit)
	}
	dir, err := skills.CleanPath(dir)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if !s.skills.Holds(repoURL, dir) {
		return "", fmt.Errorf("%w: %s at %q is in no configured skill source", ErrInvalid, repoURL, dir)
	}
	if !identity.IsService(ctx) {
		if err := s.pinner.RequireReadable(ctx, repoURL, gitHubLogin(ctx)); err != nil {
			return "", classify(err)
		}
	}
	return dir, nil
}
