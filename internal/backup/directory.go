package backup

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"backupctl/internal/sshclient"
	"backupctl/internal/storage"

	"github.com/klauspost/compress/zstd"
)

type DirectoryProvider struct{}

func init() { Register(DirectoryProvider{}) }

func (DirectoryProvider) Name() string { return "directory" }

func strCfg(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k].(string); ok && v != "" {
			return v
		}
	}
	// nested connection map support
	if conn, ok := m["connection"].(map[string]any); ok {
		for _, k := range keys {
			if v, ok := conn[k].(string); ok && v != "" {
				return v
			}
		}
	}
	return ""
}

func (DirectoryProvider) Backup(ctx context.Context, server Server, job Job, destination string) (BackupResult, error) {
	source := strCfg(job.Config, "source", "path")
	if source == "" {
		return BackupResult{}, fmt.Errorf("directory provider: source is required")
	}
	level := 3
	if comp, ok := job.Config["compression"].(map[string]any); ok {
		if l, ok := comp["level"].(int); ok {
			level = l
		} else if lf, ok := comp["level"].(float64); ok {
			level = int(lf)
		}
	}
	excludes := []string{}
	if ex, ok := job.Config["exclude"].([]any); ok {
		for _, e := range ex {
			if s, ok := e.(string); ok {
				excludes = append(excludes, s)
			}
		}
	}
	follow := false
	if f, ok := job.Config["follow_symlinks"].(bool); ok {
		follow = f
	}

	client, err := sshclient.Dial(sshclient.ServerInfo{Host: server.Host, Port: server.Port, Username: server.Username, KeyPath: server.SSHKey})
	if err != nil {
		return BackupResult{}, fmt.Errorf("ssh connect: %w", err)
	}
	defer client.Close()

	// Build remote tar command: tar -c [opts] -C / source...
	// Use tar to stdout. Avoid shell interpolation: pass argv, sshclient escapes.
	argv := []string{"tar", "-c", "--posix"}
	if follow {
		argv = append(argv, "-h")
	}
	for _, e := range excludes {
		argv = append(argv, "--exclude="+e)
	}
	argv = append(argv, source)

	final := destination
	if !strings.HasSuffix(final, ".tar.zst") {
		final += ".tar.zst"
	}

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

	enc, err := zstd.NewWriter(aw, zstd.WithEncoderLevel(zstd.EncoderLevelFromZstd(level)))
	if err != nil {
		return BackupResult{}, err
	}
	stderr, runErr := sshclient.Run(ctx, client, argv, enc)
	_ = enc.Close()
	if runErr != nil {
		// include stderr but never secrets
		return BackupResult{}, fmt.Errorf("remote tar failed: %v: %s", runErr, truncate(stderr, 2000))
	}
	// empty check: tar of empty dir still produces headers, but zero-byte means failure
	checksum, size, err := func() (string, int64, error) {
		// need checksum from aw before commit: commit computes it
		return "", 0, nil
	}()
	_ = checksum
	_ = size
	sum, sz, err := aw.Commit()
	if err != nil {
		return BackupResult{}, err
	}
	committed = true
	if sz == 0 {
		os.Remove(final)
		return BackupResult{}, fmt.Errorf("backup is empty")
	}
	return BackupResult{Path: final, Size: sz, Checksum: sum, CreatedAt: time.Now().UTC()}, nil
}

func (DirectoryProvider) Restore(ctx context.Context, server Server, job Job, backup BackupInfo, opts RestoreOptions) error {
	dest := opts.Destination
	if dest == "" {
		dest = strCfg(job.Config, "source", "path")
	}
	if dest == "" {
		return fmt.Errorf("restore destination is required")
	}
	client, err := sshclient.Dial(sshclient.ServerInfo{Host: server.Host, Port: server.Port, Username: server.Username, KeyPath: server.SSHKey})
	if err != nil {
		return err
	}
	defer client.Close()
	// ensure remote dir exists
	if _, _, err := sshclient.RunWithCombined(ctx, client, []string{"mkdir", "-p", dest}); err != nil {
		return fmt.Errorf("mkdir remote: %w", err)
	}
	// stream local file -> remote tar -x : use ssh session stdin via raw client
	return streamFileToRemoteTar(ctx, client, backup.Path, dest)
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
