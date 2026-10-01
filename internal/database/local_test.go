package database

import (
	"path/filepath"
	"testing"

	"backupctl/internal/models"
)

func TestUpsertServerLocalRoundTrip(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	id, err := db.UpsertServer(models.Server{Name: "local", Local: true, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	srv, err := db.GetServerByID(id)
	if err != nil {
		t.Fatal(err)
	}
	if !srv.Local {
		t.Fatal("expected local=true round-trip")
	}
	// existing remote servers still work
	if _, err := db.UpsertServer(models.Server{Name: "prod", Host: "1.2.3.4", Port: 22, Username: "backup", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	servers, err := db.ListServers()
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 2 {
		t.Fatalf("expected 2 servers, got %d", len(servers))
	}
}

func TestMigrateKeepsOldDBs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "old.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	// simulate pre-local schema by dropping the column value path:
	// insert then reopen (migrate must be idempotent)
	if _, err := db.UpsertServer(models.Server{Name: "s", Host: "h", Username: "u", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	db.Close()
	db2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen old db: %v", err)
	}
	defer db2.Close()
	srv, err := db2.GetServerByName("s")
	if err != nil {
		t.Fatal(err)
	}
	if srv.Local {
		t.Fatal("old server must default to local=false")
	}
}
