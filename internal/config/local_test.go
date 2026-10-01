package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLocalServerNoSSHRequired(t *testing.T) {
	dir := t.TempDir()
	y := `storage: {path: /tmp/b}
database: {path: /tmp/d.db}
scheduler: {max_parallel_jobs: 1}
retention:
  hourly: {count: 1, window: 1h}
  weekly: {count: 1, window: 24h}
  monthly: {count: 1, window: 720h}
servers:
  - name: local
    local: true
    jobs:
      - name: app
        type: directory
        schedule: "0 * * * *"
        config: {source: /opt/app}
      - name: pg
        type: postgresql
        schedule: "0 * * * *"
        config: {database: db, username: u}
      - name: cmd
        type: command
        schedule: "0 * * * *"
        config: {command: [/bin/true]}
`
	p := filepath.Join(dir, "c.yaml")
	if err := os.WriteFile(p, []byte(y), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatalf("local server should validate without host/username/key: %v", err)
	}
	if !c.Servers[0].Local {
		t.Fatal("expected local=true")
	}
	if len(c.Servers[0].Jobs) != 3 {
		t.Fatalf("all job variants must be allowed for local servers, got %d", len(c.Servers[0].Jobs))
	}
}

func TestRemoteServerStillRequiresHost(t *testing.T) {
	dir := t.TempDir()
	y := `storage: {path: /tmp/b}
database: {path: /tmp/d.db}
scheduler: {max_parallel_jobs: 1}
retention:
  hourly: {count: 1, window: 1h}
  weekly: {count: 1, window: 24h}
  monthly: {count: 1, window: 720h}
servers:
  - name: prod-1
    username: backup
    jobs:
      - name: app
        type: directory
        schedule: "0 * * * *"
        config: {source: /opt/app}
`
	p := filepath.Join(dir, "c.yaml")
	if err := os.WriteFile(p, []byte(y), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err == nil {
		t.Fatal("expected host-required error for non-local server")
	}
}
