package backup

import (
	"context"
	"fmt"
	"time"

	"backupctl/internal/models"
)

type Server = models.Server
type Job = models.BackupJob
type BackupRecord = models.Backup

type BackupResult struct {
	Path      string
	Size      int64
	Checksum  string
	CreatedAt time.Time
}

type RestoreOptions struct {
	Destination string
	Database    string
	Extra       map[string]string
}

type BackupInfo struct {
	ID        int64
	Path      string
	CreatedAt time.Time
	Size      int64
	Checksum  string
}

// Provider interface: scheduler never branches on type.
type Provider interface {
	Name() string
	Backup(ctx context.Context, server Server, job Job, destination string) (BackupResult, error)
	Restore(ctx context.Context, server Server, job Job, backup BackupInfo, opts RestoreOptions) error
}

var registry = map[string]Provider{}

func Register(p Provider) {
	registry[p.Name()] = p
}

func Get(name string) (Provider, error) {
	// normalize postgres alias
	if name == "postgres" {
		name = "postgresql"
	}
	p, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("unknown provider %q", name)
	}
	return p, nil
}

func Types() []string {
	out := []string{}
	for k := range registry {
		out = append(out, k)
	}
	return out
}
