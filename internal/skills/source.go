package skills

import (
	"encoding/json"
	"fmt"
	"path"
	"strings"
)

// Source is one configured skill repository: its URL and, optionally, the
// ref discovery reads and agents pin (empty: the default branch's head), the
// directory discovery scans (empty: the whole repository) and readOnly.
// skill-manager only reports readOnly — it has no write path into skill
// repositories; consumers that write skills (the portal) honour it.
type Source struct {
	URL      string `json:"url"`
	Ref      string `json:"ref,omitempty"`
	Path     string `json:"path,omitempty"`
	ReadOnly bool   `json:"readOnly,omitempty"`
}

// UnmarshalJSON takes a plain URL string or the object form.
func (s *Source) UnmarshalJSON(data []byte) error {
	var url string
	if err := json.Unmarshal(data, &url); err == nil {
		*s = Source{URL: url}
		return nil
	}
	type plain Source
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return fmt.Errorf("a skill repository is a URL or {url, ref, path, readOnly}: %w", err)
	}
	*s = Source(p)
	return nil
}

// ParseSources reads the configured skill repositories: a JSON array whose
// items are a URL or {url, ref, path, readOnly}, or comma-separated URLs.
func ParseSources(value string) ([]Source, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	var out []Source
	if strings.HasPrefix(value, "[") {
		if err := json.Unmarshal([]byte(value), &out); err != nil {
			return nil, fmt.Errorf("skill repositories: %w", err)
		}
	} else {
		for _, url := range strings.Split(value, ",") {
			out = append(out, Source{URL: url})
		}
	}
	kept := out[:0]
	for i, s := range out {
		s.URL = strings.TrimSpace(s.URL)
		s.Ref = strings.TrimSpace(s.Ref)
		switch {
		case s.URL == "" && s.Ref == "" && strings.TrimSpace(s.Path) == "":
			continue // an empty item of a comma-separated list
		case s.URL == "":
			return nil, fmt.Errorf("skill repository %d has no url", i)
		}
		if err := CheckRef(s.Ref); err != nil {
			return nil, fmt.Errorf("skill repository %s: %w", s.URL, err)
		}
		cleaned, err := CleanPath(s.Path)
		if err != nil {
			return nil, fmt.Errorf("skill repository %s: %w", s.URL, err)
		}
		s.Path = cleaned
		for _, prev := range kept {
			if SameRepository(prev.URL, s.URL) && (within(s.Path, prev.Path) || within(prev.Path, s.Path) || s.Path == prev.Path) {
				return nil, fmt.Errorf("skill repository %s is configured twice with overlapping paths (%q and %q): give it once, or paths that do not contain each other", s.URL, prev.Path, s.Path)
			}
		}
		kept = append(kept, s)
	}
	if len(kept) == 0 {
		return nil, nil
	}
	return kept, nil
}

// SameRepository compares two repository URLs by their canonical form.
func SameRepository(a, b string) bool {
	return canonicalOrRaw(a) == canonicalOrRaw(b)
}

func canonicalOrRaw(url string) string {
	if owner, name, err := parseRepoURL(url); err == nil {
		return strings.ToLower(canonicalRepoURL(owner, name))
	}
	return strings.ToLower(strings.TrimRight(url, "/"))
}

// CheckRef refuses a ref that is not a git ref name (git check-ref-format:
// no "..", no component that is empty or starts with ".", no control
// character, space, ~ ^ : ? * [ or \, no "@{", not "@", not ending in "/",
// "." or ".lock"). A ref goes into a GitHub API path, which GitHub resolves
// dot segments in: "../../<other repository>" would ask about a repository
// the caller's readability was never checked for. "" (the default branch) is
// valid.
func CheckRef(ref string) error {
	if ref == "" {
		return nil
	}
	bad := ref == "@" || strings.Contains(ref, "..") || strings.Contains(ref, "@{") ||
		strings.HasSuffix(ref, "/") || strings.HasSuffix(ref, ".") || strings.HasSuffix(ref, ".lock") ||
		strings.ContainsAny(ref, " ~^:?*[\\")
	for _, r := range ref {
		if r < 0x20 || r == 0x7f {
			bad = true
		}
	}
	for _, seg := range strings.Split(ref, "/") {
		if seg == "" || strings.HasPrefix(seg, ".") {
			bad = true
		}
	}
	if bad {
		return fmt.Errorf("ref %q is not a valid git ref name", ref)
	}
	return nil
}

// CleanPath is a directory in a repository in its one form: relative,
// cleaned (./a//b/ is a/b), "" for the root. An absolute path or one that
// climbs out with .. is refused. Source paths and item paths both take it.
func CleanPath(raw string) (string, error) {
	p := strings.TrimSpace(raw)
	if strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("path %q must be relative to the repository root", raw)
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return "", fmt.Errorf("path %q must not climb out of the repository", raw)
		}
	}
	if p = path.Clean(p); p == "." {
		return "", nil
	}
	return p, nil
}
