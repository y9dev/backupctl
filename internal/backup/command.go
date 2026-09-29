package backup

import (
	"context"
	"fmt"
	"os"
	"time"

	"backupctl/internal/sshclient"
	"backupctl/internal/storage"
)

type CommandProvider struct{}

func init() { Register(CommandProvider{}) }

func (CommandProvider) Name() string { return "command" }

func toArgv(v any) []string {
	switch t := v.(type) {
	case []any:
		out := []string{}
		for _, e := range t {
			out = append(out, fmt.Sprintf("%v", e))
		}
		return out
	case []string:
		return t
	case string:
		return []string{"sh", "-c", t}
	}
	return nil
}

func (CommandProvider) Backup(ctx context.Context, server Server, job Job, destination string) (BackupResult, error) {
	argv := toArgv(job.Config["command"])
	if len(argv) == 0 {
		return BackupResult{}, fmt.Errorf("command provider: command is required")
	}
	timeout := 0 * time.Second
	if ts, _ := job.Config["timeout"].(string); ts != "" {
		if d, err := time.ParseDuration(ts); err == nil {
			timeout = d
		}
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	filename := "output.bin"
	if out, ok := job.Config["output"].(map[string]any); ok {
		if fn, _ := out["filename"].(string); fn != "" {
			filename = storage.Sanitize(fn)
		}
	}
	_ = filename
	client, err := sshclient.Dial(sshclient.ServerInfo{Host: server.Host, Port: server.Port, Username: server.Username, KeyPath: server.SSHKey})
	if err != nil {
		return BackupResult{}, fmt.Errorf("ssh connect: %w", err)
	}
	defer client.Close()

	final := destination
	aw, err := storage.NewAtomicWriter(final)
	if err != nil {
		return BackupResult{}, err
	}
	committed := false
	defer func() {
		if !committed {
			aw.Abort()
		}
	}()
	stderr, runErr := sshclient.Run(ctx, client, argv, aw)
	if runErr != nil {
		return BackupResult{}, fmt.Errorf("remote command failed: %v: %s", runErr, truncate(stderr, 2000))
	}
	sum, sz, err := aw.Commit()
	if err != nil {
		return BackupResult{}, err
	}
	committed = true
	if sz == 0 {
		os.Remove(final)
		return BackupResult{}, fmt.Errorf("command produced empty output")
	}
	return BackupResult{Path: final, Size: sz, Checksum: sum, CreatedAt: time.Now().UTC()}, nil
}

func (CommandProvider) Restore(ctx context.Context, server Server, job Job, backup BackupInfo, opts RestoreOptions) error {
	return fmt.Errorf("command provider restore not supported: restore the file manually via SSH")
}
