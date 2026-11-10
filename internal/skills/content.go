package skills

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// The caps of a skill's content, the portal's: files listed per skill and
// the largest file answered inline.
const (
	MaxSkillFiles      = 200
	MaxInlineFileBytes = 512 * 1024
)

// contentReadTimeout bounds one content read including its wait for a slot;
// contentReadSlots bounds content reads in flight to GitHub; missTTL keeps a
// definite miss before it is asked again.
const (
	contentReadTimeout = 30 * time.Second
	contentReadSlots   = 4
	missTTL            = 5 * time.Minute
)

// binarySniffBytes is how far into a file a NUL byte is looked for: git's
// own heuristic.
const binarySniffBytes = 8000

// binaryExtensions are read as binary without fetching the file.
var binaryExtensions = map[string]bool{
	"7z": true, "a": true, "avi": true, "bin": true, "bmp": true, "bz2": true, "class": true, "db": true,
	"dll": true, "doc": true, "docx": true, "dylib": true, "eot": true, "exe": true, "flac": true, "gif": true,
	"gz": true, "ico": true, "jar": true, "jpeg": true, "jpg": true, "mkv": true, "mov": true, "mp3": true,
	"mp4": true, "npy": true, "npz": true, "o": true, "odt": true, "ogg": true, "otf": true, "parquet": true,
	"pdf": true, "pkl": true, "png": true, "ppt": true, "pptx": true, "psd": true, "pyc": true, "rar": true,
	"so": true, "sqlite": true, "tar": true, "tgz": true, "tif": true, "tiff": true, "ttf": true, "war": true, "wasm": true,
	"wav": true, "webm": true, "webp": true, "woff": true, "woff2": true, "xls": true, "xlsx": true, "xz": true,
	"zip": true, "zst": true,
}

// File is one file of a skill at a commit.
type File struct {
	// Path is relative to the skill directory.
	Path string `json:"path"`
	Size int64  `json:"size"`
	// SHA is the git blob id.
	SHA string `json:"sha"`
}

// SkillContent is a skill's SKILL.md at a commit, split the way an editor
// shows it, and the files of its directory.
type SkillContent struct {
	RepoURL string `json:"repoUrl"`
	// Path is the skill directory; "" at the repository root.
	Path   string `json:"path"`
	Commit string `json:"commit"`
	// Name is the frontmatter name, else the directory (or repository) name.
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Tags        []string `json:"tags,omitempty"`
	// Instructions is the Markdown body below the frontmatter.
	Instructions string `json:"instructions"`
	// Files are SKILL.md first, then by path, at most MaxSkillFiles. A file
	// below a nested SKILL.md belongs to that skill, not this one.
	Files          []File `json:"files"`
	FilesTruncated bool   `json:"filesTruncated,omitempty"`
}

// FileContent is one file of a skill. Content is set only for a text file
// of at most MaxInlineFileBytes.
type FileContent struct {
	File
	Binary    bool    `json:"binary"`
	Truncated bool    `json:"truncated"`
	Content   *string `json:"content,omitempty"`
}

// skillEntry is a skill at a commit: immutable once read, so kept until
// evicted; a definite miss is kept until expires. files are all the skill's
// files in the order SkillContent lists them, past MaxSkillFiles too.
type skillEntry struct {
	content *SkillContent
	files   []File
	err     error
	expires time.Time
}

// Holds reports whether a configured source holds path of repoURL: the
// content reads serve the catalogue, nothing else.
func (d *Discoverer) Holds(repoURL, dir string) bool {
	for _, s := range d.configured(repoURL) {
		if dir == s.Path || within(dir, s.Path) {
			return true
		}
	}
	return false
}

// Skill reads the skill at dir of repoURL at commit (a full commit id).
func (d *Discoverer) Skill(ctx context.Context, repoURL, commit, dir string) (*SkillContent, error) {
	e, err := d.skillAt(ctx, repoURL, commit, dir)
	if err != nil {
		return nil, err
	}
	out := *e.content
	return &out, nil
}

// SkillFile reads one file of the skill at dir; file must be one of the
// skill's files, so only a path GitHub listed is ever requested. A binary
// file by name or one over MaxInlineFileBytes is answered from the listing
// without fetching it.
func (d *Discoverer) SkillFile(ctx context.Context, repoURL, commit, dir, file string) (*FileContent, error) {
	e, err := d.skillAt(ctx, repoURL, commit, dir)
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(e.files, func(f File) bool { return f.Path == file })
	if i < 0 {
		return nil, fmt.Errorf("%w: %s is no file of the skill at %s", ErrNotFound, file, dir)
	}
	f := e.files[i]
	out := &FileContent{File: f, Binary: binaryByName(f.Path), Truncated: f.Size > MaxInlineFileBytes}
	if out.Binary || out.Truncated {
		return out, nil
	}
	owner, name, err := parseRepoURL(repoURL)
	if err != nil {
		return nil, err
	}
	var raw string
	err = d.withSlot(ctx, func(ctx context.Context) error {
		var err error
		raw, err = d.gh.raw(ctx, owner, name, joinPluginPath(dir, f.Path), commit)
		return err
	})
	if err != nil {
		return nil, contentError(repoURL, commit, err)
	}
	switch {
	case len(raw) > MaxInlineFileBytes:
		out.Truncated = true
	case strings.IndexByte(raw[:min(len(raw), binarySniffBytes)], 0) >= 0 || !utf8.ValidString(raw):
		// Invalid UTF-8 would reach the JSON answer with its bytes replaced.
		out.Binary = true
	default:
		out.Content = &raw
	}
	return out, nil
}

func (d *Discoverer) skillAt(ctx context.Context, repoURL, commit, dir string) (skillEntry, error) {
	owner, name, err := parseRepoURL(repoURL)
	if err != nil {
		return skillEntry{}, err
	}
	key := strings.ToLower(canonicalRepoURL(owner, name)) + "@" + commit + "#" + dir
	if e, ok := d.contents.Get(key); ok && (e.err == nil || time.Now().Before(e.expires)) {
		return e, e.err
	}
	v, _, _ := d.reads.Do("content "+key, func() (any, error) {
		// Shared by every caller waiting on key: one that gives up must not
		// fail the others. withSlot bounds the read.
		e := skillEntry{}
		e.content, e.files, e.err = d.readSkill(context.WithoutCancel(ctx), owner, name, commit, dir)
		switch {
		case e.err == nil:
			d.contents.Add(key, e)
		case errors.Is(e.err, ErrNotFound):
			e.expires = time.Now().Add(missTTL)
			d.contents.Add(key, e)
		}
		return e, nil
	})
	e := v.(skillEntry)
	return e, e.err
}

func (d *Discoverer) readSkill(ctx context.Context, owner, name, commit, dir string) (*SkillContent, []File, error) {
	repoURL := canonicalRepoURL(owner, name)
	var (
		raw  string
		tree gitTree
	)
	// SKILL.md first: a directory without one costs a single request, not a
	// recursive tree read.
	err := d.withSlot(ctx, func(ctx context.Context) error {
		var err error
		if raw, err = d.gh.raw(ctx, owner, name, joinPluginPath(dir, skillFile), commit); err != nil {
			return err
		}
		tree, err = d.tree(ctx, owner, name, commit, dir)
		return err
	})
	if err != nil {
		return nil, nil, contentError(repoURL, commit, err)
	}
	files := ownFiles(tree, dir)
	doc, body := splitFrontmatter(raw)
	fm := scalars(doc)
	n := min(len(files), MaxSkillFiles)
	c := &SkillContent{
		RepoURL:        repoURL,
		Path:           dir,
		Commit:         commit,
		Name:           skillName(fm, dir, name),
		Description:    strings.TrimSpace(fm["description"]),
		Tags:           metadataTags(doc, d.cfg.Tags),
		Instructions:   strings.TrimSpace(body),
		Files:          files[:n:n],
		FilesTruncated: len(files) > MaxSkillFiles || tree.Truncated,
	}
	return c, files, nil
}

// ownFiles are the blobs below dir that belong to its skill, relative to
// dir, SKILL.md first and the rest by path: a file below a nested SKILL.md
// is that skill's.
func ownFiles(tree gitTree, dir string) []File {
	nested := map[string]bool{}
	for _, e := range tree.Tree {
		if e.Type == "blob" && isSkillFile(e.Path) && skillDir(e.Path) != dir {
			nested[skillDir(e.Path)] = true
		}
	}
	var out []File
	for _, e := range tree.Tree {
		if e.Type != "blob" {
			continue
		}
		owned := true
		for p := path.Dir(e.Path); p != "." && p != dir; p = path.Dir(p) {
			if nested[p] {
				owned = false
				break
			}
		}
		if owned {
			rel := e.Path
			if dir != "" {
				rel = strings.TrimPrefix(e.Path, dir+"/")
			}
			out = append(out, File{Path: rel, Size: e.Size, SHA: e.SHA})
		}
	}
	slices.SortFunc(out, func(a, b File) int {
		switch {
		case a.Path == b.Path:
			return 0
		case a.Path == skillFile:
			return -1
		case b.Path == skillFile:
			return 1
		}
		return strings.Compare(a.Path, b.Path)
	})
	return out
}

// withSlot runs read with one of the content read slots, within
// contentReadTimeout of now; a read that waits past it fails as an upstream
// failure, so a burst of reads cannot hold GitHub's rate limit.
func (d *Discoverer) withSlot(ctx context.Context, read func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(ctx, contentReadTimeout)
	defer cancel()
	select {
	case d.contentSlots <- struct{}{}:
	case <-ctx.Done():
		return fmt.Errorf("%w: too many skill content reads in flight", ErrUpstream)
	}
	defer func() { <-d.contentSlots }()
	return read(ctx)
}

// contentError tells a definite miss (no such repository, commit or path)
// from a GitHub failure.
func contentError(repoURL, commit string, err error) error {
	var api *apiError
	switch {
	case errors.Is(err, ErrNotFound), errors.Is(err, ErrUpstream):
		return err
	case errors.As(err, &api) && (api.status == http.StatusNotFound || api.status == http.StatusUnprocessableEntity):
		return fmt.Errorf("%w: %s at %s: GitHub answered %d", ErrNotFound, repoURL, commit, api.status)
	}
	return fmt.Errorf("%w: %s at %s: %v", ErrUpstream, repoURL, commit, err)
}

func binaryByName(file string) bool {
	base := path.Base(file)
	dot := strings.LastIndexByte(base, '.')
	return dot > 0 && binaryExtensions[strings.ToLower(base[dot+1:])]
}
