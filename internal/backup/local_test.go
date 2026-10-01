package backup

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"backupctl/internal/models"
)

func TestSplitEnvPrefix(t *testing.T) {
	env, clean := splitEnvPrefix([]string{"env", "PGPASSWORD=s3cret", "pg_dump", "-h", "127.0.0.1"})
	if len(env) != 1 || env[0] != "PGPASSWORD=s3cret" {
		t.Fatalf("unexpected env: %q", env)
	}
	if len(clean) != 3 || clean[0] != "pg_dump" {
		t.Fatalf("unexpected clean: %q", clean)
	}
	env, clean = splitEnvPrefix([]string{"pg_dump", "-h", "x"})
	if len(env) != 0 || len(clean) != 3 {
		t.Fatalf("plain argv must pass through: %q %q", env, clean)
	}
	env, clean = splitEnvPrefix([]string{"docker", "exec", "-e", "PGPASSWORD=pw", "-i", "c", "pg_dump"})
	if len(env) != 0 {
		t.Fatalf("docker argv must not be treated as env prefix: %q", env)
	}
}

func TestRunLocalStreamsStdout(t *testing.T) {
	tmp := t.TempDir()
	out := filepath.Join(tmp, "out.bin")
	f, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := runLocal(context.Background(), []string{"go", "version"}, f)
	f.Close()
	if err != nil {
		t.Fatalf("runLocal: %v: %s", err, stderr)
	}
	b, _ := os.ReadFile(out)
	if len(b) == 0 {
		t.Fatal("expected non-empty stdout")
	}
}

func TestDirectoryBackupRestoreLocal(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "f.txt"), []byte("local-data"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := models.Server{Name: "local", Local: true}
	job := models.BackupJob{Name: "app", Type: "directory", Config: map[string]any{"source": src}}
	p, err := Get("directory")
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "b")
	res, err := p.Backup(context.Background(), srv, job, dest)
	if err != nil {
		t.Fatalf("local directory backup: %v", err)
	}
	if res.Size == 0 {
		t.Fatal("empty backup")
	}
	restored := filepath.Join(t.TempDir(), "r")
	if err := p.Restore(context.Background(), srv, job, BackupInfo{Path: res.Path}, RestoreOptions{Destination: restored}); err != nil {
		t.Fatalf("local directory restore: %v", err)
	}
}

func TestCommandBackupLocal(t *testing.T) {
	srv := models.Server{Name: "local", Local: true}
	job := models.BackupJob{Name: "cmd", Type: "command", Config: map[string]any{"command": []any{"go", "version"}}}
	p, err := Get("command")
	if err != nil {
		t.Fatal(err)
	}
	res, err := p.Backup(context.Background(), srv, job, filepath.Join(t.TempDir(), "c.bin"))
	if err != nil {
		t.Fatalf("local command backup: %v", err)
	}
	if res.Size == 0 {
		t.Fatal("empty output")
	}
}
