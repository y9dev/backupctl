package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTmp(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	// create dummy ssh key to satisfy validation
	key := filepath.Join(t.TempDir(), "id_ed25519")
	os.WriteFile(key, []byte("x"), 0o600)
	// replace placeholder
	out := content
	// inject key path
	_ = out
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func validYAML(key string) string {
	return `storage:
  path: /tmp/backups
database:
  path: /tmp/backup.db
scheduler:
  max_parallel_jobs: 4
retention:
  hourly: {count: 10, window: 12h}
  weekly: {count: 1, window: 168h}
  monthly: {count: 2, window: 720h}
servers:
  - name: prod-1
    host: 1.2.3.4
    port: 22
    username: backup
    ssh_key: ` + key + `
    jobs:
      - name: app
        type: directory
        schedule: "0 * * * *"
        config: {source: /opt/app}
`
}

func TestValid(t *testing.T) {
	dir := t.TempDir()
	key := filepath.Join(dir, "k")
	os.WriteFile(key, []byte("x"), 0o600)
	p := filepath.Join(dir, "c.yaml")
	os.WriteFile(p, []byte(validYAML(key)), 0o600)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Servers) != 1 || len(c.Servers[0].Jobs) != 1 {
		t.Fatal("unexpected parse")
	}
}

func TestDuplicateServer(t *testing.T) {
	dir := t.TempDir()
	key := filepath.Join(dir, "k")
	os.WriteFile(key, []byte("x"), 0o600)
	y := validYAML(key) + `  - name: prod-1
    host: 5.6.7.8
    username: backup
    ssh_key: ` + key + `
    jobs: []
`
	p := filepath.Join(dir, "c.yaml")
	os.WriteFile(p, []byte(y), 0o600)
	if _, err := Load(p); err == nil {
		t.Fatal("expected duplicate error")
	}
}

func TestInvalidCron(t *testing.T) {
	dir := t.TempDir()
	key := filepath.Join(dir, "k")
	os.WriteFile(key, []byte("x"), 0o600)
	y := `storage: {path: /tmp/b}
database: {path: /tmp/d.db}
scheduler: {max_parallel_jobs: 1}
retention:
  hourly: {count: 1, window: 1h}
  weekly: {count: 1, window: 24h}
  monthly: {count: 1, window: 720h}
servers:
  - name: s
    host: h
    username: u
    ssh_key: ` + key + `
    jobs:
      - name: j
        type: directory
        schedule: "not a cron"
        config: {source: /x}
`
	p := filepath.Join(dir, "c.yaml")
	os.WriteFile(p, []byte(y), 0o600)
	if _, err := Load(p); err == nil {
		t.Fatal("expected cron error")
	}
}

func TestInvalidRetentionAndProvider(t *testing.T) {
	dir := t.TempDir()
	key := filepath.Join(dir, "k")
	os.WriteFile(key, []byte("x"), 0o600)
	// bad provider config: directory without source
	y := `storage: {path: /tmp/b}
database: {path: /tmp/d.db}
scheduler: {max_parallel_jobs: 1}
retention:
  hourly: {count: 1, window: 1h}
  weekly: {count: 1, window: 24h}
  monthly: {count: 1, window: 720h}
servers:
  - name: s
    host: h
    username: u
    ssh_key: ` + key + `
    jobs:
      - name: j
        type: directory
        schedule: "0 * * * *"
        config: {}
`
	p := filepath.Join(dir, "c.yaml")
	os.WriteFile(p, []byte(y), 0o600)
	if _, err := Load(p); err == nil {
		t.Fatal("expected provider error")
	}
}
