package cmd

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/uriahlevy/hud/internal/config"
	hudtui "github.com/uriahlevy/hud/internal/hud"
	"github.com/uriahlevy/hud/internal/verifier"
)

// runLocalVerifierWizardPalette runs the TTY-only `hud verifier add
// --local` flow inside a full-screen bubbletea program styled to match
// the in-HUD ctrl+p command palette. It is intentionally a separate
// program from the ctrl+n "New Verifier" wizard embedded in the HUD
// (internal/hud.EditWizard); the two share styling primitives but no
// wizard code.
func runLocalVerifierWizardPalette(cmd *cobra.Command, configPath, nameFlag, directionFlag, kindFlag, permissionsFlag string, yes bool) error {
	f, path, loaded, err := loadOrInit(configPath)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()

	m := newLocalWizardModel(f, path, nameFlag, directionFlag, kindFlag, permissionsFlag, yes)
	if m.completed {
		// No visible steps — shouldn't normally happen, but bail safely.
		return errors.New("nothing to ask")
	}

	prog := tea.NewProgram(m, tea.WithAltScreen(), tea.WithInput(cmd.InOrStdin()), tea.WithOutput(out))
	finalModel, err := prog.Run()
	if err != nil {
		return err
	}
	final, ok := finalModel.(*localWizardModel)
	if !ok {
		return errors.New("wizard exited unexpectedly")
	}
	if final.aborted {
		return errors.New("aborted")
	}

	spec := final.toSpec()
	if permissionsFlag != "" {
		if err := applyPermissionsFlag(&spec, permissionsFlag); err != nil {
			return err
		}
	}

	raw, err := yaml.Marshal(spec)
	if err != nil {
		return fmt.Errorf("marshal preview: %w", err)
	}
	fmt.Fprintf(out, "\n--- preview ---\n%s--- end preview ---\n\n", raw)

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
	fmt.Fprintln(out, "Restart `hud start` to pick up the new verifier.")
	return nil
}

// localWizardStepKind discriminates the per-step renderer in
// localWizardModel.View. Each step is either a single text input or a
// vertical choice list — the same two primitives the in-HUD palette
// composes.
type localWizardStepKind int

const (
	stepKindText localWizardStepKind = iota
	stepKindChoice
)

type localWizardChoice struct {
	label   string
	summary string
	value   string
}

// localWizardStep describes one logical question. The advance/back
// machinery walks a static slice of these, skipping any whose show()
// predicate is false (e.g. binary-only steps when type=agent).
type localWizardStep struct {
	id    string
	kind  localWizardStepKind
	title string
	// desc is rendered above the input; may be multi-line.
	desc func(*localWizardModel) string
	// show controls visibility — false steps are skipped both forward
	// and backward, so the step counter and prev/next stay coherent.
	show func(*localWizardModel) bool

	// Text-step fields.
	placeholder  string
	optional     bool
	defaultValue func(*localWizardModel) string
	getText      func(*localWizardModel) string
	setText      func(*localWizardModel, string)
	validate     func(*localWizardModel, string) error

	// Choice-step fields.
	options   func(*localWizardModel) []localWizardChoice
	getChoice func(*localWizardModel) string
	setChoice func(*localWizardModel, string)
}

// localWizardModel is the bubbletea program backing the palette-styled
// wizard. All in-flight answers live as plain fields; toSpec() folds
// them into a config.VerifierSpec once the user confirms the preview.
type localWizardModel struct {
	file    *config.File
	path    string
	yesFlag bool
	permFlag string

	name      string
	direction string
	kind      string

	agentName string
	model     string
	thinking  string
	skill     string
	customCmd string

	commandStr string

	binaryCmd  string
	passReason string
	failReason string

	timeoutStr string

	configurePerms string // "yes" / "no"
	network        string // "yes" / "no"
	fsMode         string
	envStr         string

	confirmChoice string // "yes" / "no"
	previewYAML   string

	steps    []localWizardStep
	stepIdx  int
	ti       textinput.Model
	cursor   int
	errMsg   string
	width    int
	height   int

	aborted   bool
	completed bool
}

func newLocalWizardModel(f *config.File, path, nameFlag, directionFlag, kindFlag, permissionsFlag string, yes bool) *localWizardModel {
	name := strings.TrimSpace(nameFlag)
	if name == "" {
		name = nextLocalVerifierName(f.Verifiers)
	}
	direction := strings.ToUpper(strings.TrimSpace(directionFlag))
	if direction == "" {
		direction = nextLocalVerifierDirection(f.Verifiers)
	}
	kind := strings.ToLower(strings.TrimSpace(kindFlag))
	if kind == "llm" {
		kind = verifier.TypeAgent
	}
	if kind == "" {
		kind = verifier.TypeAgent
	}

	m := &localWizardModel{
		file:           f,
		path:           path,
		yesFlag:        yes,
		permFlag:       permissionsFlag,
		name:           name,
		direction:      direction,
		kind:           kind,
		agentName:      "claude",
		fsMode:         "read-only",
		configurePerms: "no",
		network:        "no",
		confirmChoice:  "yes",
	}
	m.steps = buildLocalWizardSteps()
	m.stepIdx = -1
	if !m.advance() {
		m.completed = true
	}
	return m
}

func (m *localWizardModel) Init() tea.Cmd { return textinput.Blink }

func (m *localWizardModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.resizeInput()
		return m, nil
	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m *localWizardModel) handleKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "ctrl+c", "esc":
		m.aborted = true
		return m, tea.Quit
	case "shift+tab":
		m.goPrev()
		return m, nil
	}
	step := m.currentStep()
	switch step.kind {
	case stepKindText:
		return m.updateText(key, step)
	case stepKindChoice:
		return m.updateChoice(key, step)
	}
	return m, nil
}

func (m *localWizardModel) updateText(key tea.KeyMsg, step localWizardStep) (tea.Model, tea.Cmd) {
	if key.String() == "enter" {
		v := strings.TrimRight(m.ti.Value(), "\r\n")
		if v == "" && step.optional {
			step.setText(m, "")
			m.errMsg = ""
			return m.advanceOrFinish()
		}
		if step.validate != nil {
			if err := step.validate(m, v); err != nil {
				m.errMsg = err.Error()
				return m, nil
			}
		}
		step.setText(m, v)
		m.errMsg = ""
		return m.advanceOrFinish()
	}
	var cmd tea.Cmd
	m.ti, cmd = m.ti.Update(key)
	return m, cmd
}

func (m *localWizardModel) updateChoice(key tea.KeyMsg, step localWizardStep) (tea.Model, tea.Cmd) {
	opts := step.options(m)
	switch key.String() {
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
		return m, nil
	case "down", "tab", "j":
		if m.cursor < len(opts)-1 {
			m.cursor++
		}
		return m, nil
	case "enter":
		if m.cursor < 0 || m.cursor >= len(opts) {
			return m, nil
		}
		step.setChoice(m, opts[m.cursor].value)
		m.errMsg = ""
		return m.advanceOrFinish()
	}
	// Number shortcuts: 1..9 select the matching option directly. Mirrors
	// the muscle memory the text wizard's promptChoice already gives.
	if r := key.String(); len(r) == 1 && r[0] >= '1' && r[0] <= '9' {
		idx := int(r[0] - '1')
		if idx < len(opts) {
			step.setChoice(m, opts[idx].value)
			m.errMsg = ""
			return m.advanceOrFinish()
		}
	}
	return m, nil
}

func (m *localWizardModel) advanceOrFinish() (tea.Model, tea.Cmd) {
	if m.advance() {
		return m, nil
	}
	if m.aborted {
		return m, tea.Quit
	}
	m.completed = true
	return m, tea.Quit
}

// advance moves to the next visible step. Returns false when no more
// visible steps remain. The "user picked Cancel on the confirm step"
// case is handled inline — confirmChoice=="no" sets aborted=true and
// the caller short-circuits to tea.Quit via advanceOrFinish.
func (m *localWizardModel) advance() bool {
	// If the most recent step was the confirm step and the user picked
	// "no", treat that as abort.
	if m.stepIdx >= 0 && m.steps[m.stepIdx].id == "confirm" && m.confirmChoice == "no" {
		m.aborted = true
		return false
	}
	for i := m.stepIdx + 1; i < len(m.steps); i++ {
		if m.steps[i].show(m) {
			m.stepIdx = i
			m.prepareStep()
			return true
		}
	}
	return false
}

func (m *localWizardModel) goPrev() {
	for i := m.stepIdx - 1; i >= 0; i-- {
		if m.steps[i].show(m) {
			m.stepIdx = i
			m.prepareStep()
			return
		}
	}
}

func (m *localWizardModel) currentStep() localWizardStep { return m.steps[m.stepIdx] }

func (m *localWizardModel) prepareStep() {
	step := m.currentStep()
	m.errMsg = ""
	switch step.kind {
	case stepKindText:
		cur := step.getText(m)
		if cur == "" && step.defaultValue != nil {
			cur = step.defaultValue(m)
		}
		m.ti = newPaletteInput(step.placeholder, m.inputWidth())
		m.ti.SetValue(cur)
		m.ti.CursorEnd()
	case stepKindChoice:
		opts := step.options(m)
		current := step.getChoice(m)
		m.cursor = 0
		for i, o := range opts {
			if o.value == current {
				m.cursor = i
				break
			}
		}
	}
	if step.id == "confirm" {
		m.previewYAML = m.renderPreview()
	}
}

func (m *localWizardModel) resizeInput() {
	if m.stepIdx < 0 || m.stepIdx >= len(m.steps) {
		return
	}
	if m.steps[m.stepIdx].kind == stepKindText {
		m.ti.Width = m.inputWidth()
	}
}

func (m *localWizardModel) inputWidth() int {
	// Leave room for the "> " prompt and a couple of trailing cells so a
	// long value doesn't crash into the modal's right edge.
	w := localWizardInnerWidth(m.width) - 4
	if w < 16 {
		w = 16
	}
	return w
}

// localWizardInnerWidth picks the modal's inner content width. It uses
// the same shape as the in-HUD palette helper (~70% of the terminal) but
// raises the cap from 72 to 92 cells — the wizard renders longer
// description sentences than the palette's one-word command labels, so
// truncating at 72 makes the type/permissions step copy unreadable.
func localWizardInnerWidth(termWidth int) int {
	if termWidth <= 0 {
		return 64
	}
	target := termWidth * 7 / 10
	if target < 60 {
		target = 60
	}
	if target > 92 {
		target = 92
	}
	chrome := stylePaletteBorder.GetHorizontalFrameSize()
	w := target - chrome
	if w < 28 {
		w = 28
	}
	return w
}

// wordWrap splits s into lines no wider than w. Pre-existing newlines
// are honored as hard breaks; each logical line is wrapped on word
// boundaries. Long single tokens that exceed w are emitted on their
// own line rather than hard-cut, since wrapping mid-path or mid-flag
// would mislead the user about what they typed.
func wordWrap(s string, w int) []string {
	if w <= 0 {
		return []string{s}
	}
	var out []string
	for _, raw := range strings.Split(s, "\n") {
		if raw == "" {
			out = append(out, "")
			continue
		}
		// Preserve leading whitespace (matters for YAML indentation in the
		// confirm-step preview).
		leading := 0
		for _, r := range raw {
			if r == ' ' || r == '\t' {
				leading++
			} else {
				break
			}
		}
		indent := raw[:leading]
		body := raw[leading:]
		if lipgloss.Width(raw) <= w {
			out = append(out, raw)
			continue
		}
		words := strings.Fields(body)
		if len(words) == 0 {
			out = append(out, raw)
			continue
		}
		cur := indent + words[0]
		for _, word := range words[1:] {
			if lipgloss.Width(cur)+1+lipgloss.Width(word) <= w {
				cur += " " + word
			} else {
				out = append(out, cur)
				cur = indent + word
			}
		}
		out = append(out, cur)
	}
	return out
}

func newPaletteInput(placeholder string, width int) textinput.Model {
	ti := textinput.New()
	ti.Placeholder = placeholder
	ti.Prompt = ""
	ti.CharLimit = 512
	ti.Width = width
	ti.Focus()
	ti.PromptStyle = stylePalettePrompt
	ti.PlaceholderStyle = stylePalettePlaceholder
	ti.TextStyle = stylePaletteBody
	ti.Cursor.Style = lipgloss.NewStyle().Background(lipgloss.Color(hudtui.BrandBg))
	return ti
}

// toSpec assembles the final VerifierSpec from the wizard's draft
// fields. Permissions are applied here only when permissionsFlag is
// empty and the user explicitly opted into configuring them; otherwise
// the caller folds in the flag value via applyPermissionsFlag.
func (m *localWizardModel) toSpec() config.VerifierSpec {
	spec := config.VerifierSpec{
		Name:      strings.TrimSpace(m.name),
		Direction: strings.ToUpper(m.direction),
		Type:      m.kind,
	}
	switch m.kind {
	case verifier.TypeCommand:
		spec.Command = splitCommandFields(m.commandStr)
	case verifier.TypeAgent:
		spec.LLM = config.AgentVerifierSpec{
			Agent:    strings.ToLower(m.agentName),
			Model:    m.model,
			Thinking: m.thinking,
			Skill:    m.skill,
		}
		if strings.EqualFold(m.agentName, "custom") {
			spec.LLM.Custom = &config.CustomAgentSpec{Command: splitCommandFields(m.customCmd)}
		}
	case verifier.TypeBinary:
		spec.Binary = config.BinaryVerifierSpec{
			Command:    splitCommandFields(m.binaryCmd),
			PassReason: m.passReason,
			FailReason: m.failReason,
		}
	}
	spec.Timeout = strings.TrimSpace(m.timeoutStr)
	if m.permFlag == "" && m.configurePerms == "yes" {
		p := &config.PermissionsSpec{
			Network:    strings.EqualFold(m.network, "yes"),
			Filesystem: strings.ToLower(strings.TrimSpace(m.fsMode)),
		}
		for _, e := range strings.Split(m.envStr, ":") {
			if e = strings.TrimSpace(e); e != "" {
				p.Env = append(p.Env, e)
			}
		}
		spec.Permissions = p
	}
	return spec
}

func (m *localWizardModel) renderPreview() string {
	raw, err := yaml.Marshal(m.toSpec())
	if err != nil {
		return fmt.Sprintf("(preview unavailable: %v)", err)
	}
	return string(raw)
}

// buildLocalWizardSteps lays out every possible question in the
// wizard. show() predicates gate visibility — e.g. agent-only fields
// stay hidden when the user picked the binary type.
func buildLocalWizardSteps() []localWizardStep {
	yesNo := func(_ *localWizardModel) []localWizardChoice {
		return []localWizardChoice{
			{label: "yes", summary: "", value: "yes"},
			{label: "no", summary: "", value: "no"},
		}
	}
	always := func(_ *localWizardModel) bool { return true }
	whenAgent := func(m *localWizardModel) bool { return m.kind == verifier.TypeAgent }
	whenCommand := func(m *localWizardModel) bool { return m.kind == verifier.TypeCommand }
	whenBinary := func(m *localWizardModel) bool { return m.kind == verifier.TypeBinary }

	return []localWizardStep{
		{
			id: "name", kind: stepKindText, title: "Name",
			desc:        descConst("How this verifier is referenced everywhere else in hud."),
			show:        always,
			placeholder: "MyVerifier",
			getText:     func(m *localWizardModel) string { return m.name },
			setText:     func(m *localWizardModel, v string) { m.name = strings.TrimSpace(v) },
			validate: func(m *localWizardModel, s string) error {
				s = strings.TrimSpace(s)
				if s == "" {
					return errors.New("name is required")
				}
				if hasVerifier(m.file, s) {
					return fmt.Errorf("verifier %q already exists in %s", s, m.path)
				}
				return nil
			},
		},
		{
			id: "direction", kind: stepKindChoice, title: "Direction",
			desc: descConst("Which compass slot this verifier occupies."),
			show: always,
			options: func(_ *localWizardModel) []localWizardChoice {
				return []localWizardChoice{
					{label: "N", summary: "north", value: "N"},
					{label: "NE", summary: "northeast", value: "NE"},
					{label: "E", summary: "east", value: "E"},
					{label: "SE", summary: "southeast", value: "SE"},
					{label: "S", summary: "south", value: "S"},
					{label: "SW", summary: "southwest", value: "SW"},
					{label: "W", summary: "west", value: "W"},
					{label: "NW", summary: "northwest", value: "NW"},
				}
			},
			getChoice: func(m *localWizardModel) string { return m.direction },
			setChoice: func(m *localWizardModel, v string) { m.direction = v },
		},
		{
			id: "type", kind: stepKindChoice, title: "Type",
			desc: descConst("How the verifier produces its distance/reason."),
			show: always,
			options: func(_ *localWizardModel) []localWizardChoice {
				return []localWizardChoice{
					{label: "agent", summary: "run a configured agent against a SKILL.md rubric", value: verifier.TypeAgent},
					{label: "command", summary: "read session JSON on stdin, print distance/reason JSON", value: verifier.TypeCommand},
					{label: "binary", summary: "map command exit status to pass/fail distance", value: verifier.TypeBinary},
				}
			},
			getChoice: func(m *localWizardModel) string { return m.kind },
			setChoice: func(m *localWizardModel, v string) { m.kind = v },
		},
		{
			id: "agent-name", kind: stepKindChoice, title: "Agent",
			desc: descConst("Which agent runs the rubric."),
			show: whenAgent,
			options: func(_ *localWizardModel) []localWizardChoice {
				return []localWizardChoice{
					{label: "claude", summary: "Anthropic Claude CLI", value: "claude"},
					{label: "codex", summary: "OpenAI codex CLI", value: "codex"},
					{label: "custom", summary: "provide your own command", value: "custom"},
				}
			},
			getChoice: func(m *localWizardModel) string { return m.agentName },
			setChoice: func(m *localWizardModel, v string) { m.agentName = v },
		},
		{
			id: "agent-model", kind: stepKindText, title: "Model",
			desc:        descConst("Override the agent's default model (optional)."),
			show:        whenAgent,
			optional:    true,
			placeholder: "e.g. claude-sonnet-4-6",
			getText:     func(m *localWizardModel) string { return m.model },
			setText:     func(m *localWizardModel, v string) { m.model = v },
		},
		{
			id: "agent-thinking", kind: stepKindText, title: "Thinking effort",
			desc:        descConst("Optional thinking budget hint passed to the agent."),
			show:        whenAgent,
			optional:    true,
			placeholder: "e.g. high",
			getText:     func(m *localWizardModel) string { return m.thinking },
			setText:     func(m *localWizardModel, v string) { m.thinking = v },
		},
		{
			id: "agent-skill", kind: stepKindText, title: "Skill path",
			desc:         descConst("Path to the SKILL.md rubric the agent is judged against."),
			show:         whenAgent,
			placeholder:  "./skills/myverifier/SKILL.md",
			defaultValue: func(m *localWizardModel) string { return "./skills/" + slugifyName(m.name) + "/SKILL.md" },
			getText:      func(m *localWizardModel) string { return m.skill },
			setText:      func(m *localWizardModel, v string) { m.skill = v },
			validate: func(_ *localWizardModel, s string) error {
				if strings.TrimSpace(s) == "" {
					return errors.New("skill path is required")
				}
				return nil
			},
		},
		{
			id: "agent-custom-cmd", kind: stepKindText, title: "Custom agent command",
			desc:        descConst("Space-separated command that invokes your custom agent."),
			show:        func(m *localWizardModel) bool { return whenAgent(m) && strings.EqualFold(m.agentName, "custom") },
			placeholder: "e.g. ./bin/my-agent --json",
			getText:     func(m *localWizardModel) string { return m.customCmd },
			setText:     func(m *localWizardModel, v string) { m.customCmd = v },
			validate: func(_ *localWizardModel, s string) error {
				if len(splitCommandFields(s)) == 0 {
					return errors.New("custom agent command is required")
				}
				return nil
			},
		},
		{
			id: "command", kind: stepKindText, title: "Command",
			desc:         descConst("Script that reads session JSON on stdin and prints distance/reason JSON."),
			show:         whenCommand,
			placeholder:  "./verifiers/myverifier.sh",
			defaultValue: func(m *localWizardModel) string { return "./verifiers/" + slugifyName(m.name) + ".sh" },
			getText:      func(m *localWizardModel) string { return m.commandStr },
			setText:      func(m *localWizardModel, v string) { m.commandStr = v },
			validate: func(_ *localWizardModel, s string) error {
				if len(splitCommandFields(s)) == 0 {
					return errors.New("command is required")
				}
				return nil
			},
		},
		{
			id: "binary-cmd", kind: stepKindText, title: "Binary command",
			desc:        descConst("Command whose exit status maps to pass (0) or fail (non-zero)."),
			show:        whenBinary,
			placeholder: "e.g. go test ./...",
			getText:     func(m *localWizardModel) string { return m.binaryCmd },
			setText:     func(m *localWizardModel, v string) { m.binaryCmd = v },
			validate: func(_ *localWizardModel, s string) error {
				if len(splitCommandFields(s)) == 0 {
					return errors.New("command is required")
				}
				return nil
			},
		},
		{
			id: "binary-pass", kind: stepKindText, title: "Pass reason",
			desc:        descConst("Short message shown on the compass when the command passes (optional)."),
			show:        whenBinary,
			optional:    true,
			placeholder: "checks passed",
			getText:     func(m *localWizardModel) string { return m.passReason },
			setText:     func(m *localWizardModel, v string) { m.passReason = v },
		},
		{
			id: "binary-fail", kind: stepKindText, title: "Fail reason",
			desc:        descConst("Short message shown on the compass when the command fails (optional)."),
			show:        whenBinary,
			optional:    true,
			placeholder: "checks failed",
			getText:     func(m *localWizardModel) string { return m.failReason },
			setText:     func(m *localWizardModel, v string) { m.failReason = v },
		},
		{
			id: "timeout", kind: stepKindText, title: "Timeout",
			desc:        descConst("Maximum time per run, e.g. 60s. Leave blank for the hud default."),
			show:        always,
			optional:    true,
			placeholder: "e.g. 60s",
			getText:     func(m *localWizardModel) string { return m.timeoutStr },
			setText:     func(m *localWizardModel, v string) { m.timeoutStr = v },
			validate: func(_ *localWizardModel, s string) error {
				s = strings.TrimSpace(s)
				if s == "" {
					return nil
				}
				if _, err := time.ParseDuration(s); err != nil {
					return fmt.Errorf("bad duration: %w", err)
				}
				return nil
			},
		},
		{
			id: "perms-configure", kind: stepKindChoice, title: "Permissions",
			desc:      descConst("Configure advisory sandboxing hints for downstream runners?"),
			show:      func(m *localWizardModel) bool { return m.permFlag == "" },
			options:   yesNo,
			getChoice: func(m *localWizardModel) string { return m.configurePerms },
			setChoice: func(m *localWizardModel, v string) { m.configurePerms = v },
		},
		{
			id: "perms-network", kind: stepKindChoice, title: "Allow network?",
			desc:      descConst("Whether the verifier may reach the network."),
			show:      func(m *localWizardModel) bool { return m.permFlag == "" && m.configurePerms == "yes" },
			options:   yesNo,
			getChoice: func(m *localWizardModel) string { return m.network },
			setChoice: func(m *localWizardModel, v string) { m.network = v },
		},
		{
			id: "perms-fs", kind: stepKindChoice, title: "Filesystem",
			desc: descConst("How much filesystem access the verifier should have."),
			show: func(m *localWizardModel) bool { return m.permFlag == "" && m.configurePerms == "yes" },
			options: func(_ *localWizardModel) []localWizardChoice {
				return []localWizardChoice{
					{label: "read-only", summary: "default — verifiers shouldn't mutate the workspace", value: "read-only"},
					{label: "read-write", summary: "verifier writes back to the workspace", value: "read-write"},
					{label: "none", summary: "no filesystem access at all", value: "none"},
				}
			},
			getChoice: func(m *localWizardModel) string { return m.fsMode },
			setChoice: func(m *localWizardModel, v string) { m.fsMode = v },
		},
		{
			id: "perms-env", kind: stepKindText, title: "Env vars",
			desc:        descConst("Colon-separated env var names to pass through. Leave blank for none."),
			show:        func(m *localWizardModel) bool { return m.permFlag == "" && m.configurePerms == "yes" },
			optional:    true,
			placeholder: "PATH:GOPATH",
			getText:     func(m *localWizardModel) string { return m.envStr },
			setText:     func(m *localWizardModel, v string) { m.envStr = v },
		},
		{
			id: "confirm", kind: stepKindChoice, title: "Confirm",
			desc: func(m *localWizardModel) string {
				return "Add this verifier to " + m.path + "?\n\n" + m.previewYAML
			},
			show: func(m *localWizardModel) bool { return !m.yesFlag },
			options: func(_ *localWizardModel) []localWizardChoice {
				return []localWizardChoice{
					{label: "Add", summary: "write the entry and exit", value: "yes"},
					{label: "Cancel", summary: "discard and exit", value: "no"},
				}
			},
			getChoice: func(m *localWizardModel) string { return m.confirmChoice },
			setChoice: func(m *localWizardModel, v string) { m.confirmChoice = v },
		},
	}
}

func descConst(s string) func(*localWizardModel) string {
	return func(*localWizardModel) string { return s }
}

// ----- styles -----

// All palette-styled surfaces explicitly set Background to BrandBg so
// the embedded \x1b[0m resets don't punch terminal black through the
// modal — same trick the in-HUD palette uses, see palette.go's
// long-form note for why.
var (
	stylePaletteBorder      = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color(hudtui.BrandCoral)).BorderBackground(lipgloss.Color(hudtui.BrandBg)).Background(lipgloss.Color(hudtui.BrandBg)).Padding(1, 2)
	stylePaletteTitle       = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(hudtui.BrandCoralSoft)).Background(lipgloss.Color(hudtui.BrandBg))
	stylePaletteSlash       = lipgloss.NewStyle().Foreground(lipgloss.Color(hudtui.BrandCoral)).Background(lipgloss.Color(hudtui.BrandBg))
	stylePalettePrompt      = lipgloss.NewStyle().Foreground(lipgloss.Color(hudtui.BrandCoralSoft)).Background(lipgloss.Color(hudtui.BrandBg))
	stylePalettePlaceholder = lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Background(lipgloss.Color(hudtui.BrandBg))
	stylePaletteSelected    = lipgloss.NewStyle().Foreground(lipgloss.Color("231")).Background(lipgloss.Color(hudtui.BrandCoral)).Bold(true)
	stylePaletteHelp        = lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Background(lipgloss.Color(hudtui.BrandBg))
	stylePaletteSeparator   = lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Background(lipgloss.Color(hudtui.BrandBg))
	stylePaletteBody        = lipgloss.NewStyle().Foreground(lipgloss.Color("252")).Background(lipgloss.Color(hudtui.BrandBg))
	stylePaletteDesc        = lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Background(lipgloss.Color(hudtui.BrandBg))
	stylePaletteError       = lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Background(lipgloss.Color(hudtui.BrandBg)).Bold(true)
	stylePalettePreview     = lipgloss.NewStyle().Foreground(lipgloss.Color("251")).Background(lipgloss.Color(hudtui.BrandBg))
)

// ----- view -----

func (m *localWizardModel) View() string {
	if m.stepIdx < 0 || m.stepIdx >= len(m.steps) {
		return ""
	}
	innerW := localWizardInnerWidth(m.width)
	step := m.currentStep()

	var b strings.Builder
	b.WriteString(m.renderTitleRow(innerW, step))
	b.WriteString("\n\n")

	if step.desc != nil {
		raw := step.desc(m)
		// Confirm step: first paragraph is the "Add this verifier to …?"
		// prompt (prose, dim grey); everything after the first blank line
		// is the YAML preview (content, brighter). Other steps render the
		// whole description as dim helper text.
		descPart, previewPart := raw, ""
		if step.id == "confirm" {
			if idx := strings.Index(raw, "\n\n"); idx >= 0 {
				descPart = raw[:idx]
				previewPart = raw[idx+2:]
			}
		}
		for _, ln := range wordWrap(descPart, innerW) {
			b.WriteString(stylePaletteDesc.Render(ln))
			b.WriteString("\n")
		}
		if previewPart != "" {
			b.WriteString("\n")
			for _, ln := range wordWrap(previewPart, innerW) {
				b.WriteString(stylePalettePreview.Render(ln))
				b.WriteString("\n")
			}
		}
		b.WriteString("\n")
	}

	switch step.kind {
	case stepKindText:
		prompt := stylePalettePrompt.Render("> ")
		b.WriteString(prompt + m.ti.View())
		b.WriteString("\n")
		if step.optional {
			b.WriteString(stylePaletteDesc.Render("(optional — press enter to leave blank)"))
			b.WriteString("\n")
		}
	case stepKindChoice:
		b.WriteString(stylePaletteSeparator.Render(strings.Repeat("─", innerW)))
		b.WriteString("\n")
		opts := step.options(m)
		for i, opt := range opts {
			b.WriteString(renderChoiceRow(opt, innerW, i == m.cursor))
			b.WriteString("\n")
		}
	}

	b.WriteString("\n")
	if m.errMsg != "" {
		for i, ln := range wordWrap("! "+m.errMsg, innerW) {
			if i > 0 {
				ln = "  " + ln
			}
			b.WriteString(stylePaletteError.Render(ln))
			b.WriteString("\n")
		}
	}
	b.WriteString(stylePaletteHelp.Render(footerHelp(step)))

	box := stylePaletteBorder.
		Width(innerW + stylePaletteBorder.GetHorizontalPadding()).
		Render(hudtui.ReanchorBrandBg(b.String()))
	if m.width == 0 || m.height == 0 {
		return box
	}
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box)
}

// renderTitleRow draws "New Verifier · 3/7 · Type ////…" filling the
// inner width, mirroring the in-HUD palette's "Commands //…" banner.
func (m *localWizardModel) renderTitleRow(innerW int, step localWizardStep) string {
	cur, total := m.visibleCounts()
	title := fmt.Sprintf("New Verifier · %d/%d · %s ", cur, total, step.title)
	if w := lipgloss.Width(title); w > innerW {
		title = ansi.Truncate(title, innerW, "")
	}
	slashCount := max(innerW-lipgloss.Width(title), 0)
	return stylePaletteTitle.Render(title) + stylePaletteSlash.Render(strings.Repeat("/", slashCount))
}

func (m *localWizardModel) visibleCounts() (int, int) {
	cur, total := 0, 0
	for i, s := range m.steps {
		if !s.show(m) {
			continue
		}
		total++
		if i == m.stepIdx {
			cur = total
		}
	}
	return cur, total
}

func renderChoiceRow(opt localWizardChoice, innerW int, selected bool) string {
	const labelCol = 12
	labelW := labelCol
	if lipgloss.Width(opt.label) >= labelW {
		labelW = lipgloss.Width(opt.label) + 1
	}
	indent := strings.Repeat(" ", labelW)
	avail := innerW - labelW
	if avail < 12 {
		avail = 12
	}
	pad := labelW - lipgloss.Width(opt.label)
	if pad < 1 {
		pad = 1
	}

	var summaryLines []string
	if opt.summary != "" {
		summaryLines = wordWrap(opt.summary, avail)
	} else {
		summaryLines = []string{""}
	}

	style := stylePaletteBody
	if selected {
		style = stylePaletteSelected
	}

	rows := make([]string, 0, len(summaryLines))
	for i, sl := range summaryLines {
		var row string
		if i == 0 {
			row = opt.label + strings.Repeat(" ", pad) + sl
		} else {
			row = indent + sl
		}
		if w := lipgloss.Width(row); w < innerW {
			row += strings.Repeat(" ", innerW-w)
		}
		rows = append(rows, style.Render(row))
	}
	return strings.Join(rows, "\n")
}

func footerHelp(step localWizardStep) string {
	switch step.kind {
	case stepKindChoice:
		return "↑/↓ choose · 1-9 jump · enter next · shift+tab back · esc cancel"
	case stepKindText:
		if step.optional {
			return "type to edit · enter accept (blank ok) · shift+tab back · esc cancel"
		}
		return "type to edit · enter next · shift+tab back · esc cancel"
	}
	return "enter next · esc cancel"
}
