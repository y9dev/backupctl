package backup

import (
	"testing"

	"backupctl/internal/models"
)

func TestRegistryNoBranching(t *testing.T) {
	// scheduler must resolve via registry, not if/else
	for _, typ := range []string{"directory", "postgresql", "command"} {
		if _, err := Get(typ); err != nil {
			t.Fatalf("provider %s not registered: %v", typ, err)
		}
	}
	if _, err := Get("nosuch"); err == nil {
		t.Fatal("expected error for unknown provider")
	}
}

func TestDirectoryMissingSource(t *testing.T) {
	p := DirectoryProvider{}
	_, err := p.Backup(t.Context(), models.Server{}, models.BackupJob{Config: map[string]any{}}, t.TempDir()+"/x.tar.zst")
	if err == nil {
		t.Fatal("expected source error (fails before SSH)")
	}
}

func TestPostgresMissingConfig(t *testing.T) {
	p := PostgresProvider{}
	_, err := p.Backup(t.Context(), models.Server{}, models.BackupJob{Config: map[string]any{}}, t.TempDir()+"/x.dump")
	if err == nil {
		t.Fatal("expected config error")
	}
}

func TestCommandMissing(t *testing.T) {
	p := CommandProvider{}
	_, err := p.Backup(t.Context(), models.Server{}, models.BackupJob{Config: map[string]any{}}, t.TempDir()+"/x.bin")
	if err == nil {
		t.Fatal("expected command error")
	}
}

func TestDumpArgvHostMode(t *testing.T) {
	s := pgSettings{Host: "127.0.0.1", Port: "5432", Database: "production", Username: "backup", Password: "s3cret"}
	got := buildDumpArgv(s)
	want := []string{"env", "PGPASSWORD=s3cret", "pg_dump", "-h", "127.0.0.1", "-p", "5432", "-U", "backup", "-Fc", "production"}
	if !equalStr(got, want) {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestDumpArgvHostModeNoPassword(t *testing.T) {
	s := pgSettings{Host: "127.0.0.1", Port: "5432", Database: "db", Username: "u"}
	got := buildDumpArgv(s)
	want := []string{"pg_dump", "-h", "127.0.0.1", "-p", "5432", "-U", "u", "-Fc", "db"}
	if !equalStr(got, want) {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestDumpArgvDockerMode(t *testing.T) {
	s := pgSettings{Host: "127.0.0.1", Port: "5432", Database: "production", Username: "backup", Password: "s3cret", Container: "pg-prod", DockerBin: "docker"}
	got := buildDumpArgv(s)
	want := []string{"docker", "exec", "-e", "PGPASSWORD=s3cret", "-i", "pg-prod", "pg_dump", "-h", "127.0.0.1", "-p", "5432", "-U", "backup", "-Fc", "production"}
	if !equalStr(got, want) {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestRestoreArgvDockerMode(t *testing.T) {
	s := pgSettings{Host: "127.0.0.1", Port: "5432", Username: "backup", Password: "pw", Container: "pg-prod", DockerBin: "docker"}
	got := buildRestoreArgv(s, "newdb")
	want := []string{"docker", "exec", "-e", "PGPASSWORD=pw", "-i", "pg-prod", "pg_restore", "-h", "127.0.0.1", "-p", "5432", "-U", "backup", "-d", "newdb", "--clean", "--if-exists"}
	if !equalStr(got, want) {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestPgSettingsContainerParsing(t *testing.T) {
	j := models.BackupJob{Config: map[string]any{
		"database": "production", "username": "backup",
		"container": "pg-prod", "docker": "/usr/bin/docker",
	}}
	s := pgSettingsFrom(j)
	if s.Container != "pg-prod" || s.DockerBin != "/usr/bin/docker" {
		t.Fatalf("unexpected settings: %+v", s)
	}
	// nested connection map
	j2 := models.BackupJob{Config: map[string]any{
		"connection": map[string]any{"database": "d", "username": "u", "container": "c2"},
	}}
	if s2 := pgSettingsFrom(j2); s2.Container != "c2" || s2.DockerBin != "docker" {
		t.Fatalf("unexpected nested settings: %+v", s2)
	}
}

func equalStr(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
