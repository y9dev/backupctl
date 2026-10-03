package main

import (
	"path/filepath"
	"testing"

	"backupctl/internal/database"
	"backupctl/internal/models"
)

func TestNormalizeHistoryPage(t *testing.T) {
	p, pp := normalizeHistoryPage(0, 0)
	if p != 1 || pp != 20 {
		t.Fatalf("defaults: got %d/%d", p, pp)
	}
	p, pp = normalizeHistoryPage(-3, 500)
	if p != 1 || pp != 100 {
		t.Fatalf("clamp: got %d/%d", p, pp)
	}
	p, pp = normalizeHistoryPage(2, 5)
	if p != 2 || pp != 5 {
		t.Fatalf("passthrough: got %d/%d", p, pp)
	}
}

func TestHistoryFooter(t *testing.T) {
	if got := historyFooter(1, 20, 0); got != "No entries." {
		t.Fatalf("empty: %q", got)
	}
	if got := historyFooter(2, 10, 25); got != "Page 2/3 — showing 11-20 of 25 (per-page 10)" {
		t.Fatalf("middle: %q", got)
	}
	if got := historyFooter(3, 10, 25); got != "Page 3/3 — showing 21-25 of 25 (per-page 10)" {
		t.Fatalf("last: %q", got)
	}
}

func TestResolveHistoryFilter(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srvID, err := db.UpsertServer(models.Server{Name: "s1", Local: true, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	jobID, err := db.SyncJobs(srvID, "j1", "directory", "* * * * *", "", true, map[string]any{"source": "/tmp"})
	if err != nil {
		t.Fatal(err)
	}
	j, s, err := resolveHistoryFilter(db, "s1", "j1")
	if err != nil || j != jobID || s != srvID {
		t.Fatalf("server+job: %d/%d err=%v", j, s, err)
	}
	j, s, err = resolveHistoryFilter(db, "", "j1")
	if err != nil || j != jobID {
		t.Fatalf("job alone: %d/%d err=%v", j, s, err)
	}
	if _, _, err := resolveHistoryFilter(db, "nope", ""); err == nil {
		t.Fatal("unknown server must error")
	}
	// ambiguous job name across servers
	srv2, _ := db.UpsertServer(models.Server{Name: "s2", Local: true, Enabled: true})
	_, _ = db.SyncJobs(srv2, "j1", "directory", "* * * * *", "", true, map[string]any{"source": "/tmp"})
	if _, _, err := resolveHistoryFilter(db, "", "j1"); err == nil {
		t.Fatal("ambiguous job must require --server")
	}
}
