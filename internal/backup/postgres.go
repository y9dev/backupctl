package backup

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"backupctl/internal/secrets"
	"backupctl/internal/sshclient"
	"backupctl/internal/storage"

	"golang.org/x/crypto/ssh"
)

func streamFileToRemoteTar(ctx context.Context, client *ssh.Client, localPath, remoteDest string) error {
	f, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer f.Close()
	sess, err := client.NewSession()
	if err != nil {
		return err
	}
	defer sess.Close()
	stdin, err := sess.StdinPipe()
	if err != nil {
		return err
	}
	// remote: tar --use-compress-program=unzstd -x -C dest  (fallback: zstd -d | tar -x)
	// Use sh -c with escaped dest only. dest is escaped via single quotes.
	cmd := fmt.Sprintf("mkdir -p %s && (zstd -d -c | tar -x -C %s)", quote(dest(remoteDest)), quote(dest(remoteDest)))
	_ = cmd
	// Simpler: pipe through zstd locally? No: file is already zstd. Decompress locally then send tar stream.
	if err := sess.Start(fmt.Sprintf("tar -x -C %s", quote(remoteDest))); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() {
		defer stdin.Close()
		done <- decompressZstdToWriter(f, stdin)
	}()
	// wait for copy then wait session
	select {
	case <-ctx.Done():
		sess.Signal(ssh.SIGKILL)
		return ctx.Err()
	case err := <-done:
		if err != nil {
			return err
		}
		return sess.Wait()
	}
}

func dest(s string) string { return s }

func quote(s string) string {
	out := "'"
	for _, r := range s {
		if r == '\'' {
			out += `'"'"'`
		} else {
			out += string(r)
		}
	}
	return out + "'"
}

func getSecret(cfg map[string]any) string {
	name, _ := cfg["password_secret"].(string)
	if name == "" {
		if conn, ok := cfg["connection"].(map[string]any); ok {
			name, _ = conn["password_secret"].(string)
		}
	}
	if name == "" {
		if p, ok := cfg["password"].(string); ok {
			return p // legacy fallback (not recommended)
		}
		return ""
	}
	m, _ := secrets.Load("")
	if m == nil {
		return ""
	}
	return m[name]
}

// PostgresProvider

type PostgresProvider struct{}

func init() { Register(PostgresProvider{}) }

func (PostgresProvider) Name() string { return "postgresql" }

func pgCfg(job Job) (host, port, db, user, pass string) {
	s := pgSettingsFrom(job)
	return s.Host, s.Port, s.Database, s.Username, s.Password
}

// pgSettings описывает подключение к PostgreSQL, включая опциональный
// режим работы через Docker-контейнер на удалённом хосте.
type pgSettings struct {
	Host      string
	Port      string
	Database  string
	Username  string
	Password  string
	Container string // имя Docker-контейнера на удалённом хосте; пусто = pg_dump напрямую на хосте
	DockerBin string // путь к docker на удалённом хосте, по умолчанию "docker"
}

func pgSettingsFrom(job Job) pgSettings {
	m := job.Config
	var s pgSettings
	if conn, ok := m["connection"].(map[string]any); ok {
		s.Host, _ = conn["host"].(string)
		s.Port, _ = conn["port"].(string)
		if s.Port == "" {
			if pf, ok := conn["port"].(float64); ok {
				s.Port = fmt.Sprintf("%d", int(pf))
			}
		}
		s.Database, _ = conn["database"].(string)
		s.Username, _ = conn["username"].(string)
		s.Container, _ = conn["container"].(string)
		if s.Container == "" {
			s.Container, _ = conn["docker_container"].(string)
		}
		s.DockerBin, _ = conn["docker"].(string)
	}
	if s.Host == "" {
		s.Host, _ = m["host"].(string)
	}
	if s.Host == "" {
		s.Host = "127.0.0.1"
	}
	if s.Port == "" {
		if p, ok := m["port"].(string); ok && p != "" {
			s.Port = p
		} else if pf, ok := m["port"].(float64); ok {
			s.Port = fmt.Sprintf("%d", int(pf))
		} else {
			s.Port = "5432"
		}
	}
	if s.Database == "" {
		s.Database, _ = m["database"].(string)
	}
	if s.Username == "" {
		s.Username, _ = m["username"].(string)
	}
	if s.Container == "" {
		s.Container, _ = m["container"].(string)
	}
	if s.Container == "" {
		s.Container, _ = m["docker_container"].(string)
	}
	if s.DockerBin == "" {
		s.DockerBin, _ = m["docker"].(string)
	}
	if s.DockerBin == "" {
		s.DockerBin = "docker"
	}
	s.Password = getSecret(m)
	return s
}

// buildDumpArgv строит команду удалённого pg_dump.
// Без container: [env PGPASSWORD=...] pg_dump ...
// С container: docker exec [-e PGPASSWORD=...] -i <container> pg_dump ...
func buildDumpArgv(s pgSettings) []string {
	dump := []string{"pg_dump", "-h", s.Host, "-p", s.Port, "-U", s.Username, "-Fc", s.Database}
	if s.Container == "" {
		if s.Password != "" {
			return append([]string{"env", "PGPASSWORD=" + s.Password}, dump...)
		}
		return dump
	}
	argv := []string{s.DockerBin, "exec"}
	if s.Password != "" {
		argv = append(argv, "-e", "PGPASSWORD="+s.Password)
	}
	argv = append(argv, "-i", s.Container)
	return append(argv, dump...)
}

// buildRestoreArgv строит команду удалённого pg_restore (дамп подаётся на stdin).
func buildRestoreArgv(s pgSettings, db string) []string {
	restore := []string{"pg_restore", "-h", s.Host, "-p", s.Port, "-U", s.Username, "-d", db, "--clean", "--if-exists"}
	if s.Container == "" {
		if s.Password != "" {
			return append([]string{"env", "PGPASSWORD=" + s.Password}, restore...)
		}
		return restore
	}
	argv := []string{s.DockerBin, "exec"}
	if s.Password != "" {
		argv = append(argv, "-e", "PGPASSWORD="+s.Password)
	}
	argv = append(argv, "-i", s.Container)
	return append(argv, restore...)
}

func (PostgresProvider) Backup(ctx context.Context, server Server, job Job, destination string) (BackupResult, error) {
	s := pgSettingsFrom(job)
	if s.Database == "" || s.Username == "" {
		return BackupResult{}, fmt.Errorf("postgresql provider: database and username are required")
	}
	client, err := sshclient.Dial(sshclient.ServerInfo{Host: server.Host, Port: server.Port, Username: server.Username, KeyPath: server.SSHKey})
	if err != nil {
		return BackupResult{}, fmt.Errorf("ssh connect: %w", err)
	}
	defer client.Close()

	// При container != "": pg_dump выполняется внутри Docker-контейнера
	// через `docker exec` на удалённом хосте, stdout так же стримится по SSH.
	argv := buildDumpArgv(s)

	final := destination
	if len(final) < 5 || final[len(final)-5:] != ".dump" {
		final += ".dump"
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
	stderr, runErr := sshclient.Run(ctx, client, argv, aw)
	if runErr != nil {
		return BackupResult{}, fmt.Errorf("pg_dump failed: %v: %s", runErr, truncate(stderr, 2000))
	}
	sum, sz, err := aw.Commit()
	if err != nil {
		return BackupResult{}, err
	}
	committed = true
	if sz == 0 {
		os.Remove(final)
		return BackupResult{}, fmt.Errorf("pg_dump produced empty output")
	}
	return BackupResult{Path: final, Size: sz, Checksum: sum, CreatedAt: time.Now().UTC()}, nil
}

func (PostgresProvider) Restore(ctx context.Context, server Server, job Job, backup BackupInfo, opts RestoreOptions) error {
	s := pgSettingsFrom(job)
	db := opts.Database
	if db == "" {
		db = s.Database
	}
	if db == "" {
		return fmt.Errorf("database is required")
	}
	client, err := sshclient.Dial(sshclient.ServerInfo{Host: server.Host, Port: server.Port, Username: server.Username, KeyPath: server.SSHKey})
	if err != nil {
		return err
	}
	defer client.Close()
	f, err := os.Open(backup.Path)
	if err != nil {
		return err
	}
	defer f.Close()
	sess, err := client.NewSession()
	if err != nil {
		return err
	}
	defer sess.Close()
	stdin, err := sess.StdinPipe()
	if err != nil {
		return err
	}
	var argv = buildRestoreArgv(s, db)
	// start remote via escaped command: rebuild using shellEscape-like quoting through Run? Instead run directly:
	// Use sess with env: we need to pass through sshclient escaping; simplest: use sshclient.Run-like start.
	// Build command string manually with quoting.
	_ = argv
	// Start session with quoted argv
	sess.Stdin = nil
	cmdStr := joinQuoted(argv)
	if err := sess.Start(cmdStr); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() {
		defer stdin.Close()
		_, err := io.Copy(stdin, f)
		done <- err
	}()
	select {
	case <-ctx.Done():
		sess.Signal(ssh.SIGKILL)
		return ctx.Err()
	case err := <-done:
		if err != nil {
			return err
		}
		return sess.Wait()
	}
}

func joinQuoted(argv []string) string {
	out := ""
	for i, a := range argv {
		if i > 0 {
			out += " "
		}
		// env VAR=val must not quote the whole; handle VAR= prefix
		if len(a) > 11 && a[:11] == "PGPASSWORD=" {
			out += "PGPASSWORD=" + quote(a[11:])
		} else {
			out += quote(a)
		}
	}
	return out
}
