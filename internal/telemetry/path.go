package telemetry

import (
	"path/filepath"
	"strings"
)

// NormalizeRepoPath returns the repo-relative, forward-slash key for p so the
// edit and finding streams join on one key per file. Returns "" when no clean
// key exists (empty input, absolute path with no worktree, or a ".." escape);
// callers map "" as they need — a finding to tree-global, an edit to the raw path.
func NormalizeRepoPath(worktree, p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if filepath.IsAbs(p) {
		if worktree == "" {
			return ""
		}
		rel, err := filepath.Rel(worktree, p)
		if err != nil {
			return ""
		}
		p = rel
	}
	p = filepath.Clean(p)
	if p == "." || p == ".." || strings.HasPrefix(p, ".."+string(filepath.Separator)) {
		return ""
	}
	return filepath.ToSlash(p)
}
