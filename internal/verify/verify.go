package verify

import (
	"fmt"
	"os"
	"os/exec"

	"backupctl/internal/database"
	"backupctl/internal/storage"
)

type Result struct {
	Exists   bool
	Size     int64
	Expected string
	Actual   string
	OK       bool
	Extra    string
}

func Verify(db *database.DB, backupID int64) (Result, error) {
	var r Result
	b, err := db.GetBackup(backupID)
	if err != nil {
		return r, err
	}
	fi, err := os.Stat(b.Path)
	if err != nil {
		return r, fmt.Errorf("backup file missing %s: %w", b.Path, err)
	}
	r.Exists = true
	r.Size = fi.Size()
	r.Expected = b.Checksum
	sum, _, err := storage.ChecksumFile(b.Path)
	if err != nil {
		return r, err
	}
	r.Actual = sum
	r.OK = (sum == b.Checksum)
	if !r.OK {
		return r, fmt.Errorf("checksum mismatch: expected %s got %s", b.Checksum, sum)
	}
	return r, nil
}

// CheckPostgres runs pg_restore --list to validate readability (optional).
func CheckPostgres(path string) (string, error) {
	pg, err := exec.LookPath("pg_restore")
	if err != nil {
		return "pg_restore not installed, skipped", nil
	}
	out, err := exec.Command(pg, "--list", path).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("pg_restore --list failed: %w: %s", err, string(out))
	}
	return "pg_restore --list OK", nil
}
