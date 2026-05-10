package hud

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"gopkg.in/yaml.v3"

	"github.com/uriahlevy/hud/internal/config"
)

type editPhase int

const (
	editSelect editPhase = iota
	editMetadata
	editSkill
)

var (
	styleEditBox    = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
	styleEditTitle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	styleEditHelp   = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	styleEditCursor = lipgloss.NewStyle().Foreground(lipgloss.Color("12")).Bold(true)
	styleEditSaved  = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
)

// EditWizard is the in-TUI verifier editor. It intentionally writes only on
// explicit save keys; skip/abort only changes the in-memory draft.
type EditWizard struct {
	configPath string
	configDir  string
	file       config.File

	phase    editPhase
	cursor   int
	selected int

	text    textBuffer
	message string
	errMsg  string
	saved   bool

	width  int
	height int
}

// NewEditWizard loads hud.yaml and starts at the verifier picker.
func NewEditWizard(configPath string) EditWizard {
	w := EditWizard{configPath: configPath, selected: -1}
	if configPath == "" {
		w.errMsg = "no hud.yaml is loaded; demo verifiers cannot be edited"
		return w
	}
	f, path, err := config.Load(configPath)
	if err != nil {
		w.errMsg = err.Error()
		return w
	}
	w.configPath = path
	w.configDir = filepath.Dir(path)
	w.file = *f
	return w
}

// NewEditWizardFor loads hud.yaml and jumps straight into editing the named
// verifier when it exists. Missing names fall back to the normal picker.
func NewEditWizardFor(configPath, verifierName string) EditWizard {
	w := NewEditWizard(configPath)
	if w.errMsg != "" || verifierName == "" {
		return w
	}
	for i, v := range w.file.Verifiers {
		if v.Name == verifierName {
			w.cursor = i
			w.selected = i
			w.startMetadata()
			return w
		}
	}
	return w
}

// Update handles a wizard event. done is true when the main HUD should close
// the wizard and return to the compass.
func (w EditWizard) Update(msg tea.Msg) (EditWizard, tea.Cmd, bool) {
	if size, ok := msg.(tea.WindowSizeMsg); ok {
		w.width = size.Width
		w.height = size.Height
		return w, nil, false
	}
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return w, nil, false
	}
	if key.String() == "ctrl+c" || key.String() == "esc" {
		return w, nil, true
	}
	if w.errMsg != "" && len(w.file.Verifiers) == 0 {
		return w, nil, key.String() == "q" || key.String() == "enter"
	}

	switch w.phase {
	case editSelect:
		return w.updateSelect(key)
	case editMetadata:
		return w.updateMetadata(key)
	case editSkill:
		return w.updateSkill(key)
	default:
		return w, nil, true
	}
}

func (w EditWizard) updateSelect(key tea.KeyMsg) (EditWizard, tea.Cmd, bool) {
	switch key.String() {
	case "q":
		return w, nil, true
	case "up", "k":
		if w.cursor > 0 {
			w.cursor--
		}
	case "down", "j":
		if w.cursor < len(w.file.Verifiers)-1 {
			w.cursor++
		}
	case "enter":
		if len(w.file.Verifiers) == 0 {
			w.errMsg = "no verifiers configured"
			return w, nil, false
		}
		w.selected = w.cursor
		w.startMetadata()
	}
	return w, nil, false
}

func (w EditWizard) updateMetadata(key tea.KeyMsg) (EditWizard, tea.Cmd, bool) {
	switch key.String() {
	case "ctrl+s":
		if err := w.saveMetadata(); err != nil {
			w.errMsg = err.Error()
			return w, nil, false
		}
		w.message = "metadata saved"
		w.startSkill()
	case "ctrl+n":
		w.message = "metadata skipped"
		w.startSkill()
	default:
		w.text.Update(key)
	}
	return w, nil, false
}

func (w EditWizard) updateSkill(key tea.KeyMsg) (EditWizard, tea.Cmd, bool) {
	switch key.String() {
	case "ctrl+s":
		if err := w.saveSkill(); err != nil {
			w.errMsg = err.Error()
			return w, nil, false
		}
		w.message = "skill saved"
		return w, nil, true
	case "ctrl+n":
		w.message = "skill skipped"
		return w, nil, true
	default:
		if w.skillPath() == "" {
			return w, nil, false
		}
		w.text.Update(key)
	}
	return w, nil, false
}

func (w *EditWizard) startMetadata() {
	w.phase = editMetadata
	w.errMsg = ""
	w.message = ""
	raw, err := yaml.Marshal(w.file.Verifiers[w.selected])
	if err != nil {
		w.errMsg = err.Error()
		w.text = newTextBuffer("")
		return
	}
	w.text = newTextBuffer(strings.TrimRight(string(raw), "\n"))
}

func (w *EditWizard) startSkill() {
	w.phase = editSkill
	w.errMsg = ""
	path := w.skillPath()
	if path == "" {
		w.text = newTextBuffer("")
		w.message = "no llm.skill path for this verifier; skip to finish"
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		w.text = newTextBuffer("")
		w.errMsg = fmt.Sprintf("read %s: %v", path, err)
		return
	}
	w.text = newTextBuffer(strings.TrimRight(string(raw), "\n"))
}

func (w *EditWizard) saveMetadata() error {
	if w.selected < 0 || w.selected >= len(w.file.Verifiers) {
		return fmt.Errorf("no verifier selected")
	}
	var spec config.VerifierSpec
	if err := yaml.Unmarshal([]byte(w.text.String()), &spec); err != nil {
		return fmt.Errorf("parse metadata yaml: %w", err)
	}
	next := w.file
	next.Verifiers = append([]config.VerifierSpec(nil), w.file.Verifiers...)
	next.Verifiers[w.selected] = spec
	if _, err := next.Resolve(w.configDir); err != nil {
		return err
	}
	if err := config.Save(w.configPath, &next); err != nil {
		return err
	}
	w.file = next
	w.saved = true
	return nil
}

func (w EditWizard) saveSkill() error {
	path := w.skillPath()
	if path == "" {
		return fmt.Errorf("selected verifier has no llm.skill path")
	}
	if err := writeFileAtomic(path, []byte(w.text.String()+"\n"), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func (w EditWizard) skillPath() string {
	if w.selected < 0 || w.selected >= len(w.file.Verifiers) {
		return ""
	}
	p := w.file.Verifiers[w.selected].LLM.Skill
	if p == "" {
		return ""
	}
	return config.ResolveLocalPath(w.configDir, p)
}

// View renders the wizard as a full-screen replacement for the compass.
func (w EditWizard) View() string {
	width := w.width
	if width < 60 {
		width = 60
	}
	contentW := width - styleEditBox.GetHorizontalFrameSize() - styleEditBox.GetHorizontalPadding()
	if contentW < 20 {
		contentW = 20
	}

	var b strings.Builder
	b.WriteString(styleEditTitle.Render(w.title()))
	b.WriteString("\n")
	b.WriteString(styleEditHelp.Render(w.help()))
	b.WriteString("\n\n")

	switch w.phase {
	case editSelect:
		b.WriteString(w.renderSelect(contentW))
	case editMetadata, editSkill:
		b.WriteString(w.renderEditor(contentW))
	}

	if w.message != "" {
		b.WriteString("\n")
		b.WriteString(styleEditSaved.Render(w.message))
	}
	if w.errMsg != "" {
		b.WriteString("\n")
		b.WriteString(stylePickerError.Render(w.errMsg))
	}
	return styleEditBox.Width(width - styleEditBox.GetHorizontalFrameSize()).Render(b.String())
}

func (w EditWizard) title() string {
	switch w.phase {
	case editSelect:
		return "HUD verifier editor"
	case editMetadata:
		return "Step 1/2: hud.yaml metadata"
	case editSkill:
		return "Step 2/2: SKILL.md content"
	default:
		return "HUD verifier editor"
	}
}

func (w EditWizard) help() string {
	switch w.phase {
	case editSelect:
		return "up/down choose verifier | enter edit | esc abort"
	case editMetadata:
		return "edit verifier YAML | ctrl+s save and continue | ctrl+n skip | esc abort"
	case editSkill:
		if w.skillPath() == "" {
			return "no skill file for this verifier | ctrl+n finish | esc abort"
		}
		return "edit SKILL.md | ctrl+s save and finish | ctrl+n skip | esc abort"
	default:
		return "esc abort"
	}
}

func (w EditWizard) renderSelect(width int) string {
	if len(w.file.Verifiers) == 0 {
		return styleReason.Render("(no verifiers configured)")
	}
	var b strings.Builder
	for i, v := range w.file.Verifiers {
		cursor := "  "
		if i == w.cursor {
			cursor = styleEditCursor.Render("> ")
		}
		kind := v.Type
		if kind == "" {
			kind = "command"
		}
		line := fmt.Sprintf("%s%-16s %-7s %-3s %s", cursor, v.Name, kind, strings.ToUpper(v.Direction), v.LLM.Skill)
		b.WriteString(truncate(line, width))
		if i < len(w.file.Verifiers)-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func (w EditWizard) renderEditor(width int) string {
	rows := w.height - 8
	if rows < 8 {
		rows = 8
	}
	if rows > 28 {
		rows = 28
	}
	if w.phase == editSkill && w.skillPath() != "" {
		pathLine := styleHeaderLabel.Render("file: ") + truncate(w.skillPath(), width-len("file: "))
		return pathLine + "\n" + w.text.View(width, rows-1)
	}
	return w.text.View(width, rows)
}

type textBuffer struct {
	lines []string
	row   int
	col   int
}

func newTextBuffer(s string) textBuffer {
	lines := strings.Split(s, "\n")
	if len(lines) == 0 {
		lines = []string{""}
	}
	return textBuffer{lines: lines}
}

func (b *textBuffer) Update(key tea.KeyMsg) {
	if len(b.lines) == 0 {
		b.lines = []string{""}
	}
	switch key.String() {
	case "up":
		if b.row > 0 {
			b.row--
			b.clampCol()
		}
	case "down":
		if b.row < len(b.lines)-1 {
			b.row++
			b.clampCol()
		}
	case "left":
		if b.col > 0 {
			b.col--
		} else if b.row > 0 {
			b.row--
			b.col = len([]rune(b.lines[b.row]))
		}
	case "right":
		lineLen := len([]rune(b.lines[b.row]))
		if b.col < lineLen {
			b.col++
		} else if b.row < len(b.lines)-1 {
			b.row++
			b.col = 0
		}
	case "home":
		b.col = 0
	case "end":
		b.col = len([]rune(b.lines[b.row]))
	case "backspace", "ctrl+h":
		b.backspace()
	case "delete":
		b.delete()
	case "enter":
		b.splitLine()
	default:
		if len(key.Runes) > 0 {
			b.insert(string(key.Runes))
		}
	}
}

func (b *textBuffer) String() string {
	return strings.Join(b.lines, "\n")
}

func (b *textBuffer) View(width, rows int) string {
	if rows < 1 {
		rows = 1
	}
	start := b.row - rows/2
	if start < 0 {
		start = 0
	}
	if start+rows > len(b.lines) {
		start = len(b.lines) - rows
		if start < 0 {
			start = 0
		}
	}
	end := start + rows
	if end > len(b.lines) {
		end = len(b.lines)
	}
	var out strings.Builder
	for i := start; i < end; i++ {
		line := b.lines[i]
		if i == b.row {
			line = insertCursor(line, b.col)
		}
		prefix := fmt.Sprintf("%3d ", i+1)
		out.WriteString(styleHeaderLabel.Render(prefix))
		out.WriteString(truncate(line, width-lipgloss.Width(prefix)))
		if i < end-1 {
			out.WriteByte('\n')
		}
	}
	return out.String()
}

func (b *textBuffer) clampCol() {
	if n := len([]rune(b.lines[b.row])); b.col > n {
		b.col = n
	}
}

func (b *textBuffer) insert(s string) {
	runes := []rune(b.lines[b.row])
	next := append([]rune{}, runes[:b.col]...)
	next = append(next, []rune(s)...)
	next = append(next, runes[b.col:]...)
	b.lines[b.row] = string(next)
	b.col += len([]rune(s))
}

func (b *textBuffer) backspace() {
	if b.col > 0 {
		runes := []rune(b.lines[b.row])
		b.lines[b.row] = string(append(runes[:b.col-1], runes[b.col:]...))
		b.col--
		return
	}
	if b.row == 0 {
		return
	}
	prevLen := len([]rune(b.lines[b.row-1]))
	b.lines[b.row-1] += b.lines[b.row]
	b.lines = append(b.lines[:b.row], b.lines[b.row+1:]...)
	b.row--
	b.col = prevLen
}

func (b *textBuffer) delete() {
	runes := []rune(b.lines[b.row])
	if b.col < len(runes) {
		b.lines[b.row] = string(append(runes[:b.col], runes[b.col+1:]...))
		return
	}
	if b.row < len(b.lines)-1 {
		b.lines[b.row] += b.lines[b.row+1]
		b.lines = append(b.lines[:b.row+1], b.lines[b.row+2:]...)
	}
}

func (b *textBuffer) splitLine() {
	runes := []rune(b.lines[b.row])
	left := string(runes[:b.col])
	right := string(runes[b.col:])
	b.lines[b.row] = left
	next := append([]string{}, b.lines[:b.row+1]...)
	next = append(next, right)
	next = append(next, b.lines[b.row+1:]...)
	b.lines = next
	b.row++
	b.col = 0
}

func insertCursor(s string, col int) string {
	runes := []rune(s)
	if col < 0 {
		col = 0
	}
	if col > len(runes) {
		col = len(runes)
	}
	next := append([]rune{}, runes[:col]...)
	next = append(next, '|')
	next = append(next, runes[col:]...)
	return string(next)
}
