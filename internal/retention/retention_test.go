package retention

import (
	"testing"
	"time"

	"backupctl/internal/models"
)

func mkBackups(n int, step time.Duration, now time.Time) []models.Backup {
	out := []models.Backup{}
	for i := 0; i < n; i++ {
		out = append(out, models.Backup{ID: int64(i + 1), CreatedAt: now.Add(-time.Duration(i) * step)})
	}
	return out
}

func TestHourlyKeeps10(t *testing.T) {
	now := time.Now().UTC()
	bs := mkBackups(20, time.Hour, now)
	p := Policy{Hourly: Rule{Count: 10, Window: 12 * time.Hour}, Weekly: Rule{Count: 1, Window: 7 * 24 * time.Hour}, Monthly: Rule{Count: 2, Window: 30 * 24 * time.Hour}}
	keep := Select(bs, p, now)
	// hourly keeps 10 newest within 12h + weekly/monthly overlap may add more
	if len(keep) < 10 {
		t.Fatalf("expected >=10 kept, got %d", len(keep))
	}
	// newest 10 must be kept
	for i := 1; i <= 10; i++ {
		if !keep[int64(i)] {
			t.Fatalf("backup %d should be kept", i)
		}
	}
}

func TestOverlapUnion(t *testing.T) {
	now := time.Now().UTC()
	bs := []models.Backup{{ID: 1, CreatedAt: now.Add(-time.Hour)}}
	p := Policy{Hourly: Rule{10, 12 * time.Hour}, Weekly: Rule{1, 7 * 24 * time.Hour}, Monthly: Rule{2, 30 * 24 * time.Hour}}
	keep := Select(bs, p, now)
	if !keep[1] {
		t.Fatal("single backup must be kept (union + newest fallback)")
	}
}

func TestEmpty(t *testing.T) {
	keep := Select(nil, Policy{Hourly: Rule{10, 12 * time.Hour}}, time.Now())
	if len(keep) != 0 {
		t.Fatal("empty should keep nothing")
	}
}

func TestIrregularAndGaps(t *testing.T) {
	now := time.Now().UTC()
	bs := []models.Backup{
		{ID: 1, CreatedAt: now.Add(-30 * time.Minute)},
		{ID: 2, CreatedAt: now.Add(-5 * time.Hour)},
		{ID: 3, CreatedAt: now.Add(-3 * 24 * time.Hour)},
		{ID: 4, CreatedAt: now.Add(-20 * 24 * time.Hour)},
		{ID: 5, CreatedAt: now.Add(-40 * 24 * time.Hour)}, // outside monthly
	}
	p := Policy{Hourly: Rule{10, 12 * time.Hour}, Weekly: Rule{1, 7 * 24 * time.Hour}, Monthly: Rule{2, 30 * 24 * time.Hour}}
	keep := Select(bs, p, now)
	if !keep[1] || !keep[2] {
		t.Fatal("recent hourly should be kept")
	}
	if keep[5] {
		t.Fatal("backup outside all windows should be deleted")
	}
}

func TestSameTimestamps(t *testing.T) {
	now := time.Now().UTC()
	bs := []models.Backup{{ID: 1, CreatedAt: now}, {ID: 2, CreatedAt: now}}
	p := Policy{Hourly: Rule{1, 12 * time.Hour}}
	keep := Select(bs, p, now)
	if len(keep) != 1 {
		t.Fatalf("count=1 should keep exactly 1, got %d", len(keep))
	}
}
