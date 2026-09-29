package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"backupctl/internal/backup"
	"backupctl/internal/config"
	"backupctl/internal/database"
	"backupctl/internal/models"
	"backupctl/internal/retention"
	"backupctl/internal/storage"

	"github.com/robfig/cron/v3"
)

type Scheduler struct {
	cfg *config.Config
	db  *database.DB
	log *slog.Logger

	sem     chan struct{}
	mu      sync.Mutex
	running map[int64]bool
	cron    *cron.Cron
}

func New(cfg *config.Config, db *database.DB, log *slog.Logger) *Scheduler {
	n := cfg.Scheduler.MaxParallelJobs
	if n <= 0 {
		n = 4
	}
	loc := time.Local
	if cfg.Timezone != "" {
		if l, err := time.LoadLocation(cfg.Timezone); err == nil {
			loc = l
		}
	}
	return &Scheduler{
		cfg: cfg, db: db, log: log,
		sem:     make(chan struct{}, n),
		running: map[int64]bool{},
		cron:    cron.New(cron.WithLocation(loc)),
	}
}

func PidPath() string {
	if v := os.Getenv("BACKUPCTL_PID"); v != "" {
		return v
	}
	return "/var/run/backupctl.pid"
}

func AcquireLock() (release func(), err error) {
	p := PidPath()
	if data, err := os.ReadFile(p); err == nil {
		var pid int
		fmt.Sscanf(string(data), "%d", &pid)
		if pid > 0 && pidAlive(pid) {
			return nil, fmt.Errorf("another backupctl daemon is already running (pid %d)", pid)
		}
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(p, []byte(fmt.Sprintf("%d", os.Getpid())), 0o644); err != nil {
		return nil, err
	}
	return func() { os.Remove(p) }, nil
}

// Recover marks stale running runs and cleans tmp files.
func (s *Scheduler) Recover() error {
	n, err := s.db.MarkStaleRunning()
	if err != nil {
		return err
	}
	if n > 0 {
		s.log.Warn("marked stale running jobs as failed", "count", n)
	}
	removed, _ := storage.CleanupTmp(s.cfg.Storage.Path)
	if removed > 0 {
		s.log.Info("cleaned tmp files", "count", removed)
	}
	return nil
}

func (s *Scheduler) Start(ctx context.Context) error {
	if err := s.Recover(); err != nil {
		return err
	}
	// schedule all enabled jobs from DB (synced from YAML at startup by caller)
	jobs, err := s.db.ListJobs(0)
	if err != nil {
		return err
	}
	for _, j := range jobs {
		if !j.Enabled {
			continue
		}
		srv, err := s.db.GetServerByID(j.ServerID)
		if err != nil || !srv.Enabled {
			continue
		}
		j := j
		if _, err := cron.ParseStandard(j.Schedule); err != nil {
			s.log.Warn("skip job with invalid schedule", "job", j.Name, "error", err)
			continue
		}
		_, err = s.cron.AddFunc(j.Schedule, func() {
			_ = s.RunJob(context.Background(), j.ID)
		})
		if err != nil {
			s.log.Warn("schedule failed", "job", j.Name, "error", err)
		}
	}
	s.cron.Start()
	<-ctx.Done()
	s.cron.Stop()
	return nil
}

// RunJob executes one job with overlap protection, timeout, retry, retention.
func (s *Scheduler) RunJob(ctx context.Context, jobID int64) error {
	s.mu.Lock()
	if s.running[jobID] {
		s.mu.Unlock()
		s.log.Warn("job already running, skip", "job_id", jobID)
		return fmt.Errorf("job %d already running", jobID)
	}
	s.running[jobID] = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.running, jobID)
		s.mu.Unlock()
	}()

	// global parallelism limit
	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	case <-ctx.Done():
		return ctx.Err()
	}

	job, err := s.db.GetJobByID(jobID)
	if err != nil {
		return err
	}
	srv, err := s.db.GetServerByID(job.ServerID)
	if err != nil {
		return err
	}
	// timeout
	timeout := 2 * time.Hour
	if job.Timeout != "" {
		if d, err := time.ParseDuration(job.Timeout); err == nil {
			timeout = d
		}
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	s.log.Info("backup started", "server", srv.Name, "job", job.Name)
	runID, err := s.db.CreateRun(job.ID, srv.ID)
	if err != nil {
		return err
	}
	// disk space check
	if s.cfg.Storage.MinFreeBytes > 0 {
		if free, err := freeSpace(s.cfg.Storage.Path); err == nil && free < uint64(s.cfg.Storage.MinFreeBytes) {
			msg := fmt.Sprintf("insufficient disk space: free %d need %d", free, s.cfg.Storage.MinFreeBytes)
			s.log.Warn(msg, "server", srv.Name, "job", job.Name)
			_ = s.db.FinishRun(runID, models.RunFailed, "", 0, msg)
			return fmt.Errorf("%s", msg)
		}
	}

	p, err := backup.Get(job.Type)
	if err != nil {
		_ = s.db.FinishRun(runID, models.RunFailed, "", 0, err.Error())
		return err
	}

	dest := destinationPath(s.cfg.Storage.Path, srv.Name, job)
	start := time.Now()

	var res backup.BackupResult
	var runErr error
	attempts := s.cfg.Retry.Attempts
	if attempts < 1 {
		attempts = 1
	}
	for a := 1; a <= attempts; a++ {
		res, runErr = p.Backup(ctx, srv, job, dest)
		if runErr == nil {
			break
		}
		if ctx.Err() != nil {
			break
		}
		if !retryable(runErr) || a == attempts {
			break
		}
		s.log.Warn("backup attempt failed, retrying", "server", srv.Name, "job", job.Name, "attempt", a, "error", runErr)
		select {
		case <-time.After(s.cfg.Retry.DelayDur):
		case <-ctx.Done():
			break
		}
	}
	dur := time.Since(start)
	if runErr != nil {
		status := models.RunFailed
		if ctx.Err() == context.DeadlineExceeded {
			status = models.RunCancelled
		}
		msg := fmt.Sprintf("failed to backup job %q on server %q: %v", job.Name, srv.Name, runErr)
		s.log.Error("backup failed", "server", srv.Name, "job", job.Name, "error", runErr)
		_ = s.db.FinishRun(runID, status, "", 0, msg)
		return fmt.Errorf("%s", msg)
	}
	// record
	if _, err := s.db.AddBackup(job.ID, srv.ID, res.Path, res.Size, res.Checksum, res.CreatedAt); err != nil {
		s.log.Error("add backup record failed", "error", err)
	}
	_ = s.db.FinishRun(runID, models.RunSuccess, res.Path, res.Size, "")
	s.log.Info("backup completed", "server", srv.Name, "job", job.Name, "size", res.Size, "duration", dur.String())

	// retention after success
	pol := retention.Policy{
		Hourly:  retention.Rule{Count: s.cfg.Retention.Hourly.Count, Window: s.cfg.Retention.Hourly.Dur},
		Weekly:  retention.Rule{Count: s.cfg.Retention.Weekly.Count, Window: s.cfg.Retention.Weekly.Dur},
		Monthly: retention.Rule{Count: s.cfg.Retention.Monthly.Count, Window: s.cfg.Retention.Monthly.Dur},
	}
	if _, err := retention.Run(s.db, job.ID, pol, s.log); err != nil {
		s.log.Warn("retention failed", "job", job.Name, "error", err)
	}
	return nil
}

func destinationPath(base, server string, job models.BackupJob) string {
	ts := storage.Timestamp(time.Now().UTC())
	sub, ext := jobSubpath(job)
	dir := storage.JobDir(base, server, sub)
	return dir + "/" + ts + ext
}

// jobSubpath derives storage subdir + extension from job config.
func jobSubpath(job models.BackupJob) (string, string) {
	switch job.Type {
	case "directory", "command":
		sub := storage.Sanitize(job.Name)
		ext := ".tar.zst"
		if job.Type == "command" {
			if out, ok := job.Config["output"].(map[string]any); ok {
				if fn, _ := out["filename"].(string); fn != "" {
					// keep sub as job name, filename not needed since timestamp names file
					_ = fn
				}
			}
			ext = ".bin"
			if t, _ := job.Config["output"].(map[string]any); t != nil {
				if typ, _ := t["type"].(string); typ == "archive" {
					ext = ".tar.zst"
				}
			}
		}
		return sub, ext
	case "postgresql", "postgres":
		db := "db"
		if v, _ := job.Config["database"].(string); v != "" {
			db = v
		} else if conn, ok := job.Config["connection"].(map[string]any); ok {
			if v, _ := conn["database"].(string); v != "" {
				db = v
			}
		}
		return storage.Sanitize(job.Name) + "/" + storage.Sanitize(db), ".dump"
	default:
		return storage.Sanitize(job.Name), ".bin"
	}
}

func retryable(err error) bool {
	s := err.Error()
	for _, kw := range []string{"connection refused", "timeout", "network", "EOF", "broken pipe", "ssh", "dial", "temporary", "i/o"} {
		if containsFold(s, kw) {
			return true
		}
	}
	// never retry config errors
	for _, kw := range []string{"invalid", "permission denied", "password", "source is required", "unknown"} {
		if containsFold(s, kw) {
			return false
		}
	}
	return false
}

func containsFold(s, sub string) bool {
	if len(s) < len(sub) {
		return false
	}
	ls := toLower(s)
	lsub := toLower(sub)
	for i := 0; i+len(lsub) <= len(ls); i++ {
		if ls[i:i+len(lsub)] == lsub {
			return true
		}
	}
	return false
}

func toLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}
