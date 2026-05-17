package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/meloniteai/sidekick/internal/ipc"
)

func newHookCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "hook <event>",
		Short: "Forward an agent hook event to the running daemon",
		Long: `Reads Claude Code or Codex hook JSON from stdin and forwards a normalized event to the daemon.
Supported events:
  write  - file/tool write happened; triggers verifier recomputation

Goals are set by the agent itself via the sidekick_set_goal MCP tool.

Hooks must succeed silently and never block the agent, so any error here is
logged to stderr and the command always exits 0.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			defer func() { os.Exit(0) }() // never block the agent
			event := strings.ToLower(args[0])

			raw, _ := io.ReadAll(os.Stdin)

			switch event {
			case "write":
				files, err := hookFiles(raw)
				if err != nil {
					fmt.Fprintf(os.Stderr, "[sidekick hook] ignoring non-JSON stdin: %v\n", err)
				}
				if len(files) == 0 {
					// Codex hook payloads are still evolving. If the hook
					// fired but no path was obvious, trigger a verifier run
					// without a changed file rather than silently going stale.
					return forward(ipc.TypeWrite, ipc.WriteData{})
				}
				for _, file := range files {
					if err := forwardWrite(file); err != nil {
						return err
					}
				}
				return nil
			default:
				fmt.Fprintf(os.Stderr, "[sidekick hook] unknown event %q\n", event)
				return nil
			}
		},
	}
	return c
}

func hookFiles(raw []byte) ([]string, error) {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return nil, nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var files []string
	add := func(path string) {
		path = strings.TrimSpace(path)
		if path == "" || seen[path] {
			return
		}
		seen[path] = true
		files = append(files, path)
	}
	walkHookJSON(v, func(key, s string) {
		switch normalizeHookKey(key) {
		case "filepath", "notebookpath", "absolutefilepath", "relativepath":
			add(s)
		case "path":
			if looksLikeSourcePath(s) {
				add(s)
			}
		case "patch":
			for _, path := range patchFiles(s) {
				add(path)
			}
		}
		if strings.Contains(s, "*** Begin Patch") {
			for _, path := range patchFiles(s) {
				add(path)
			}
		}
	})
	return files, nil
}

func walkHookJSON(v any, visit func(key, value string)) {
	var walk func(key string, value any)
	walk = func(key string, value any) {
		switch x := value.(type) {
		case map[string]any:
			for k, child := range x {
				walk(k, child)
			}
		case []any:
			for _, child := range x {
				walk(key, child)
			}
		case string:
			visit(key, x)
			var nested any
			if strings.HasPrefix(strings.TrimSpace(x), "{") && json.Unmarshal([]byte(x), &nested) == nil {
				walk(key, nested)
			}
		}
	}
	walk("", v)
}

func normalizeHookKey(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, "_", "")
	s = strings.ReplaceAll(s, "-", "")
	return s
}

func looksLikeSourcePath(s string) bool {
	if s == "" || strings.ContainsAny(s, "\n\r{}") {
		return false
	}
	if strings.HasPrefix(s, ".") || strings.HasPrefix(s, "/") {
		return true
	}
	return strings.Contains(s, "/") || strings.Contains(s, "\\")
}

var patchFilePattern = regexp.MustCompile(`(?m)^\*\*\* (?:Add|Update|Delete) File: (.+)$`)

func patchFiles(patch string) []string {
	var files []string
	for _, match := range patchFilePattern.FindAllStringSubmatch(patch, -1) {
		if len(match) == 2 {
			files = append(files, strings.TrimSpace(match[1]))
		}
	}
	return files
}

func forwardWrite(file string) error {
	return forwardFrom(ipc.TypeWrite, ipc.WriteData{File: file}, hookRouteCWD(file))
}

func hookRouteCWD(file string) string {
	cwd, _ := os.Getwd()
	file = strings.TrimSpace(file)
	if file == "" || !filepath.IsAbs(file) {
		return cwd
	}
	if info, err := os.Stat(file); err == nil && info.IsDir() {
		return file
	}
	for dir := filepath.Dir(file); dir != "." && dir != string(filepath.Separator); dir = filepath.Dir(dir) {
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return dir
		}
	}
	return cwd
}

func forward(reqType string, data any) error {
	cwd, _ := os.Getwd()
	return forwardFrom(reqType, data, cwd)
}

func forwardFrom(reqType string, data any, cwd string) error {
	rawData, err := ipc.MarshalData(data)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[sidekick hook] marshal: %v\n", err)
		return nil
	}
	if _, err := ipc.SendFrom(ipc.Request{Type: reqType, Data: rawData}, cwd); err != nil {
		fmt.Fprintf(os.Stderr, "[sidekick hook] daemon unreachable: %v\n", err)
	}
	return nil
}
