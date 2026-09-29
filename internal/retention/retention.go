package retention

import (
	"fmt"
	"log/slog"
	"os"
	"sort"
	"time"

	"backupctl/internal/database"
	"backupctl/internal/models"
)

type Rule struct {
	Count  int
	Window time.Duration
}

type Policy struct {
	Hourly  Rule
	Weekly  Rule
	Monthly Rule
}

type Result struct {
	Kept    []models.Backup
	Deleted []models.Backup
}

// Select determines protected backups. Union of rules.
func Select(backups []models.Backup, p Policy, now time.Time) (keep map[int64]bool) {
	keep = map[int64]bool{}
	if len(backups) == 0 {
		return keep
	}
	sorted := append([]models.Backup(nil), backups...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].CreatedAt.After(sorted[j].CreatedAt) })

	apply := func(window time.Duration, count int) {
		if count <= 0 {
			return
		}
		n := 0
		for _, b := range sorted {
			if now.Sub(b.CreatedAt) <= window {
				if n < count {
					keep[b.ID] = true
					n++
				}
			}
		}
	}
	apply(p.Hourly.Window, p.Hourly.Count)
	apply(p.Weekly.Window, p.Weekly.Count)
	apply(p.Monthly.Window, p.Monthly.Count)
	// Edge: if total backups fewer than protection, Select naturally keeps subset;
	// but always keep the newest one to avoid deleting everything.
	if len(keep) == 0 && len(sorted) > 0 {
		keep[sorted[0].ID] = true
	}
	return keep
}

// Run applies retention for a job: deletes unprotected files, updates DB.
func Run(db *database.DB, jobID int64, p Policy, log *slog.Logger) (Result, error) {
	var res Result
	backups, err := db.ListBackups(jobID, 0)
	if err != nil {
		return res, err
	}
	now := time.Now().UTC()
	keep := Select(backups, p, now)
	for _, b := range backups {
		if keep[b.ID] {
			res.Kept = append(res.Kept, b)
			continue
		}
		// safe deletion: remove file first
		if err := os.Remove(b.Path); err != nil && !os.IsNotExist(err) {
			log.Warn("retention remove failed", "path", b.Path, "error", err)
			// do not delete DB record
			res.Kept = append(res.Kept, b)
			continue
		}
		if err := db.DeleteBackup(b.ID); err != nil {
			log.Warn("retention db delete failed", "id", b.ID, "error", err)
			res.Kept = append(res.Kept, b)
			continue
		}
		res.Deleted = append(res.Deleted, b)
	}
	if log != nil {
		log.Info("retention completed", "job_id", jobID, "kept", len(res.Kept), "deleted", len(res.Deleted))
	}
	_ = fmt.Sprint()
	return res, nil
}
