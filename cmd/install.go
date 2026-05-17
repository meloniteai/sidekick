package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"

	"github.com/meloniteai/sidekick/internal/install"
)

// newInstallCmd wires sidekick into the user's agent clients (Claude Code,
// Codex). It is the "second half" of the bootstrap that `install.sh`
// kicks off after dropping the binary into $PATH — but it's also the
// canonical way to re-wire integrations after a `sidekick` upgrade.
func newInstallCmd() *cobra.Command {
	var (
		assumeYes  bool
		dryRun     bool
		forceFlags = map[install.Agent]*bool{
			install.AgentClaude: new(bool),
			install.AgentCodex:  new(bool),
		}
		skipFlags = map[install.Agent]*bool{
			install.AgentClaude: new(bool),
			install.AgentCodex:  new(bool),
		}
		skipSkill bool
		skipHook  bool
		skipMCP   bool
	)

	c := &cobra.Command{
		Use:   "install",
		Short: "Wire sidekick into Claude Code and/or Codex (skill + MCP + write hook)",
		Long: `Install the sidekick skill, register the MCP server, and merge the
PostToolUse write hook into the user-scope settings for any agent client
that's installed.

Re-running this command after a sidekick upgrade is safe and recommended — the
skill is rewritten in place to match the newly-installed binary, the MCP
registration is idempotent at the agent CLI's discretion, and the hook
merge is a no-op when the desired entry is already present.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			home, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("resolve home dir: %w", err)
			}

			// Auto-yes when stdin is not a TTY (curl|bash piped case).
			stdinIsTTY := isatty.IsTerminal(os.Stdin.Fd())
			if !stdinIsTTY {
				assumeYes = true
			}

			detections := install.DetectAll(home)
			plan := []install.Agent{}
			for _, d := range detections {
				force := *forceFlags[d.Agent]
				skip := *skipFlags[d.Agent]
				switch {
				case skip:
					continue
				case force:
					plan = append(plan, d.Agent)
				case d.Found:
					plan = append(plan, d.Agent)
				}
			}

			if len(plan) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No supported agent detected. Use --claude or --codex to force-install.")
				return nil
			}

			confirmed := plan
			if !assumeYes && !dryRun {
				confirmed = confirmedAgents(cmd.InOrStdin(), cmd.OutOrStdout(), detections, plan)
				if len(confirmed) == 0 {
					fmt.Fprintln(cmd.OutOrStdout(), "Nothing selected — exiting.")
					return nil
				}
			}

			if len(sidekickSkillBody) == 0 && !skipSkill {
				return errors.New("internal: SKILL.md embed is empty (did the build skip //go:embed?)")
			}

			for _, agent := range confirmed {
				if err := runOneAgent(cmd, home, agent, runOpts{
					DryRun:    dryRun,
					SkipSkill: skipSkill,
					SkipMCP:   skipMCP,
					SkipHook:  skipHook,
				}); err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "  %s: %v\n", agent, err)
				}
			}

			if dryRun {
				fmt.Fprintln(cmd.OutOrStdout(), "\nDry run — no files written.")
				return nil
			}
			fmt.Fprintln(cmd.OutOrStdout(), "\nNext: run `sidekick start` in a repo to launch the daemon and TUI.")
			return nil
		},
	}

	c.Flags().BoolVarP(&assumeYes, "yes", "y", false, "Skip confirmation prompts (default when stdin is not a TTY)")
	c.Flags().BoolVar(&dryRun, "print", false, "Print what would happen without writing anything")
	c.Flags().BoolVar(forceFlags[install.AgentClaude], "claude", false, "Force-include Claude regardless of detection")
	c.Flags().BoolVar(skipFlags[install.AgentClaude], "no-claude", false, "Skip Claude even if detected")
	c.Flags().BoolVar(forceFlags[install.AgentCodex], "codex", false, "Force-include Codex regardless of detection")
	c.Flags().BoolVar(skipFlags[install.AgentCodex], "no-codex", false, "Skip Codex even if detected")
	c.Flags().BoolVar(&skipSkill, "skip-skill", false, "Don't copy the sidekick skill")
	c.Flags().BoolVar(&skipMCP, "skip-mcp", false, "Don't register the MCP server")
	c.Flags().BoolVar(&skipHook, "skip-hook", false, "Don't merge the PostToolUse write hook")
	return c
}

type runOpts struct {
	DryRun    bool
	SkipSkill bool
	SkipMCP   bool
	SkipHook  bool
}

func runOneAgent(cmd *cobra.Command, home string, agent install.Agent, opts runOpts) error {
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "\n%s:\n", titleCase(string(agent)))

	// Skill
	if !opts.SkipSkill {
		dirs := install.SkillDirs(home, "sidekick", agent)
		if opts.DryRun {
			for _, d := range dirs {
				fmt.Fprintf(out, "  skill  would write %s/SKILL.md\n", d)
			}
		} else {
			written, err := install.WriteSkill(home, agent, "sidekick", sidekickSkillBody, nil)
			if err != nil {
				return fmt.Errorf("write skill: %w", err)
			}
			for _, p := range written {
				fmt.Fprintf(out, "  skill  wrote %s\n", p)
			}
		}
	}

	// MCP
	if !opts.SkipMCP {
		_, argv, err := dryOrRegisterMCP(agent, opts.DryRun, out, cmd.ErrOrStderr())
		switch {
		case err == nil:
			fmt.Fprintf(out, "  mcp    %s\n", joinArgv(argv))
		case errors.Is(err, exec.ErrNotFound):
			fmt.Fprintf(out, "  mcp    skipped — `%s` not on PATH\n", agent)
		default:
			fmt.Fprintf(out, "  mcp    `%s` exited non-zero (already registered?)\n", joinArgv(argv))
		}
	}

	// Hook
	if !opts.SkipHook {
		path := install.HookFilePath(home, agent)
		cfg := install.CanonicalHookConfig(agent)
		if opts.DryRun {
			existing, _ := os.ReadFile(path)
			_, changed, err := install.MergeHook(existing, cfg)
			if err != nil {
				return fmt.Errorf("plan hook merge: %w", err)
			}
			if !changed {
				fmt.Fprintf(out, "  hook   already present in %s\n", path)
			} else {
				fmt.Fprintf(out, "  hook   would merge %q → %s\n", cfg.Matcher, path)
			}
		} else {
			changed, err := install.MergeHookFile(path, cfg)
			if err != nil {
				return fmt.Errorf("merge hook: %w", err)
			}
			if changed {
				fmt.Fprintf(out, "  hook   merged %q → %s\n", cfg.Matcher, path)
			} else {
				fmt.Fprintf(out, "  hook   already present in %s\n", path)
			}
		}
	}
	return nil
}

func dryOrRegisterMCP(agent install.Agent, dryRun bool, stdout, stderr io.Writer) (bool, []string, error) {
	if dryRun {
		// Construct the argv without running anything.
		_, argv, err := install.RegisterMCP(agent, io.Discard, io.Discard)
		if err == nil {
			return false, argv, nil
		}
		// If the CLI isn't on PATH we still want to surface the argv for visibility.
		return false, argv, err
	}
	return install.RegisterMCP(agent, stdout, stderr)
}

func joinArgv(argv []string) string {
	return strings.Join(argv, " ")
}

func titleCase(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// confirmedAgents asks the user which detected agents to install for.
// Returns the subset they confirmed. Reads "y" / "n" / "" (default y)
// per agent from r and writes the prompts to w.
func confirmedAgents(r io.Reader, w io.Writer, all []install.Detection, plan []install.Agent) []install.Agent {
	planned := map[install.Agent]bool{}
	for _, a := range plan {
		planned[a] = true
	}
	var out []install.Agent
	for _, d := range all {
		if !planned[d.Agent] {
			continue
		}
		detail := []string{}
		if d.HasCLI {
			detail = append(detail, "cli")
		}
		if d.HasDir {
			detail = append(detail, "config")
		}
		fmt.Fprintf(w, "Install sidekick integration for %s (%s)? [Y/n] ", d.Agent, strings.Join(detail, "+"))
		var resp string
		_, _ = fmt.Fscanln(r, &resp)
		resp = strings.ToLower(strings.TrimSpace(resp))
		if resp == "" || resp == "y" || resp == "yes" {
			out = append(out, d.Agent)
		}
	}
	return out
}
