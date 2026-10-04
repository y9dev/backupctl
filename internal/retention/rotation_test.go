package retention

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"backupctl/internal/database"
	"backupctl/internal/models"
)

// Fixed mocked "now" for all rotation tests: 2026-10-03 12:00 UTC.
// Using a frozen timestamp (instead of time.Now()) makes rotation deterministic.
func fixedNow() time.Time {
	return time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
}

func defaultPolicy() Policy {
	return Policy{
		Hourly:  Rule{Count: 10, Window: 12 * time.Hour},
		Weekly:  Rule{Count: 1, Window: 7 * 24 * time.Hour},
		Monthly: Rule{Count: 2, Window: 30 * 24 * time.Hour},
	}
}

func mkHourly(n int, now time.Time) []models.Backup {
	out := make([]models.Backup, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, models.Backup{
			ID:        int64(i + 1),
			CreatedAt: now.Add(-time.Duration(i) * time.Hour),
		})
	}
	return out
}

func keepIDs(keep map[int64]bool) map[int64]bool { return keep }

// 1. Hourly rotation with frozen now: 10 hourly + daily/monthly bucket
// representatives on top (GFS spread, not just the N newest).
func TestRotation_HourlyExactWithFixedNow(t *testing.T) {
	now := fixedNow()
	bs := mkHourly(20, now)
	keep := Select(bs, defaultPolicy(), now)

	// 10 hourly (IDs 1..10, ages 0..9h) + weekly daily rep (ID 11, Oct 3
	// leftover past the hourly count) + monthly rep (ID 12, next Oct
	// leftover). IDs 13..20 are older dups of covered day/month buckets.
	if len(keep) != 12 {
		t.Fatalf("expected exactly 12 kept, got %d: %v", len(keep), keep)
	}
	for i := 1; i <= 12; i++ {
		if !keep[int64(i)] {
			t.Fatalf("backup %d must be kept", i)
		}
	}
	for i := 13; i <= 20; i++ {
		if keep[int64(i)] {
			t.Fatalf("backup %d must be rotated out (deleted)", i)
		}
	}
}

// 2. Window boundary is inclusive (<=): exactly at 12h kept, 12h+1s deleted.
func TestRotation_WindowBoundaryInclusive(t *testing.T) {
	now := fixedNow()
	p := Policy{Hourly: Rule{Count: 10, Window: 12 * time.Hour}}
	bs := []models.Backup{
		{ID: 1, CreatedAt: now},
		{ID: 2, CreatedAt: now.Add(-12 * time.Hour)},
		{ID: 3, CreatedAt: now.Add(-12*time.Hour - time.Second)},
	}
	keep := Select(bs, p, now)
	if !keep[1] || !keep[2] {
		t.Fatalf("backups at now and exactly window edge must be kept: %v", keepIDs(keep))
	}
	if keep[3] {
		t.Fatal("backup 1s outside window must be deleted")
	}
}

// 3. Weekly tier keeps one representative per calendar day beyond the
// hourly window (GFS buckets), up to Count most recent days.
func TestRotation_WeeklyUnionBeyondHourly(t *testing.T) {
	now := fixedNow()
	p := Policy{
		Hourly:  Rule{Count: 1, Window: 12 * time.Hour},
		Weekly:  Rule{Count: 2, Window: 7 * 24 * time.Hour},
		Monthly: Rule{Count: 0, Window: 0},
	}
	bs := []models.Backup{
		{ID: 1, CreatedAt: now.Add(-time.Hour)},      // inside hourly
		{ID: 2, CreatedAt: now.Add(-20 * time.Hour)}, // outside hourly, daily rep day 1
		{ID: 3, CreatedAt: now.Add(-3 * 24 * time.Hour)}, // daily rep day 2
		{ID: 4, CreatedAt: now.Add(-10 * 24 * time.Hour)}, // outside all
	}
	keep := Select(bs, p, now)
	// hourly keeps {1}; weekly keeps newest-per-day for the 2 most recent
	// days with unprotected backups = {2, 3}; union = {1,2,3}.
	if !keep[1] {
		t.Fatal("ID 1 (hourly) must be kept")
	}
	if !keep[2] {
		t.Fatal("ID 2 must be kept as daily representative despite being outside hourly window")
	}
	if !keep[3] {
		t.Fatal("ID 3 must be kept as second daily representative (count=2)")
	}
	if keep[4] {
		t.Fatal("ID 4 outside all windows must be deleted")
	}
}

// 4. Monthly tier: newest one per calendar month, up to Count most recent
// months. Same-month duplicates are pruned even if newer than kept reps
// of other months.
func TestRotation_MonthlyTierExhaustion(t *testing.T) {
	now := fixedNow()
	p := Policy{
		Hourly:  Rule{Count: 1, Window: 12 * time.Hour},
		Weekly:  Rule{Count: 0, Window: 0},
		Monthly: Rule{Count: 2, Window: 30 * 24 * time.Hour},
	}
	bs := []models.Backup{
		{ID: 1, CreatedAt: now.Add(-time.Hour)},
		{ID: 2, CreatedAt: now.Add(-10 * 24 * time.Hour)},
		{ID: 3, CreatedAt: now.Add(-20 * 24 * time.Hour)}, // in window, over count
		{ID: 4, CreatedAt: now.Add(-40 * 24 * time.Hour)}, // outside window
	}
	keep := Select(bs, p, now)
	if !keep[1] || !keep[2] {
		t.Fatalf("IDs 1,2 must be kept, got %v", keepIDs(keep))
	}
	if keep[3] {
		t.Fatal("ID 3 in monthly window but beyond count must be deleted")
	}
	if keep[4] {
		t.Fatal("ID 4 outside monthly window must be deleted")
	}
}

// 5. All backups older than every window: fallback keeps ONLY the newest.
func TestRotation_AllOldFallbackKeepsNewestOnly(t *testing.T) {
	now := fixedNow()
	bs := []models.Backup{
		{ID: 1, CreatedAt: now.Add(-40 * 24 * time.Hour)}, // newest of the old
		{ID: 2, CreatedAt: now.Add(-50 * 24 * time.Hour)},
		{ID: 3, CreatedAt: now.Add(-60 * 24 * time.Hour)},
	}
	keep := Select(bs, defaultPolicy(), now)
	if !keep[1] {
		t.Fatal("newest backup must survive as fallback (never delete everything)")
	}
	if keep[2] || keep[3] {
		t.Fatalf("older backups outside all windows must be deleted, got %v", keepIDs(keep))
	}
}

// 6. Future timestamps are safe: never deleted (negative age <= window).
func TestRotation_FutureTimestampsNeverDeleted(t *testing.T) {
	now := fixedNow()
	p := Policy{Hourly: Rule{Count: 10, Window: 12 * time.Hour}}
	bs := []models.Backup{
		{ID: 1, CreatedAt: now.Add(time.Hour)}, // clock skew / future
		{ID: 2, CreatedAt: now},
	}
	keep := Select(bs, p, now)
	if !keep[1] || !keep[2] {
		t.Fatalf("future and current backups must be kept, got %v", keepIDs(keep))
	}
}

// 7. Disabled tiers (count=0): only fallback newest survives.
func TestRotation_DisabledTiersKeepOnlyNewest(t *testing.T) {
	now := fixedNow()
	p := Policy{
		Hourly:  Rule{Count: 0, Window: 12 * time.Hour},
		Weekly:  Rule{Count: 0, Window: 7 * 24 * time.Hour},
		Monthly: Rule{Count: 0, Window: 30 * 24 * time.Hour},
	}
	bs := []models.Backup{
		{ID: 1, CreatedAt: now},
		{ID: 2, CreatedAt: now.Add(-time.Hour)},
		{ID: 3, CreatedAt: now.Add(-2 * time.Hour)},
	}
	keep := Select(bs, p, now)
	if len(keep) != 1 || !keep[1] {
		t.Fatalf("with all tiers disabled only newest must survive, got %v", keepIDs(keep))
	}
}

// 8. Equal timestamps are deterministic: higher ID (newer record) wins.
func TestRotation_SameTimestampDeterministic(t *testing.T) {
	now := fixedNow()
	p := Policy{Hourly: Rule{Count: 1, Window: 12 * time.Hour}}
	for i := 0; i < 25; i++ {
		bs := []models.Backup{{ID: 1, CreatedAt: now}, {ID: 2, CreatedAt: now}}
		keep := Select(bs, p, now)
		if len(keep) != 1 {
			t.Fatalf("iter %d: count=1 must keep exactly 1, got %v", i, keepIDs(keep))
		}
		if !keep[2] {
			t.Fatalf("iter %d: tie must resolve deterministically to higher ID, got %v", i, keepIDs(keep))
		}
	}
}

// 12. GFS spread: weekly/monthly tiers keep one representative per
// calendar day/month — NOT just the next-newest backups.
// With half-hourly backups covering only the last 4.5h, daily and monthly
// representatives must survive instead of everything older being pruned.
func TestRotation_GFSSpreadKeepsDailyAndMonthly(t *testing.T) {
	now := fixedNow() // 2026-10-03 12:00 UTC
	bs := []models.Backup{}
	add := func(id int64, tm time.Time) {
		bs = append(bs, models.Backup{ID: id, CreatedAt: tm})
	}
	// 10 half-hourly backups, all today (ages 0..4.5h).
	for i := 0; i < 10; i++ {
		add(int64(i+1), now.Add(-time.Duration(i)*30*time.Minute))
	}
	add(11, time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)) // yesterday
	add(12, time.Date(2026, 10, 1, 18, 0, 0, 0, time.UTC)) // same day, newest
	add(13, time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)) // same day, older dup
	add(14, time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC))
	add(15, time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC))
	add(16, time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC))
	add(17, time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)) // outside 30d window

	keep := Select(bs, defaultPolicy(), now)

	// 10 hourly + 1 daily (Oct 2) + 2 monthly (Oct 1, Sep 30).
	if len(keep) != 13 {
		t.Fatalf("expected 13 kept (10 hourly + 1 daily + 2 monthly), got %d: %v", len(keep), keepIDs(keep))
	}
	for i := 1; i <= 10; i++ {
		if !keep[int64(i)] {
			t.Fatalf("hourly backup %d must be kept", i)
		}
	}
	for _, id := range []int64{11, 12, 14} {
		if !keep[id] {
			t.Fatalf("representative backup %d must be kept (daily/monthly spread)", id)
		}
	}
	// Same-day duplicate deleted even though NEWER than kept monthly rep 14:
	// bucketing, not prefix-keeping.
	if keep[13] {
		t.Fatal("backup 13 (older dup of Oct 1) must be deleted, newest-per-day wins")
	}
	for _, id := range []int64{15, 16, 17} {
		if keep[id] {
			t.Fatalf("backup %d must be deleted (month quota filled / outside window)", id)
		}
	}
}

// --- helpers for end-to-end rotation (DB + files) under mocked time ---

func openTestDB(t *testing.T) *database.DB {
	t.Helper()
	db, err := database.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func seedBackupsWithFiles(t *testing.T, db *database.DB, dir string, jobID, serverID int64, now time.Time, ages []time.Duration) []models.Backup {
	t.Helper()
	var out []models.Backup
	for i, age := range ages {
		p := filepath.Join(dir, "backup-seed-"+itoa(i)+".bak")
		if err := os.WriteFile(p, []byte("data"), 0o600); err != nil {
			t.Fatal(err)
		}
		created := now.Add(-age)
		id, err := db.AddBackup(jobID, serverID, p, 4, "", created)
		if err != nil {
			t.Fatal(err)
		}
		b, err := db.GetBackup(id)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, b)
	}
	return out
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(b[pos:])
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// 9. End-to-end: RunAt with mocked now deletes files AND db rows exactly per policy.
func TestRunAt_EndToEndDeletesFilesAndRows(t *testing.T) {
	now := fixedNow()
	db := openTestDB(t)
	dir := t.TempDir()
	const jobID, serverID = int64(7), int64(3)

	// hourly{2, 12h}: f1,f2 survive; f3 in-window but over count -> pruned;
	// f4,f5 outside window -> pruned.
	seeded := seedBackupsWithFiles(t, db, dir, jobID, serverID, now, []time.Duration{
		time.Hour, 2 * time.Hour, 3 * time.Hour, 20 * time.Hour, 40 * time.Hour,
	})
	p := Policy{Hourly: Rule{Count: 2, Window: 12 * time.Hour}}

	res, err := RunAt(db, jobID, p, now, nil) // nil logger must not panic
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Kept) != 2 || len(res.Deleted) != 3 {
		t.Fatalf("expected kept=2 deleted=3, got kept=%d deleted=%d", len(res.Kept), len(res.Deleted))
	}
	// Newest two (by CreatedAt) survive on disk + in DB.
	for _, b := range res.Kept {
		if !fileExists(b.Path) {
			t.Fatalf("kept file must remain on disk: %s", b.Path)
		}
	}
	for _, b := range res.Deleted {
		if fileExists(b.Path) {
			t.Fatalf("deleted file must be removed from disk: %s", b.Path)
		}
		if _, err := db.GetBackup(b.ID); err == nil {
			t.Fatalf("deleted backup %d must be removed from DB", b.ID)
		}
	}
	remaining, err := db.ListBackups(jobID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 2 {
		t.Fatalf("DB must contain exactly 2 rows after rotation, got %d", len(remaining))
	}
	// Verify the survivors are the two newest seeds.
	keptSet := map[int64]bool{}
	for _, b := range remaining {
		keptSet[b.ID] = true
	}
	if !keptSet[seeded[0].ID] || !keptSet[seeded[1].ID] {
		t.Fatal("rotation must keep the two newest backups")
	}
}

// 10. Run() honours NowFunc mock (wall-clock decoupled).
func TestRun_RespectsNowFuncMock(t *testing.T) {
	now := fixedNow()
	old := NowFunc
	NowFunc = func() time.Time { return now }
	defer func() { NowFunc = old }()

	db := openTestDB(t)
	dir := t.TempDir()
	const jobID, serverID = int64(9), int64(9)
	seedBackupsWithFiles(t, db, dir, jobID, serverID, now, []time.Duration{
		time.Hour, 2 * time.Hour, 30 * time.Hour,
	})
	p := Policy{Hourly: Rule{Count: 1, Window: 12 * time.Hour}}

	logger := slog.New(slog.NewTextHandler(testDiscard{}, nil))
	res, err := Run(db, jobID, p, logger)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Kept) != 1 || len(res.Deleted) != 2 {
		t.Fatalf("mocked Run: expected kept=1 deleted=2, got kept=%d deleted=%d", len(res.Kept), len(res.Deleted))
	}
	remaining, _ := db.ListBackups(jobID, 0)
	if len(remaining) != 1 {
		t.Fatalf("mocked Run: DB must hold 1 row, got %d", len(remaining))
	}
	if !remaining[0].CreatedAt.Equal(now.Add(-time.Hour)) {
		t.Fatalf("mocked Run: survivor must be the 1h-old backup, got %v", remaining[0].CreatedAt)
	}
}

type testDiscard struct{}

func (testDiscard) Write(p []byte) (int, error) { return len(p), nil }

// 11. Time travel: advancing mocked now prunes backups that aged out.
func TestRotation_TimeTravelAgesOut(t *testing.T) {
	t0 := fixedNow()
	db := openTestDB(t)
	dir := t.TempDir()
	const jobID, serverID = int64(11), int64(11)
	p := Policy{Hourly: Rule{Count: 2, Window: 12 * time.Hour}}

	// Day 0: two backups, both fresh -> both kept.
	seedBackupsWithFiles(t, db, dir, jobID, serverID, t0, []time.Duration{0, time.Hour})
	if res, err := RunAt(db, jobID, p, t0.Add(time.Hour), nil); err != nil {
		t.Fatal(err)
	} else if len(res.Kept) != 2 || len(res.Deleted) != 0 {
		t.Fatalf("t0: expected 2 kept 0 deleted, got %d/%d", len(res.Kept), len(res.Deleted))
	}

	// +48h: one new backup arrives; the two old ones (47-48h) aged out of 12h window.
	t1 := t0.Add(48 * time.Hour)
	newPath := filepath.Join(dir, "backup-new.bak")
	if err := os.WriteFile(newPath, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AddBackup(jobID, serverID, newPath, 3, "", t1); err != nil {
		t.Fatal(err)
	}
	res, err := RunAt(db, jobID, p, t1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Kept) != 1 || len(res.Deleted) != 2 {
		t.Fatalf("t1: expected 1 kept 2 deleted after 48h travel, got %d/%d", len(res.Kept), len(res.Deleted))
	}
	remaining, _ := db.ListBackups(jobID, 0)
	if len(remaining) != 1 || !remaining[0].CreatedAt.Equal(t1) {
		t.Fatal("after time travel only the fresh backup must survive")
	}
	if !fileExists(newPath) {
		t.Fatal("fresh backup file must remain")
	}
}
