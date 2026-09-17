// Package tui provides the Bubble Tea TUI for Claude Financial Researcher.
// Screens: home, run (live status), results (+detail), history, report viewer,
// wired through a top-level App model.
package tui

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/orchestrator"
	"github.com/mamut/claude-financial-researcher/internal/store"
)

// page names the currently active screen.
type page int

const (
	pageHome page = iota
	pageRun
	pageResults
	pageHistory
	pageReports
	pageScoreboard
)

// RunFunc is the constructor for an orchestrator run channel; injected so the
// TUI does not need to import orchestrator's Config directly. It takes the
// context the run should observe, so the TUI can cancel a live pipeline
// instead of merely stopping listening to it.
type RunFunc func(ctx context.Context, req model.RunRequest) <-chan orchestrator.Event

// App is the top-level Bubble Tea model.
type App struct {
	page       page
	home       homeModel
	run        runModel
	results    resultsModel
	history    historyModel
	reports    reportsModel
	scoreboard scoreboardModel
	runFn      RunFunc
	sbFn       ScoreboardFunc

	runsDir string // where run artifacts live (history browsing)
	lastReq model.RunRequest
	// runCancel stops the run currently on the run screen, if any. Set every
	// time a run starts, so ctrl+c can actually cancel the pipeline instead
	// of just walking away from it.
	runCancel context.CancelFunc

	width  int
	height int
}

// New creates a ready-to-use App. runsDir is the artifacts directory shown in
// the history screen (usually "runs"); sbFn builds the performance scoreboard.
func New(runFn RunFunc, runsDir string, sbFn ScoreboardFunc) *App {
	if runsDir == "" {
		runsDir = "runs"
	}
	return &App{
		page:    pageHome,
		home:    newHomeModel(),
		runFn:   runFn,
		sbFn:    sbFn,
		runsDir: runsDir,
	}
}

func (a *App) Init() tea.Cmd {
	return a.home.Init()
}

// startRun begins a pipeline run and wires up its cancellation, so a later
// abort can actually stop the pipeline rather than just stop listening to it.
func (a *App) startRun(req model.RunRequest) tea.Cmd {
	ctx, cancel := context.WithCancel(context.Background())
	a.runCancel = cancel
	a.lastReq = req
	eventCh := a.runFn(ctx, req)
	a.run = newRunModel(req, eventCh, cancel)
	a.page = pageRun
	return a.run.Init()
}

func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		a.width = msg.Width
		a.height = msg.Height
		var cmd tea.Cmd
		var cmds []tea.Cmd
		a.home, cmd = a.home.Update(msg)
		cmds = append(cmds, cmd)
		a.run, cmd = a.run.Update(msg)
		cmds = append(cmds, cmd)
		a.results, cmd = a.results.Update(msg)
		cmds = append(cmds, cmd)
		a.history, cmd = a.history.Update(msg)
		cmds = append(cmds, cmd)
		a.reports, cmd = a.reports.Update(msg)
		cmds = append(cmds, cmd)
		a.scoreboard, cmd = a.scoreboard.Update(msg)
		cmds = append(cmds, cmd)
		return a, tea.Batch(cmds...)

	case tea.KeyMsg:
		// Global quit shortcut — except mid-run, where ctrl+c must cancel the
		// pipeline (see below) rather than abandon it still running by
		// exiting the whole program immediately.
		if msg.String() == "ctrl+c" && !(a.page == pageRun && !a.run.done) {
			return a, tea.Quit
		}
		// Results (list mode) or Run screen: "r" retries the last request.
		if (a.page == pageRun || (a.page == pageResults && !a.results.inDetail())) && msg.String() == "r" {
			if a.lastReq.Mode != "" {
				return a, a.startRun(a.lastReq)
			}
		}
		// Results (list mode) or Run screen: "b" goes back home
		if (a.page == pageRun || (a.page == pageResults && !a.results.inDetail())) && msg.String() == "b" {
			a.page = pageHome
			a.home = newHomeModel()
			return a, a.home.Init()
		}
		// Results screen (list mode): esc returns to history (if that's where
		// we came from) or quits; q always quits.
		if a.page == pageResults && !a.results.inDetail() {
			if msg.String() == "q" {
				return a, tea.Quit
			}
			if msg.String() == "esc" {
				if a.results.fromHistory {
					a.page = pageHistory
					return a, nil
				}
				return a, tea.Quit
			}
		}

	// Home → Run transition
	case startRunMsg:
		return a, a.startRun(msg.req)

	// Home → History
	case openHistoryMsg:
		a.history = newHistoryModel(a.runsDir)
		a.page = pageHistory
		return a, a.history.Init()

	case historyBackMsg:
		a.page = pageHome
		a.home = newHomeModel()
		return a, a.home.Init()

	// Home → Scoreboard (async price fetch kicked off by Init)
	case openScoreboardMsg:
		a.scoreboard = newScoreboardModel(a.sbFn)
		a.page = pageScoreboard
		return a, a.scoreboard.Init()

	case scoreboardBackMsg:
		a.page = pageHome
		a.home = newHomeModel()
		return a, a.home.Init()

	// History → Results (loaded from disk)
	case historyOpenMsg:
		ideas, err := store.LoadIdeas(msg.dir)
		if err != nil || ideas == nil {
			// No parseable ideas: fall through to the report viewer so the
			// run is still inspectable.
			a.reports = newReportsModel(msg.dir, a.width, a.height)
			a.page = pageReports
			return a, a.reports.Init()
		}
		a.results = newResultsModel(ideas, msg.dir)
		if meta, err := store.LoadMeta(msg.dir); err == nil {
			a.results.meta = meta
		}
		a.results.fromHistory = true
		if a.width > 0 {
			a.results.width, a.results.height = a.width, a.height
		}
		a.page = pageResults
		return a, a.results.Init()

	// Results/detail → report viewer
	case openReportsMsg:
		a.reports = newReportsModel(msg.runDir, a.width, a.height)
		a.page = pageReports
		return a, a.reports.Init()

	case reportsCloseMsg:
		// Return to results when ideas are loaded; otherwise to history.
		if a.results.ideas != nil {
			a.page = pageResults
		} else {
			a.page = pageHistory
		}
		return a, nil

	// Run → Results transition (EventComplete bubbled up via runModel)
	case orchestrator.Event:
		if a.page == pageRun {
			var cmd tea.Cmd
			a.run, cmd = a.run.Update(msg)
			if msg.Type == orchestrator.EventComplete && msg.Ideas != nil {
				a.results = newResultsModel(msg.Ideas, msg.Message)
				a.results.meta = msg.Meta
				if a.width > 0 {
					a.results.width, a.results.height = a.width, a.height
				}
				a.page = pageResults
				return a, a.results.Init()
			}
			return a, cmd
		}
	}

	// Delegate to active page
	switch a.page {
	case pageHome:
		var cmd tea.Cmd
		a.home, cmd = a.home.Update(msg)
		return a, cmd

	case pageRun:
		var cmd tea.Cmd
		a.run, cmd = a.run.Update(msg)
		return a, cmd

	case pageResults:
		var cmd tea.Cmd
		a.results, cmd = a.results.Update(msg)
		return a, cmd

	case pageHistory:
		var cmd tea.Cmd
		a.history, cmd = a.history.Update(msg)
		return a, cmd

	case pageReports:
		var cmd tea.Cmd
		a.reports, cmd = a.reports.Update(msg)
		return a, cmd

	case pageScoreboard:
		var cmd tea.Cmd
		a.scoreboard, cmd = a.scoreboard.Update(msg)
		return a, cmd
	}

	return a, nil
}

func (a *App) View() string {
	switch a.page {
	case pageHome:
		return a.home.View()
	case pageRun:
		return a.run.View()
	case pageResults:
		return a.results.View()
	case pageHistory:
		return a.history.View()
	case pageReports:
		return a.reports.View()
	case pageScoreboard:
		return a.scoreboard.View()
	}
	return ""
}
