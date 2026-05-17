// Package transcript reads recent agent messages out of Claude Code's
// per-session JSONL transcripts so Sidekick verifiers can ground their judgment
// in what the agent has actually been saying — not just file diffs.
//
// CC writes one JSONL file per session under
//
//	$HOME/.claude/projects/<project-key>/<session-id>.jsonl
//
// where <project-key> is the cwd with separators replaced by dashes.
package transcript

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// LastMessages returns up to `n` most recent assistant + user text turns from
// the active CC session for the given project root. Best-effort: returns nil
// silently if no transcript can be located.
func LastMessages(projectRoot string, n int) []string {
	path, err := latestSessionFile(projectRoot)
	if err != nil || path == "" {
		return nil
	}
	return tailMessages(path, n)
}

func latestSessionFile(projectRoot string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(projectRoot)
	if err != nil {
		return "", err
	}
	key := strings.ReplaceAll(abs, string(filepath.Separator), "-")
	dir := filepath.Join(home, ".claude", "projects", key)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	type fi struct {
		path string
		mod  int64
	}
	var jsonl []fi
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		jsonl = append(jsonl, fi{filepath.Join(dir, e.Name()), info.ModTime().UnixNano()})
	}
	if len(jsonl) == 0 {
		return "", nil
	}
	sort.Slice(jsonl, func(i, j int) bool { return jsonl[i].mod > jsonl[j].mod })
	return jsonl[0].path, nil
}

// turn is a minimal subset of the CC JSONL message envelope.
type turn struct {
	Type    string `json:"type"` // "user" | "assistant"
	Message struct {
		Role    string `json:"role"`
		Content any    `json:"content"`
	} `json:"message"`
}

func tailMessages(path string, n int) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var msgs []string
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var t turn
		if err := json.Unmarshal(line, &t); err != nil {
			continue
		}
		if t.Type != "user" && t.Type != "assistant" {
			continue
		}
		text := flattenContent(t.Message.Content)
		if text == "" {
			continue
		}
		role := t.Message.Role
		if role == "" {
			role = t.Type
		}
		msgs = append(msgs, role+": "+truncate(text, 1000))
	}
	if len(msgs) <= n {
		return msgs
	}
	return msgs[len(msgs)-n:]
}

// flattenContent handles both string and Anthropic-block-array formats.
func flattenContent(c any) string {
	switch v := c.(type) {
	case string:
		return v
	case []any:
		var parts []string
		for _, item := range v {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if t, _ := m["type"].(string); t == "text" {
				if s, _ := m["text"].(string); s != "" {
					parts = append(parts, s)
				}
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
