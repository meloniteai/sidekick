// Package gitstats fetches lightweight workspace metadata used by the Sidekick
// header: the current worktree name, the active branch, and per-file line
// add/remove counts since the session base ref.
//
// All commands shell out to `git`. Errors are surfaced as zero-value fields
// in the result so the TUI never has to special-case a missing repo: a
// session running outside git renders an empty git row instead of crashing.
package gitstats

import (
	"bufio"
	"context"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// FileStat is the per-file +added/-removed counts surfaced by the panel.
// Binary files (numstat reports "-\t-") are flagged via Binary; callers can
// render them with a marker instead of zeros.
type FileStat struct {
	Path    string
	Added   int
	Removed int
	Binary  bool
}

// Workspace bundles every git-derived field rendered in the Sidekick header and
// the toggleable per-file panel.
//
// BaseRefUnset signals that no session base ref was provided to Fetch, so
// Files is empty for that reason rather than because nothing changed. The
// panel renders an explanatory hint instead of "(no files edited yet)".
type Workspace struct {
	WorktreeName string
	Branch       string
	Files        []FileStat
	TotalAdded   int
	TotalRemoved int
	BaseRefUnset bool
}

// Fetch collects the workspace summary. baseRef is the session base SHA;
// extraFiles are paths reported via the agent write hook so that files
// touched without producing a tracked diff (e.g. created then reverted)
// still appear in the per-file panel.
//
// A short per-command timeout keeps a hung git process from blocking the
// TUI tick loop.
func Fetch(ctx context.Context, worktree, baseRef string, extraFiles []string) Workspace {
	var ws Workspace
	ws.WorktreeName = worktreeName(ctx, worktree)
	ws.Branch = currentBranch(ctx, worktree)
	ws.BaseRefUnset = baseRef == ""
	files := diffNumstat(ctx, worktree, baseRef)

	known := map[string]int{}
	for i, f := range files {
		known[f.Path] = i
	}
	for _, p := range extraFiles {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if _, ok := known[p]; ok {
			continue
		}
		// Hook-reported file that doesn't appear in the diff (e.g. the agent
		// touched it then reverted, or it's outside the repo). Append with
		// zero counts so the user can still see it was edited.
		known[p] = len(files)
		files = append(files, FileStat{Path: p})
	}

	sort.SliceStable(files, func(i, j int) bool {
		// Heaviest churn first; ties broken alphabetically so the panel is
		// stable across refreshes.
		ci := files[i].Added + files[i].Removed
		cj := files[j].Added + files[j].Removed
		if ci != cj {
			return ci > cj
		}
		return files[i].Path < files[j].Path
	})

	ws.Files = files
	for _, f := range files {
		if f.Binary {
			continue
		}
		ws.TotalAdded += f.Added
		ws.TotalRemoved += f.Removed
	}
	return ws
}

// withTimeout runs `git args...` with a 1.5s cap so the TUI never blocks on
// a slow git invocation. Empty output is returned on any error.
func gitRun(ctx context.Context, worktree string, args ...string) string {
	if _, err := exec.LookPath("git"); err != nil {
		return ""
	}
	cctx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	cmd := exec.CommandContext(cctx, "git", args...)
	if strings.TrimSpace(worktree) != "" {
		cmd.Dir = worktree
	}
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimRight(string(out), "\n")
}

func worktreeName(ctx context.Context, worktree string) string {
	top := gitRun(ctx, worktree, "rev-parse", "--show-toplevel")
	if top == "" {
		return ""
	}
	return filepath.Base(top)
}

func currentBranch(ctx context.Context, worktree string) string {
	// --abbrev-ref returns "HEAD" when detached; the user still wants to know
	// they're detached, so we pass the literal "HEAD" through unchanged.
	return gitRun(ctx, worktree, "rev-parse", "--abbrev-ref", "HEAD")
}

// diffNumstat runs `git diff --numstat <baseRef>` and parses the rows.
// Format per line: "<added>\t<removed>\t<path>". Binary files use "-\t-".
func diffNumstat(ctx context.Context, worktree, baseRef string) []FileStat {
	if baseRef == "" {
		return nil
	}
	out := gitRun(ctx, worktree, "diff", "--numstat", baseRef)
	if out == "" {
		return nil
	}
	var files []FileStat
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		f, ok := parseNumstatLine(sc.Text())
		if !ok {
			continue
		}
		files = append(files, f)
	}
	return files
}

func parseNumstatLine(line string) (FileStat, bool) {
	parts := strings.SplitN(line, "\t", 3)
	if len(parts) != 3 {
		return FileStat{}, false
	}
	a, aErr := strconv.Atoi(parts[0])
	r, rErr := strconv.Atoi(parts[1])
	if parts[0] == "-" && parts[1] == "-" {
		return FileStat{Path: parts[2], Binary: true}, true
	}
	if aErr != nil || rErr != nil {
		return FileStat{}, false
	}
	return FileStat{Path: parts[2], Added: a, Removed: r}, true
}
