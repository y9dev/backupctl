package retention

import (
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

// NowFunc returns the current time for retention decisions.
// Overridden in tests to freeze/drive time deterministically.
var NowFunc = func() time.Time { return time.Now().UTC() }

// Select determines protected backups (GFS rotation).
//
//   - Hourly: Count newest backups within Window.
//   - Weekly: up to Count backups within Window, newest one per calendar
//     day (UTC), skipping hourly-kept ones — the "daily" spread.
//   - Monthly: up to Count backups within Window, newest one per calendar
//     month (UTC), skipping hourly- and weekly-kept ones.
//
// The union is kept. Plain "N newest in window" per tier would make
// weekly/monthly dead config whenever their counts are below the hourly
// count (they'd only re-protect the same newest backups); bucketing is
// what actually preserves daily/monthly representatives.
func Select(backups []models.Backup, p Policy, now time.Time) (keep map[int64]bool) {
	keep = map[int64]bool{}
	if len(backups) == 0 {
		return keep
	}
	sorted := append([]models.Backup(nil), backups...)
	// Newest-first; ID desc breaks CreatedAt ties deterministically
	// (sort.Slice alone is unstable for equal timestamps).
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].CreatedAt.Equal(sorted[j].CreatedAt) {
			return sorted[i].ID > sorted[j].ID
		}
		return sorted[i].CreatedAt.After(sorted[j].CreatedAt)
	})

	inWindow := func(b models.Backup, window time.Duration) bool {
		return now.Sub(b.CreatedAt) <= window
	}

	// Tier 1: N newest within window.
	if p.Hourly.Count > 0 {
		n := 0
		for _, b := range sorted {
			if inWindow(b, p.Hourly.Window) {
				if n < p.Hourly.Count {
					keep[b.ID] = true
					n++
				}
			}
		}
	}

	// Tiers 2-3: newest per time bucket within window, skipping kept.
	dayKey := func(b models.Backup) string {
		t := b.CreatedAt.UTC()
		return t.Format("2006-01-02")
	}
	monthKey := func(b models.Backup) string {
		t := b.CreatedAt.UTC()
		return t.Format("2006-01")
	}
	perBucket := func(window time.Duration, count int, key func(models.Backup) string) {
		if count <= 0 {
			return
		}
		seen := map[string]bool{}
		buckets := 0
		for _, b := range sorted {
			if keep[b.ID] || !inWindow(b, window) {
				continue
			}
			k := key(b)
			if seen[k] {
				continue
			}
			seen[k] = true
			keep[b.ID] = true
			buckets++
			if buckets >= count {
				break
			}
		}
	}
	perBucket(p.Weekly.Window, p.Weekly.Count, dayKey)
	perBucket(p.Monthly.Window, p.Monthly.Count, monthKey)
	// Edge: if total backups fewer than protection, Select naturally keeps subset;
	// but always keep the newest one to avoid deleting everything.
	if len(keep) == 0 && len(sorted) > 0 {
		keep[sorted[0].ID] = true
	}
	return keep
}

// Run applies retention for a job: deletes unprotected files, updates DB.
// Time source is NowFunc (mockable in tests). Prefer RunAt for explicit time.
func Run(db *database.DB, jobID int64, p Policy, log *slog.Logger) (Result, error) {
	return RunAt(db, jobID, p, NowFunc(), log)
}

// RunAt is Run with explicit now (deterministic rotation under time mocks).
func RunAt(db *database.DB, jobID int64, p Policy, now time.Time, log *slog.Logger) (Result, error) {
	var res Result
	backups, err := db.ListBackups(jobID, 0)
	if err != nil {
		return res, err
	}
	keep := Select(backups, p, now)
	for _, b := range backups {
		if keep[b.ID] {
			res.Kept = append(res.Kept, b)
			continue
		}
		// safe deletion: remove file first
		if err := os.Remove(b.Path); err != nil && !os.IsNotExist(err) {
			if log != nil {
				log.Warn("retention remove failed", "path", b.Path, "error", err)
			}
			// do not delete DB record
			res.Kept = append(res.Kept, b)
			continue
		}
		if err := db.DeleteBackup(b.ID); err != nil {
			if log != nil {
				log.Warn("retention db delete failed", "id", b.ID, "error", err)
			}
			res.Kept = append(res.Kept, b)
			continue
		}
		res.Deleted = append(res.Deleted, b)
	}
	if log != nil {
		log.Info("retention completed", "job_id", jobID, "kept", len(res.Kept), "deleted", len(res.Deleted))
	}
	return res, nil
}
