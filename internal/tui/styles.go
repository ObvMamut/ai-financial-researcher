package tui

import "github.com/charmbracelet/lipgloss"

var (
	colorPrimary   = lipgloss.Color("#7C3AED") // purple
	colorAccent    = lipgloss.Color("#10B981") // green
	colorMuted     = lipgloss.Color("#6B7280") // grey
	colorWarning   = lipgloss.Color("#F59E0B") // amber
	colorDanger    = lipgloss.Color("#EF4444") // red
	colorBuy       = lipgloss.Color("#10B981") // green
	colorSell      = lipgloss.Color("#EF4444") // red
	colorHighlight = lipgloss.Color("#F3F4F6") // near-white

	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorPrimary).
			MarginBottom(1)

	subtitleStyle = lipgloss.NewStyle().
			Foreground(colorMuted).
			Italic(true)

	selectedStyle = lipgloss.NewStyle().
			Foreground(colorAccent).
			Bold(true)

	mutedStyle = lipgloss.NewStyle().Foreground(colorMuted)

	boxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorPrimary).
			Padding(0, 1)

	buyStyle  = lipgloss.NewStyle().Bold(true).Foreground(colorBuy)
	sellStyle = lipgloss.NewStyle().Bold(true).Foreground(colorSell)

	doneStyle    = lipgloss.NewStyle().Foreground(colorAccent)
	runningStyle = lipgloss.NewStyle().Foreground(colorWarning)
	failedStyle  = lipgloss.NewStyle().Foreground(colorDanger)
	queuedStyle  = lipgloss.NewStyle().Foreground(colorMuted)
)
