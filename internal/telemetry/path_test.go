package telemetry

import "testing"

func TestNormalizeRepoPath(t *testing.T) {
	const wt = "/repo/.claude/worktrees/wt"
	cases := []struct {
		name     string
		worktree string
		in       string
		want     string
	}{
		{"empty", wt, "", ""},
		{"absolute inside worktree", wt, "/repo/.claude/worktrees/wt/internal/x.go", "internal/x.go"},
		{"absolute with no worktree to anchor", "", "/repo/internal/x.go", ""},
		{"absolute escaping the worktree", wt, "/repo/other/x.go", ""},
		{"already relative", wt, "cmd/start.go", "cmd/start.go"},
		{"relative cleaned of redundant parts", wt, "internal/./a/../start.go", "internal/start.go"},
		{"surrounding whitespace trimmed", wt, "  cmd/start.go  ", "cmd/start.go"},
		{"worktree root itself", wt, wt, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NormalizeRepoPath(tc.worktree, tc.in); got != tc.want {
				t.Errorf("NormalizeRepoPath(%q, %q) = %q, want %q", tc.worktree, tc.in, got, tc.want)
			}
		})
	}
}
