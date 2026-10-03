package tui

import (
	"fmt"
	"os"

	"backupctl/internal/config"
	"backupctl/internal/database"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var (
	titleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	helpStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	selStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Bold(true)
)

type screen int

const (
	sMenu screen = iota
	sServers
	sJobs
	sHistory
	sSetup
	sRun
)

type model struct {
	cfgPath string
	cfg     *config.Config
	db      *database.DB
	screen  screen
	menu    []string
	cursor  int
	msg     string
	// data
	servers []serverRow
	jobs    []jobRow
	hist    []histRow
	// paginated history (backups or runs, newest first)
	histMode    int // 0 = backups, 1 = runs
	histPage    int // 1-based
	histPerPage int
	histTotal   int
	histBk      []backupRow
}

const (
	histBackups = iota
	histRuns
)

type serverRow struct {
	Name, Host string
	Jobs       int
	Status     string
}
type jobRow struct {
	Server, Name, Type, Schedule string
	Enabled                      bool
}
type histRow struct {
	ID                              int64
	Time, Server, Job, Size, Status string
}
type backupRow struct {
	ID                   int64
	Time, Server, Job, Size string
}

func Run(cfgPath string) error {
	if cfgPath == "" {
		cfgPath = config.DefaultPath()
	}
	// First-run wizard if no config
	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		fmt.Println("No configuration found.")
		fmt.Println("")
		fmt.Println("Welcome to Backup Manager.")
		fmt.Print("Start setup wizard? [Y/n]: ")
		var ans string
		fmt.Scanln(&ans)
		if ans == "" || ans == "Y" || ans == "y" || ans == "yes" {
			if err := RunSetupWizard(cfgPath); err != nil {
				return err
			}
		} else {
			fmt.Println("Create config with `backupctl config validate` or re-run `backupctl tui`.")
			return nil
		}
	}
	m := model{
		cfgPath: cfgPath,
		menu:    []string{"Servers", "Backup Jobs", "Backup History", "Run Backup", "Retention", "Settings", "Exit"},
		// History starts on backups, page 1. loadHistory fills totals.
		histMode:    histBackups,
		histPage:    1,
		histPerPage: 15,
	}
	if c, err := config.Load(cfgPath); err == nil {
		m.cfg = c
		if db, err := database.Open(c.Database.Path); err == nil {
			m.db = db
			defer db.Close()
			m.refresh()
		}
	} else {
		m.msg = "config: " + err.Error()
	}
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err := p.Run()
	return err
}

func (m *model) refresh() {
	if m.db == nil || m.cfg == nil {
		return
	}
	servers, _ := m.db.ListServers()
	m.servers = nil
	for _, s := range servers {
		jobs, _ := m.db.ListJobs(s.ID)
		st := "OK"
		if !s.Enabled {
			st = "DISABLED"
		}
		host := s.Host
		if s.Local {
			host = "local"
		}
		m.servers = append(m.servers, serverRow{s.Name, host, len(jobs), st})
	}
	jobs, _ := m.db.ListJobs(0)
	m.jobs = nil
	for _, j := range jobs {
		srv, _ := m.db.GetServerByID(j.ServerID)
		m.jobs = append(m.jobs, jobRow{srv.Name, j.Name, j.Type, j.Schedule, j.Enabled})
	}
	m.loadHistory()
}

// loadHistory fetches one page of backups or runs (newest first).
func (m *model) loadHistory() {
	if m.db == nil {
		return
	}
	if m.histPerPage < 1 {
		m.histPerPage = 15
	}
	if m.histPage < 1 {
		m.histPage = 1
	}
	offset := (m.histPage - 1) * m.histPerPage
	if m.histMode == histBackups {
		total, _ := m.db.CountBackups(0, 0)
		m.histTotal = total
		bks, _ := m.db.ListBackupsPaged(database.BackupFilter{Limit: m.histPerPage, Offset: offset})
		m.histBk = nil
		for _, b := range bks {
			srv, _ := m.db.GetServerByID(b.ServerID)
			jb, _ := m.db.GetJobByID(b.JobID)
			m.histBk = append(m.histBk, backupRow{b.ID, b.CreatedAt.Local().Format("02.01 15:04"), srv.Name, jb.Name, fmt.Sprintf("%d", b.Size)})
		}
		// clamp page if total shrank
		if pages := m.histPages(); m.histPage > pages && pages > 0 {
			m.histPage = pages
			m.loadHistory()
		}
		return
	}
	total, _ := m.db.CountRuns(0, 0, "")
	m.histTotal = total
	runs, _ := m.db.ListRunsPaged(database.RunFilter{Limit: m.histPerPage, Offset: offset})
	m.hist = nil
	for _, r := range runs {
		srv, _ := m.db.GetServerByID(r.ServerID)
		jb, _ := m.db.GetJobByID(r.JobID)
		m.hist = append(m.hist, histRow{r.ID, r.StartedAt.Local().Format("02.01 15:04"), srv.Name, jb.Name, fmt.Sprintf("%d", r.BackupSize), string(r.Status)})
	}
	if pages := m.histPages(); m.histPage > pages && pages > 0 {
		m.histPage = pages
		m.loadHistory()
	}
}

// histPages returns the total page count for the current history mode.
func (m *model) histPages() int {
	if m.histTotal == 0 || m.histPerPage <= 0 {
		return 1
	}
	return (m.histTotal + m.histPerPage - 1) / m.histPerPage
}

func (m model) Init() tea.Cmd { return nil }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		// Paginated history controls (only on history screen).
		if m.screen == sHistory {
			switch msg.String() {
			case "tab":
				if m.histMode == histBackups {
					m.histMode = histRuns
				} else {
					m.histMode = histBackups
				}
				m.histPage = 1
				m.cursor = 0
				m.loadHistory()
				return m, nil
			case "n", "right", "pgdown":
				if m.histPage < m.histPages() {
					m.histPage++
					m.cursor = 0
					m.loadHistory()
				}
				return m, nil
			case "p", "left", "pgup":
				if m.histPage > 1 {
					m.histPage--
					m.cursor = 0
					m.loadHistory()
				}
				return m, nil
			}
		}
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			m.cursor++
		case "esc":
			m.screen = sMenu
			m.cursor = 0
		case "r":
			m.refresh()
		case "enter", " ":
			m = m.selectCurrent()
		case "?":
			m.msg = "↑↓ Navigate  Enter Select  Esc Back  Q Quit  r Refresh"
		}
	}
	// clamp cursor
	n := m.itemCount()
	if m.cursor >= n && n > 0 {
		m.cursor = n - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	return m, nil
}

func (m model) itemCount() int {
	switch m.screen {
	case sMenu:
		return len(m.menu)
	case sServers:
		return len(m.servers)
	case sJobs:
		return len(m.jobs)
	case sHistory:
		if m.histMode == histBackups {
			return len(m.histBk)
		}
		return len(m.hist)
	default:
		return 1
	}
}

func (m model) selectCurrent() model {
	if m.screen == sMenu {
		switch m.cursor {
		case 0:
			m.screen = sServers
		case 1:
			m.screen = sJobs
		case 2:
			m.screen = sHistory
			m.loadHistory()
		case 3:
			m.msg = "Run: backupctl job run <server> <job>  (non-interactive)"
			m.screen = sRun
		case 4:
			m.msg = "Retention: backupctl retention run"
		case 5:
			if m.cfg != nil {
				m.msg = fmt.Sprintf("Storage: %s  DB: %s", m.cfg.Storage.Path, m.cfg.Database.Path)
			}
		case 6:
			return m
		}
		m.cursor = 0
	}
	return m
}

func (m model) View() string {
	s := titleStyle.Render("Backup Manager") + "\n\n"
	switch m.screen {
	case sMenu:
		for i, item := range m.menu {
			cur := "  "
			if i == m.cursor {
				cur = selStyle.Render("> ")
			}
			s += fmt.Sprintf("%s%s\n", cur, item)
		}
	case sServers:
		s += "NAME            HOST              JOBS  STATUS\n"
		for i, r := range m.servers {
			cur := "  "
			if i == m.cursor {
				cur = "> "
			}
			s += fmt.Sprintf("%s%-15s %-17s %-5d %s\n", cur, r.Name, r.Host, r.Jobs, r.Status)
		}
		if len(m.servers) == 0 {
			s += "(no servers — edit config.yaml to add)\n"
		}
	case sJobs:
		s += "SERVER          JOB             TYPE         SCHEDULE\n"
		for i, r := range m.jobs {
			cur := "  "
			if i == m.cursor {
				cur = "> "
			}
			s += fmt.Sprintf("%s%-15s %-15s %-12s %s\n", cur, r.Server, r.Name, r.Type, r.Schedule)
		}
		if len(m.jobs) == 0 {
			s += "(no jobs)\n"
		}
	case sHistory:
		if m.histMode == histBackups {
			s += fmt.Sprintf("BACKUPS  (page %d/%d, total %d)\n", m.histPage, m.histPages(), m.histTotal)
			s += "ID    TIME          SERVER       JOB           SIZE\n"
			for i, r := range m.histBk {
				cur := "  "
				if i == m.cursor {
					cur = "> "
				}
				s += fmt.Sprintf("%s%-5d %-13s %-12s %-13s %s\n", cur, r.ID, r.Time, r.Server, r.Job, r.Size)
			}
			if len(m.histBk) == 0 {
				s += "(no backups yet)\n"
			}
		} else {
			s += fmt.Sprintf("RUNS  (page %d/%d, total %d)\n", m.histPage, m.histPages(), m.histTotal)
			s += "ID    TIME          SERVER       JOB           SIZE        STATUS\n"
			for i, r := range m.hist {
				cur := "  "
				if i == m.cursor {
					cur = "> "
				}
				s += fmt.Sprintf("%s%-5d %-13s %-12s %-13s %-11s %s\n", cur, r.ID, r.Time, r.Server, r.Job, r.Size, r.Status)
			}
			if len(m.hist) == 0 {
				s += "(no runs yet)\n"
			}
		}
		s += helpStyle.Render("tab: backups/runs   n/p or ←/→: page   r: refresh") + "\n"
	case sRun:
		s += "Manual backup without TUI:\n  backupctl job run <server> <job>\n  backupctl job run --server <server>  (all jobs)\n"
	}
	s += "\n"
	if m.msg != "" {
		s += m.msg + "\n\n"
	}
	s += helpStyle.Render("↑↓ Navigate   Enter Select   Esc Back   Q Quit   r Refresh   ? Help")
	return s
}
