package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/store"
)

// reportsCloseMsg returns from the report viewer to the previous page.
type reportsCloseMsg struct{}

// reportsModel is a tab-cycling viewer over every saved .md report of a run.
type reportsModel struct {
	runDir   string
	files    []string
	idx      int
	viewport viewport.Model
	meta     *model.RunMeta // nil for runs without metadata.json
	width    int
	height   int
}

func newReportsModel(runDir string, width, height int) reportsModel {
	m := reportsModel{
		runDir: runDir,
		files:  store.ListReports(runDir),
		width:  width,
		height: height,
	}
	if meta, err := store.LoadMeta(runDir); err == nil {
		m.meta = meta
	}
	m.viewport = viewport.New(max(width-4, 40), max(height-7, 10))
	m.loadCurrent()
	return m
}

func (m *reportsModel) loadCurrent() {
	if len(m.files) == 0 {
		m.viewport.SetContent(mutedStyle.Render("No reports found in " + m.runDir))
		return
	}
	content := store.ReadReportFile(m.runDir, m.files[m.idx])
	if content == "" {
		content = "(empty report)"
	}
	m.viewport.SetContent(content)
	m.viewport.GotoTop()
}

func (m reportsModel) Init() tea.Cmd { return nil }

func (m reportsModel) Update(msg tea.Msg) (reportsModel, tea.Cmd) {
	var cmd tea.Cmd
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.viewport.Width = max(msg.Width-4, 40)
		m.viewport.Height = max(msg.Height-7, 10)

	case tea.KeyMsg:
		switch msg.String() {
		case "esc", "q", "b":
			return m, func() tea.Msg { return reportsCloseMsg{} }
		case "tab", "right", "l":
			if len(m.files) > 0 {
				m.idx = (m.idx + 1) % len(m.files)
				m.loadCurrent()
			}
		case "shift+tab", "left", "h":
			if len(m.files) > 0 {
				m.idx = (m.idx - 1 + len(m.files)) % len(m.files)
				m.loadCurrent()
			}
		default:
			m.viewport, cmd = m.viewport.Update(msg)
			return m, cmd
		}
	}
	return m, nil
}

func (m reportsModel) View() string {
	var sb strings.Builder
	name := "(none)"
	if len(m.files) > 0 {
		name = m.files[m.idx]
	}
	sb.WriteString(titleStyle.Render(fmt.Sprintf("Report %d/%d — %s", m.idx+1, len(m.files), name)))
	sb.WriteString("\n")
	if info := m.domainInfo(name); info != "" {
		sb.WriteString(subtitleStyle.Render(info))
		sb.WriteString("\n")
	}
	sb.WriteString("\n")
	sb.WriteString(m.viewport.View())
	sb.WriteString("\n")
	sb.WriteString(mutedStyle.Render("  tab/←→ switch report · ↑/↓ scroll · esc back"))
	return sb.String()
}

// domainInfo shows grounded/attempts for specialist reports when metadata exists.
func (m reportsModel) domainInfo(fileName string) string {
	if m.meta == nil {
		return ""
	}
	domain := strings.TrimSuffix(fileName, ".md")
	for _, d := range m.meta.Domains {
		if d.Domain == domain {
			g := "ungrounded"
			if d.Grounded {
				g = "grounded"
			}
			return fmt.Sprintf("%s · attempts %d · %dms · %s", g, d.Attempts, d.Duration, d.Status)
		}
	}
	return ""
}
