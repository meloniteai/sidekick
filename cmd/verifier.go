package cmd

import (
	"bufio"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/uriahlevy/hud/internal/config"
	"github.com/uriahlevy/hud/internal/fetch"
	"github.com/uriahlevy/hud/internal/trust"
	"github.com/uriahlevy/hud/internal/verifier"
)

// newVerifierCmd assembles the `hud verifier ...` command tree. Today it
// holds `add <url>` (fetch + pin a remote SKILL.md or script and register
// it in hud.yaml) and `list` (print the configured verifiers). The intent
// is to keep this the user-facing CLI surface for managing the community
// verifier set, leaving hud.yaml hand-edits as the escape hatch.
func newVerifierCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "verifier",
		Short: "Manage HUD verifiers (add remote, list, etc.)",
	}
	cmd.AddCommand(newVerifierAddCmd())
	cmd.AddCommand(newVerifierListCmd())
	cmd.AddCommand(newVerifierTrustCmd())
	return cmd
}

func newVerifierTrustCmd() *cobra.Command {
	var (
		configPath string
		all        bool
		listOnly   bool
		revoke     bool
	)
	cmd := &cobra.Command{
		Use:   "trust [verifier-name ...]",
		Short: "Approve remote verifiers so HUD will run them",
		Long: `Each remote verifier (those with a ` + "`source:`" + ` block in hud.yaml) must
be explicitly trusted before HUD will execute it. Trust is recorded by
sha256 in ~/.hud/trust.json.

  hud verifier trust MyVerifier              approve one verifier by name
  hud verifier trust --all                   approve every pending verifier
  hud verifier trust --list                  show currently approved hashes
  hud verifier trust --revoke MyVerifier     revoke approval

Local verifiers (no source: block) are implicitly trusted.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := trust.New("")
			if err != nil {
				return err
			}
			_ = store.Load()

			if listOnly {
				approvals := store.Approvals()
				if len(approvals) == 0 {
					fmt.Fprintln(cmd.OutOrStdout(), "no approved verifiers in trust.json")
					return nil
				}
				for sha, e := range approvals {
					fmt.Fprintf(cmd.OutOrStdout(), "%s  %s  %s  (%s)\n", sha[:12], e.Verifier, e.URL, e.ApprovedAt.Format("2006-01-02"))
				}
				return nil
			}

			f, path, err := config.Load(configPath)
			if err != nil {
				return fmt.Errorf("load hud.yaml: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "hud.yaml: %s\n", path)

			targets := map[string]bool{}
			for _, n := range args {
				targets[strings.ToLower(n)] = true
			}

			approvedAny := false
			revokedAny := false
			for _, vs := range f.Verifiers {
				if vs.Source == nil || vs.Source.URL == "" {
					continue
				}
				if !all && len(targets) > 0 && !targets[strings.ToLower(vs.Name)] {
					continue
				}
				if !all && len(targets) == 0 {
					return fmt.Errorf("no verifier names supplied — use --all to approve every pending verifier or pass names explicitly")
				}
				if revoke {
					if store.Revoke(vs.Source.SHA256) {
						fmt.Fprintf(cmd.OutOrStdout(), "revoked: %s (%s)\n", vs.Name, short(vs.Source.SHA256))
						revokedAny = true
					}
					continue
				}
				if store.IsApproved(vs.Source.SHA256) {
					fmt.Fprintf(cmd.OutOrStdout(), "already trusted: %s (%s)\n", vs.Name, short(vs.Source.SHA256))
					continue
				}
				store.Approve(vs.Source.SHA256, trust.Entry{
					URL:      vs.Source.URL,
					Verifier: vs.Name,
				})
				approvedAny = true
				fmt.Fprintf(cmd.OutOrStdout(), "approved: %s (%s)  from %s\n", vs.Name, short(vs.Source.SHA256), vs.Source.URL)
			}
			if approvedAny || revokedAny {
				if err := store.Save(); err != nil {
					return fmt.Errorf("save trust.json: %w", err)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&configPath, "config", "", "path to hud.yaml")
	cmd.Flags().BoolVar(&all, "all", false, "approve every remote verifier currently in hud.yaml")
	cmd.Flags().BoolVar(&listOnly, "list", false, "list approved sha256 entries from trust.json and exit")
	cmd.Flags().BoolVar(&revoke, "revoke", false, "revoke approval for the named verifier(s)")
	return cmd
}

func newVerifierListCmd() *cobra.Command {
	var configPath string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "Print the verifiers configured in hud.yaml",
		RunE: func(cmd *cobra.Command, args []string) error {
			f, path, err := config.Load(configPath)
			if err != nil {
				return err
			}
			fmt.Printf("hud.yaml: %s\n\n", path)
			vs, err := f.Resolve(filepath.Dir(path))
			if err != nil {
				// Print whatever we have even if resolution fails so the
				// user can see where the bad entry lives.
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: %v\n\n", err)
			}
			for i, vsp := range f.Verifiers {
				fmt.Printf("%d. %s [%s, %s]\n", i+1, vsp.Name, displayType(vsp.Type), vsp.Direction)
				if vsp.Source != nil && vsp.Source.URL != "" {
					fmt.Printf("   source: %s\n", vsp.Source.URL)
					fmt.Printf("   sha256: %s\n", short(vsp.Source.SHA256))
				}
				if vsp.LLM.Skill != "" {
					fmt.Printf("   skill: %s\n", vsp.LLM.Skill)
				}
				if len(vsp.Command) > 0 {
					fmt.Printf("   command: %s\n", strings.Join(vsp.Command, " "))
				}
				if vsp.Disabled {
					fmt.Printf("   (disabled)\n")
				}
			}
			fmt.Printf("\n%d resolved verifier(s).\n", len(vs))
			return nil
		},
	}
	cmd.Flags().StringVar(&configPath, "config", "", "path to hud.yaml")
	return cmd
}

func newVerifierAddCmd() *cobra.Command {
	var (
		configPath  string
		name        string
		direction   string
		kind        string
		yes         bool
		permissions string
		local       bool
	)
	cmd := &cobra.Command{
		Use:   "add [url]",
		Short: "Register a verifier in hud.yaml — either a pinned remote URL or a local one via the interactive wizard",
		Long: `Add a verifier to hud.yaml.

Remote mode (default):
` + "  `hud verifier add <url>`" + ` downloads the URL once, displays the first 20
  lines of the content, prompts for confirmation, computes the sha256, and
  writes a new entry into hud.yaml that pins the artefact by hash. Subsequent
  loads of the verifier go through the on-disk cache; HUD refuses to use any
  content whose hash has drifted from the pin.

  URLs are heuristically classified by extension and content:
    *.md or first line "---" -> agent verifier (SKILL.md)
    anything else            -> command verifier (executable script)

  Override the heuristic with --type {agent,command,binary}.

Local mode:
` + "  `hud verifier add --local`" + ` runs an interactive field-by-field wizard
  that prompts for name, direction, type, command/skill path, timeout, and
  optional advisory permissions, then writes the entry to hud.yaml without
  any URL or sha256 pin. Pre-set --name/--direction/--type/--permissions
  flags become the defaults the wizard suggests.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if local {
				if len(args) > 0 {
					return fmt.Errorf("--local takes no <url> argument")
				}
				return runLocalVerifierWizard(cmd, configPath, name, direction, kind, permissions, yes)
			}
			if len(args) == 0 {
				return fmt.Errorf("provide a <url> to pin a remote verifier, or pass --local to create one interactively")
			}
			rawURL := args[0]
			parsed, err := url.Parse(rawURL)
			if err != nil || parsed.Scheme == "" {
				return fmt.Errorf("invalid url: %s", rawURL)
			}
			body, err := fetch.Download(rawURL)
			if err != nil {
				return fmt.Errorf("download %s: %w", rawURL, err)
			}
			sha := fetch.Hash(body)

			detectedType := classifyArtefact(rawURL, body)
			if kind == "" {
				kind = detectedType
			}
			if name == "" {
				name = guessName(rawURL, kind)
			}
			if direction == "" {
				direction = "NE"
			}

			fmt.Fprintf(cmd.OutOrStdout(),
				"\n--- preview of %s (sha256 %s) ---\n%s\n--- end preview ---\n\n",
				rawURL, sha, headBytes(body, 20),
			)
			fmt.Fprintf(cmd.OutOrStdout(),
				"Add as: name=%q  type=%s  direction=%s\n", name, kind, direction)

			if !yes {
				if !confirm(cmd.InOrStdin(), cmd.OutOrStdout(), "Add this verifier to hud.yaml?") {
					return errors.New("aborted")
				}
			}

			f, path, loaded, err := loadOrInit(configPath)
			if err != nil {
				return err
			}
			if hasVerifier(f, name) {
				return fmt.Errorf("verifier %q already exists in %s — pick a different --name or remove the old entry first", name, path)
			}
			spec := config.VerifierSpec{
				Name:      name,
				Direction: direction,
				Type:      kind,
				Source: &config.SourceSpec{
					URL:    rawURL,
					SHA256: sha,
				},
			}
			if permissions != "" {
				if err := applyPermissionsFlag(&spec, permissions); err != nil {
					return err
				}
			}
			f.Verifiers = append(f.Verifiers, spec)

			if err := config.Save(path, f); err != nil {
				return fmt.Errorf("save %s: %w", path, err)
			}
			// User just confirmed the preview — record their approval in
			// trust.json so the next `hud start` doesn't re-prompt.
			store, terr := trust.New("")
			if terr == nil {
				_ = store.Load()
				store.Approve(sha, trust.Entry{
					URL:      rawURL,
					Verifier: name,
				})
				if err := store.Save(); err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not write trust.json: %v\n", err)
				}
			}
			if loaded {
				fmt.Fprintf(cmd.OutOrStdout(), "\nUpdated %s.\n", path)
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "\nWrote %s.\n", path)
			}
			fmt.Fprintf(cmd.OutOrStdout(),
				"Restart `hud start` to pick up the new verifier (or trigger a config reload from the TUI editor).\n")
			return nil
		},
	}
	cmd.Flags().StringVar(&configPath, "config", "", "path to hud.yaml (default: nearest hud.yaml above cwd, else ./hud.yaml)")
	cmd.Flags().StringVar(&name, "name", "", "verifier name (default: derived from URL filename)")
	cmd.Flags().StringVar(&direction, "direction", "", "compass direction (default: NE)")
	cmd.Flags().StringVar(&kind, "type", "", "verifier type: agent | command | binary (default: detect)")
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the interactive confirmation prompt")
	cmd.Flags().StringVar(&permissions, "permissions", "",
		`advisory permissions, comma-separated. e.g. "fs=read-only,network=false" — surfaced in the TUI on first run.`)
	cmd.Flags().BoolVar(&local, "local", false, "skip the URL fetch and create a local verifier interactively (no <url> argument)")
	return cmd
}

// classifyArtefact decides whether a freshly fetched body is a SKILL.md
// (agent) or a script (command). Cheap content sniffing only — the user
// can always override with --type.
func classifyArtefact(rawURL string, body []byte) string {
	low := strings.ToLower(rawURL)
	switch {
	case strings.HasSuffix(low, ".md"), strings.HasSuffix(low, "/skill.md"):
		return verifier.TypeAgent
	case strings.HasSuffix(low, ".sh"), strings.HasSuffix(low, ".py"), strings.HasSuffix(low, ".bash"):
		return verifier.TypeCommand
	}
	first := firstLine(body)
	if strings.TrimSpace(first) == "---" {
		return verifier.TypeAgent
	}
	if strings.HasPrefix(first, "#!") {
		return verifier.TypeCommand
	}
	return verifier.TypeCommand
}

func firstLine(body []byte) string {
	if i := indexByte(body, '\n'); i >= 0 {
		return string(body[:i])
	}
	return string(body)
}

func indexByte(b []byte, c byte) int {
	for i, x := range b {
		if x == c {
			return i
		}
	}
	return -1
}

// guessName picks a default verifier name from the URL filename. e.g.
// https://example.com/perf/SKILL.md -> "Perf"; .../coverage.sh -> "Coverage".
func guessName(rawURL, kind string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "Custom"
	}
	base := filepath.Base(parsed.Path)
	switch base {
	case "SKILL.md", "skill.md":
		// Use the parent directory as the name in that case.
		dir := filepath.Base(filepath.Dir(parsed.Path))
		if dir != "" && dir != "/" {
			base = dir
		}
	}
	base = strings.TrimSuffix(base, filepath.Ext(base))
	if base == "" {
		return "Custom"
	}
	return strings.ToUpper(base[:1]) + base[1:]
}

func displayType(t string) string {
	if t == "" {
		return "command"
	}
	if t == "llm" {
		return "agent"
	}
	return t
}

func short(s string) string {
	if len(s) > 12 {
		return s[:12] + "…"
	}
	return s
}

func headBytes(body []byte, n int) string {
	lines := strings.SplitAfter(string(body), "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "")
}

// confirm reads a y/n line from stdin, returning true on yes/y (case-
// insensitive). Empty input is treated as no.
func confirm(in interface{ Read([]byte) (int, error) }, out interface{ Write([]byte) (int, error) }, prompt string) bool {
	fmt.Fprintf(out.(*os.File), "%s [y/N] ", prompt)
	r := bufio.NewReader(asReader(in))
	line, _ := r.ReadString('\n')
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes"
}

func asReader(in interface{ Read([]byte) (int, error) }) *os.File {
	if f, ok := in.(*os.File); ok {
		return f
	}
	return os.Stdin
}

// loadOrInit returns (parsed file, on-disk path, true) if hud.yaml exists,
// or (empty file, default path, false) if not. The default path when none
// is supplied is ./hud.yaml in the current working directory — the same
// place `hud start` looks for it via findUpwards.
func loadOrInit(configPath string) (*config.File, string, bool, error) {
	f, path, err := config.Load(configPath)
	if err == nil {
		return f, path, true, nil
	}
	if !errors.Is(err, os.ErrNotExist) && !errors.Is(err, errFileMissing) {
		return nil, "", false, err
	}
	// No hud.yaml found anywhere upward. Create one in cwd.
	cwd, wderr := os.Getwd()
	if wderr != nil {
		return nil, "", false, wderr
	}
	if configPath != "" {
		// User supplied a path that doesn't exist yet; honour it.
		return &config.File{GoalSource: "prompt"}, configPath, false, nil
	}
	return &config.File{GoalSource: "prompt"}, filepath.Join(cwd, "hud.yaml"), false, nil
}

// errFileMissing is a sentinel for the "no hud.yaml found" case used by
// loadOrInit. config.Load returns os.ErrNotExist directly so this is
// effectively unused, but kept for readability of the err path.
var errFileMissing = errors.New("hud.yaml not found")

func hasVerifier(f *config.File, name string) bool {
	for _, v := range f.Verifiers {
		if strings.EqualFold(v.Name, name) {
			return true
		}
	}
	return false
}

// applyPermissionsFlag parses a comma-separated key=value list (e.g.
// "fs=read-only,network=true,env=PATH:GOPATH") into a PermissionsSpec.
// Unknown keys are rejected so a typo doesn't silently ship as
// "no opinion declared".
func applyPermissionsFlag(spec *config.VerifierSpec, raw string) error {
	p := &config.PermissionsSpec{}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			return fmt.Errorf("permissions: %q is not key=value", part)
		}
		key := strings.ToLower(strings.TrimSpace(kv[0]))
		val := strings.TrimSpace(kv[1])
		switch key {
		case "fs", "filesystem":
			p.Filesystem = val
		case "network", "net":
			p.Network = strings.EqualFold(val, "true") || val == "1" || strings.EqualFold(val, "yes")
		case "env":
			for _, e := range strings.Split(val, ":") {
				if e = strings.TrimSpace(e); e != "" {
					p.Env = append(p.Env, e)
				}
			}
		default:
			return fmt.Errorf("permissions: unknown key %q (want fs, network, env)", key)
		}
	}
	spec.Permissions = p
	return nil
}
