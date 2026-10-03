package database

import (
	"path/filepath"
	"testing"
	"time"
)

func TestBackupsPagedSlice1(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 25; i++ {
		if _, err := db.AddBackup(1, 1, filepath.Join(t.TempDir(), "x"), 1, "", now.Add(-time.Duration(i)*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	n, err := db.CountBackups(0, 0)
	if err != nil || n != 25 {
		t.Fatalf("count=%d err=%v, want 25", n, err)
	}
	p1, _ := db.ListBackupsPaged(BackupFilter{Limit: 10, Offset: 0})
	p2, _ := db.ListBackupsPaged(BackupFilter{Limit: 10, Offset: 10})
	p3, _ := db.ListBackupsPaged(BackupFilter{Limit: 10, Offset: 20})
	if len(p1) != 10 || len(p2) != 10 || len(p3) != 5 {
		t.Fatalf("pages: %d/%d/%d", len(p1), len(p2), len(p3))
	}
	seen := map[int64]bool{}
	for _, b := range append(append(p1, p2...), p3...) {
		if seen[b.ID] {
			t.Fatalf("duplicate id %d across pages", b.ID)
		}
		seen[b.ID] = true
	}
	if !p1[0].CreatedAt.After(p1[1].CreatedAt) {
		t.Fatal("not newest-first")
	}
	// offset without limit
	all, _ := db.ListBackupsPaged(BackupFilter{Offset: 20})
	if len(all) != 5 {
		t.Fatalf("offset-only expected 5, got %d", len(all))
	}
	// legacy wrapper still works
	leg, _ := db.ListBackups(0, 50)
	if len(leg) != 25 {
		t.Fatalf("legacy ListBackups got %d", len(leg))
	}
}

func TestRunsPagedSlice1(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for i := 0; i < 12; i++ {
		id, err := db.CreateRun(1, 1)
		if err != nil {
			t.Fatal(err)
		}
		_ = id
	}
	n, _ := db.CountRuns(0, 0, "")
	if n != 12 {
		t.Fatalf("count runs=%d want 12", n)
	}
	p1, _ := db.ListRunsPaged(RunFilter{Limit: 5, Offset: 0})
	p2, _ := db.ListRunsPaged(RunFilter{Limit: 5, Offset: 5})
	p3, _ := db.ListRunsPaged(RunFilter{Limit: 5, Offset: 10})
	if len(p1) != 5 || len(p2) != 5 || len(p3) != 2 {
		t.Fatalf("run pages: %d/%d/%d", len(p1), len(p2), len(p3))
	}
	leg, _ := db.ListRuns(12)
	if len(leg) != 12 {
		t.Fatalf("legacy ListRuns got %d", len(leg))
	}
}
