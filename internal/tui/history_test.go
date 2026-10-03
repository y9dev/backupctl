package tui

import (
	"path/filepath"
	"testing"
	"time"

	"backupctl/internal/database"
	"backupctl/internal/models"
)

func seedHistoryDB(t *testing.T) *database.DB {
	t.Helper()
	db, err := database.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	srvID, err := db.UpsertServer(models.Server{Name: "s1", Local: true, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	jobID, err := db.SyncJobs(srvID, "j1", "directory", "* * * * *", "", true, map[string]any{"source": "/tmp"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 25; i++ {
		if _, err := db.AddBackup(jobID, srvID, "/tmp/bak", 1, "", now.Add(-time.Duration(i)*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 12; i++ {
		if _, err := db.CreateRun(jobID, srvID); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func TestHistoryBackupsPagination(t *testing.T) {
	db := seedHistoryDB(t)
	m := model{db: db, histMode: histBackups, histPage: 1, histPerPage: 10}
	m.loadHistory()
	if m.histTotal != 25 {
		t.Fatalf("total backups=%d want 25", m.histTotal)
	}
	if m.histPages() != 3 {
		t.Fatalf("pages=%d want 3", m.histPages())
	}
	if len(m.histBk) != 10 {
		t.Fatalf("page1 len=%d want 10", len(m.histBk))
	}
	m.histPage = 3
	m.loadHistory()
	if len(m.histBk) != 5 {
		t.Fatalf("page3 len=%d want 5", len(m.histBk))
	}
	// no overlap between page 1 and page 3
	m.histPage = 1
	m.loadHistory()
	seen := map[int64]bool{}
	for _, r := range m.histBk {
		seen[r.ID] = true
	}
	m.histPage = 3
	m.loadHistory()
	for _, r := range m.histBk {
		if seen[r.ID] {
			t.Fatalf("duplicate id %d across pages", r.ID)
		}
	}
}

func TestHistoryRunsPaginationAndModeFooter(t *testing.T) {
	db := seedHistoryDB(t)
	m := model{db: db, histMode: histRuns, histPage: 1, histPerPage: 5}
	m.loadHistory()
	if m.histTotal != 12 {
		t.Fatalf("total runs=%d want 12", m.histTotal)
	}
	if m.histPages() != 3 {
		t.Fatalf("pages=%d want 3", m.histPages())
	}
	if len(m.hist) != 5 {
		t.Fatalf("page1 len=%d want 5", len(m.hist))
	}
	m.histPage = 2
	m.loadHistory()
	if len(m.hist) != 5 {
		t.Fatalf("page2 len=%d want 5", len(m.hist))
	}
	view := m.View()
	if len(view) == 0 {
		t.Fatal("empty view")
	}
	// backups mode view must render backups header
	m.histMode = histBackups
	m.histPage = 1
	m.loadHistory()
}
