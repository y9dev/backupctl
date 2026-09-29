package restore

import (
	"context"
	"fmt"

	"backupctl/internal/backup"
	"backupctl/internal/database"
)

func RestoreBackup(ctx context.Context, db *database.DB, backupID int64, opts backup.RestoreOptions) error {
	b, err := db.GetBackup(backupID)
	if err != nil {
		return err
	}
	job, err := db.GetJobByID(b.JobID)
	if err != nil {
		return err
	}
	srv, err := db.GetServerByID(b.ServerID)
	if err != nil {
		return err
	}
	p, err := backup.Get(job.Type)
	if err != nil {
		return err
	}
	info := backup.BackupInfo{ID: b.ID, Path: b.Path, CreatedAt: b.CreatedAt, Size: b.Size, Checksum: b.Checksum}
	if err := p.Restore(ctx, srv, job, info, opts); err != nil {
		return fmt.Errorf("failed to restore job %q on server %q: %w", job.Name, srv.Name, err)
	}
	return nil
}
