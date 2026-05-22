package cmd

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/meloniteai/sidekick/internal/config"
	"github.com/meloniteai/sidekick/internal/verifier"
)

// stdinIsTerminal returns true when the cobra command's stdin is a real
// terminal — false in tests (strings.Reader), pipes, and CI. The wizard
// uses this to decide between a fancy huh form and the scripted-friendly
// line-by-line fallback.
func stdinIsTerminal(in io.Reader) bool {
	f, ok := in.(*os.File)
	if !ok {
		return false
	}
	return isatty.IsTerminal(f.Fd())
}

// runLocalVerifierWizard adds a verifier to sidekick.yaml interactively. On a
// real terminal it shows a palette-styled full-window wizard (see
// verifier_local_wizard.go) so the experience matches the in-Sidekick ctrl+p
// command palette; otherwise (tests, pipes) it falls back to a
// scripted-friendly line-by-line wizard so scripted-stdin tests keep
// working unchanged.
func runLocalVerifierWizard(cmd *cobra.Command, configPath, nameFlag, directionFlag, kindFlag, permissionsFlag string, yes bool) error {
	if stdinIsTerminal(cmd.InOrStdin()) {
		return runLocalVerifierWizardPalette(cmd, configPath, nameFlag, directionFlag, kindFlag, permissionsFlag, yes)
	}
	return runLocalVerifierWizardText(cmd, configPath, nameFlag, directionFlag, kindFlag, permissionsFlag, yes)
}

// runLocalVerifierWizardText is the original scripted-stdin wizard. Kept
// as the non-TTY fallback so tests that pipe answers in via strings.Reader
// (see verifier_local_test.go) continue to drive the flow line-by-line.
func runLocalVerifierWizardText(cmd *cobra.Command, configPath, nameFlag, directionFlag, kindFlag, permissionsFlag string, yes bool) error {
	f, path, loaded, err := loadOrInit(configPath)
	if err != nil {
		return err
	}

	in := bufio.NewReader(cmd.InOrStdin())
	out := cmd.OutOrStdout()

	fmt.Fprintln(out, "Creating a new local verifier. Press enter to accept the default in brackets.")
	fmt.Fprintln(out)

	name, err := promptName(in, out, nameFlag, f, path)
	if err != nil {
		return err
	}

	direction, err := promptDirection(in, out, directionFlag, f.Verifiers)
	if err != nil {
		return err
	}

	kind, err := promptKind(in, out, kindFlag)
	if err != nil {
		return err
	}

	spec := config.VerifierSpec{
		Name:      name,
		Direction: direction,
		Type:      kind,
	}

	if err := promptTypeConfig(in, out, &spec); err != nil {
		return err
	}

	timeout, err := promptOptional(in, out, "Timeout (e.g. 60s; blank for default)", "", func(s string) error {
		if s == "" {
			return nil
		}
		if _, err := time.ParseDuration(s); err != nil {
			return fmt.Errorf("bad duration: %w", err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	spec.Timeout = timeout

	if permissionsFlag != "" {
		if err := applyPermissionsFlag(&spec, permissionsFlag); err != nil {
			return err
		}
	} else if want, err := promptYesNo(in, out, "Configure advisory permissions?", false); err != nil {
		return err
	} else if want {
		if err := promptPermissions(in, out, &spec); err != nil {
			return err
		}
	}

	raw, err := yaml.Marshal(spec)
	if err != nil {
		return fmt.Errorf("marshal preview: %w", err)
	}
	fmt.Fprintf(out, "\n--- preview ---\n%s--- end preview ---\n\n", raw)

	if !yes {
		if !confirmYN(in, out, fmt.Sprintf("Add this verifier to %s?", path)) {
			return errors.New("aborted")
		}
	}

	next := *f
	next.Verifiers = append(append([]config.VerifierSpec(nil), f.Verifiers...), spec)
	if err := next.ValidateStructural(); err != nil {
		return err
	}
	if err := config.Save(path, &next); err != nil {
		return fmt.Errorf("save %s: %w", path, err)
	}

	if loaded {
		fmt.Fprintf(out, "Added %q to %s.\n", spec.Name, path)
	} else {
		fmt.Fprintf(out, "Wrote %s with %q.\n", path, spec.Name)
	}
	warnMissingArtefacts(out, filepath.Dir(path), spec)
	fmt.Fprintln(out, "Restart `sidekick start` to pick up the new verifier.")
	return nil
}

func promptName(in *bufio.Reader, out io.Writer, flagVal string, f *config.File, path string) (string, error) {
	def := strings.TrimSpace(flagVal)
	if def == "" {
		def = nextLocalVerifierName(f.Verifiers)
	}
	return promptString(in, out, "Name", def, func(s string) error {
		if strings.TrimSpace(s) == "" {
			return errors.New("name is required")
		}
		if hasVerifier(f, s) {
			return fmt.Errorf("verifier %q already exists in %s", s, path)
		}
		return nil
	})
}

func promptDirection(in *bufio.Reader, out io.Writer, flagVal string, vs []config.VerifierSpec) (string, error) {
	def := strings.ToUpper(strings.TrimSpace(flagVal))
	if def == "" {
		def = nextLocalVerifierDirection(vs)
	}
	v, err := promptString(in, out, "Direction (N/NE/E/SE/S/SW/W/NW)", def, func(s string) error {
		if !isLocalDirection(strings.ToUpper(s)) {
			return errors.New("must be one of N/NE/E/SE/S/SW/W/NW")
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return strings.ToUpper(v), nil
}

var localKindChoices = []kindChoice{
	{value: verifier.TypeAgent, label: "agent", summary: "run a configured agent against a SKILL.md rubric"},
	{value: verifier.TypeCommand, label: "command", summary: "read session JSON on stdin, print distance/reason JSON"},
	{value: verifier.TypeBinary, label: "binary", summary: "map command exit status to pass/fail distance"},
}

type kindChoice struct {
	value   string
	label   string
	summary string
}

func promptKind(in *bufio.Reader, out io.Writer, flagVal string) (string, error) {
	def := strings.ToLower(strings.TrimSpace(flagVal))
	if def == "llm" {
		def = verifier.TypeAgent
	}
	if def == "" {
		def = verifier.TypeAgent
	}
	return promptChoice(in, out, "Type", localKindChoices, def)
}

func promptTypeConfig(in *bufio.Reader, out io.Writer, spec *config.VerifierSpec) error {
	slug := slugifyName(spec.Name)
	switch spec.Type {
	case verifier.TypeCommand:
		cmdStr, err := promptString(in, out, "Command (space-separated)", "./verifiers/"+slug+".sh", func(s string) error {
			if len(splitCommandFields(s)) == 0 {
				return errors.New("command is required")
			}
			return nil
		})
		if err != nil {
			return err
		}
		spec.Command = splitCommandFields(cmdStr)
	case verifier.TypeAgent:
		agentName, err := promptString(in, out, "Agent (claude/codex/custom)", "claude", func(s string) error {
			switch strings.ToLower(s) {
			case "claude", "codex", "custom":
				return nil
			}
			return errors.New("must be claude, codex, or custom")
		})
		if err != nil {
			return err
		}
		model, err := promptOptional(in, out, "Model (optional)", "", nil)
		if err != nil {
			return err
		}
		thinking, err := promptOptional(in, out, "Thinking effort (optional)", "", nil)
		if err != nil {
			return err
		}
		skill, err := promptString(in, out, "Skill path", "./skills/"+slug+"/SKILL.md", func(s string) error {
			if strings.TrimSpace(s) == "" {
				return errors.New("skill path is required")
			}
			return nil
		})
		if err != nil {
			return err
		}
		spec.LLM = config.AgentVerifierSpec{
			Agent:    strings.ToLower(agentName),
			Model:    model,
			Thinking: thinking,
			Skill:    skill,
		}
		if strings.EqualFold(agentName, "custom") {
			customStr, err := promptString(in, out, "Custom agent command (space-separated)", "", func(s string) error {
				if len(splitCommandFields(s)) == 0 {
					return errors.New("custom agent command is required")
				}
				return nil
			})
			if err != nil {
				return err
			}
			spec.LLM.Custom = &config.CustomAgentSpec{Command: splitCommandFields(customStr)}
		}
	case verifier.TypeBinary:
		cmdStr, err := promptString(in, out, "Command (space-separated, e.g. go test ./...)", "", func(s string) error {
			if len(splitCommandFields(s)) == 0 {
				return errors.New("command is required")
			}
			return nil
		})
		if err != nil {
			return err
		}
		passReason, err := promptOptional(in, out, "Pass reason (optional)", "", nil)
		if err != nil {
			return err
		}
		failReason, err := promptOptional(in, out, "Fail reason (optional)", "", nil)
		if err != nil {
			return err
		}
		spec.Binary = config.BinaryVerifierSpec{
			Command:    splitCommandFields(cmdStr),
			PassReason: passReason,
			FailReason: failReason,
		}
	default:
		return fmt.Errorf("unknown verifier type %q", spec.Type)
	}
	return nil
}

func promptPermissions(in *bufio.Reader, out io.Writer, spec *config.VerifierSpec) error {
	network, err := promptYesNo(in, out, "Allow network?", false)
	if err != nil {
		return err
	}
	fs, err := promptString(in, out, "Filesystem (read-only/read-write/none)", "read-only", func(s string) error {
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "", "read-only", "read-write", "none":
			return nil
		}
		return errors.New("must be read-only, read-write, or none")
	})
	if err != nil {
		return err
	}
	envStr, err := promptOptional(in, out, "Env vars (colon-separated, blank for none)", "", nil)
	if err != nil {
		return err
	}
	p := &config.PermissionsSpec{
		Network:    network,
		Filesystem: strings.ToLower(strings.TrimSpace(fs)),
	}
	for _, e := range strings.Split(envStr, ":") {
		if e = strings.TrimSpace(e); e != "" {
			p.Env = append(p.Env, e)
		}
	}
	spec.Permissions = p
	return nil
}

func promptString(in *bufio.Reader, out io.Writer, label, def string, validate func(string) error) (string, error) {
	for {
		if def != "" {
			fmt.Fprintf(out, "%s [%s]: ", label, def)
		} else {
			fmt.Fprintf(out, "%s: ", label)
		}
		line, err := in.ReadString('\n')
		if err != nil && line == "" {
			return "", err
		}
		v := strings.TrimRight(line, "\r\n")
		if v == "" {
			v = def
		}
		if validate != nil {
			if verr := validate(v); verr != nil {
				fmt.Fprintf(out, "  ! %v\n", verr)
				continue
			}
		}
		return v, nil
	}
}

// promptOptional differs from promptString in that an empty answer is always
// accepted (no required check), even when there's no default.
func promptOptional(in *bufio.Reader, out io.Writer, label, def string, validate func(string) error) (string, error) {
	if def != "" {
		fmt.Fprintf(out, "%s [%s]: ", label, def)
	} else {
		fmt.Fprintf(out, "%s: ", label)
	}
	line, err := in.ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	v := strings.TrimRight(line, "\r\n")
	if v == "" {
		v = def
	}
	if validate != nil {
		if verr := validate(v); verr != nil {
			return "", verr
		}
	}
	return v, nil
}

func promptYesNo(in *bufio.Reader, out io.Writer, label string, def bool) (bool, error) {
	hint := "y/N"
	if def {
		hint = "Y/n"
	}
	for {
		fmt.Fprintf(out, "%s [%s]: ", label, hint)
		line, err := in.ReadString('\n')
		if err != nil && line == "" {
			return false, err
		}
		v := strings.ToLower(strings.TrimSpace(line))
		if v == "" {
			return def, nil
		}
		switch v {
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		}
		fmt.Fprintln(out, "  ! answer y or n")
	}
}

func promptChoice(in *bufio.Reader, out io.Writer, label string, choices []kindChoice, def string) (string, error) {
	defIdx := 0
	for i, c := range choices {
		if c.value == def {
			defIdx = i
			break
		}
	}
	for {
		fmt.Fprintf(out, "%s:\n", label)
		for i, c := range choices {
			marker := " "
			if i == defIdx {
				marker = "*"
			}
			fmt.Fprintf(out, "  %s %d) %-8s %s\n", marker, i+1, c.label, c.summary)
		}
		fmt.Fprintf(out, "Choose [%d]: ", defIdx+1)
		line, err := in.ReadString('\n')
		if err != nil && line == "" {
			return "", err
		}
		v := strings.TrimSpace(line)
		if v == "" {
			return choices[defIdx].value, nil
		}
		for i, c := range choices {
			if v == fmt.Sprintf("%d", i+1) || strings.EqualFold(v, c.label) || strings.EqualFold(v, c.value) {
				return c.value, nil
			}
		}
		fmt.Fprintln(out, "  ! pick by number or label")
	}
}

// confirmYN is a y/N prompt that reads from a buffered reader instead of
// raw os.File like the older `confirm` helper. Kept separate so the local
// wizard stays drivable with a scripted strings.Reader in tests.
func confirmYN(in *bufio.Reader, out io.Writer, prompt string) bool {
	fmt.Fprintf(out, "%s [y/N] ", prompt)
	line, _ := in.ReadString('\n')
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes"
}

func splitCommandFields(s string) []string {
	return strings.Fields(s)
}

// warnMissingArtefacts surfaces a heads-up when the local script or skill
// the user just registered doesn't exist on disk. `sidekick start` will fail
// with the same error later — catching it here is a UX nicety, not a
// correctness check.
func warnMissingArtefacts(out io.Writer, configDir string, spec config.VerifierSpec) {
	switch spec.Type {
	case verifier.TypeAgent:
		if spec.LLM.Skill == "" {
			return
		}
		path := config.ResolveLocalPath(configDir, spec.LLM.Skill)
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(out, "Note: skill file %s does not exist yet — create it before running `sidekick start`.\n", path)
		}
	case verifier.TypeCommand:
		if len(spec.Command) == 0 {
			return
		}
		warnLocalCommand(out, configDir, spec.Command[0])
	case verifier.TypeBinary:
		if len(spec.Binary.Command) == 0 {
			return
		}
		warnLocalCommand(out, configDir, spec.Binary.Command[0])
	}
}

func warnLocalCommand(out io.Writer, configDir, raw string) {
	if !looksLikeLocalScriptPath(raw) {
		return
	}
	path := config.ResolveLocalPath(configDir, raw)
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(out, "Note: script %s does not exist yet — create it before running `sidekick start`.\n", path)
	}
}

func looksLikeLocalScriptPath(p string) bool {
	clean := filepath.Clean(p)
	switch {
	case strings.HasPrefix(p, "./"), strings.HasPrefix(p, "../"), strings.HasPrefix(p, "~/"), strings.HasPrefix(p, "/"), clean == ".sidekick", strings.HasPrefix(clean, ".sidekick"+string(os.PathSeparator)):
		return true
	}
	return false
}

func nextLocalVerifierName(vs []config.VerifierSpec) string {
	seen := map[string]bool{}
	for _, v := range vs {
		seen[v.Name] = true
	}
	base := "NewVerifier"
	if !seen[base] {
		return base
	}
	for i := 2; ; i++ {
		n := fmt.Sprintf("%s%d", base, i)
		if !seen[n] {
			return n
		}
	}
}

func nextLocalVerifierDirection(vs []config.VerifierSpec) string {
	used := map[string]bool{}
	for _, v := range vs {
		used[strings.ToUpper(v.Direction)] = true
	}
	for _, d := range []string{"N", "NE", "E", "SE", "S", "SW", "W", "NW"} {
		if !used[d] {
			return d
		}
	}
	return "NE"
}

func isLocalDirection(d string) bool {
	switch d {
	case "N", "NE", "E", "SE", "S", "SW", "W", "NW":
		return true
	}
	return false
}

func slugifyName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	dashed := false
	for _, r := range name {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			dashed = false
			continue
		}
		if b.Len() > 0 && !dashed {
			b.WriteByte('-')
			dashed = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "verifier"
	}
	return out
}
