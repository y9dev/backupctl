package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"backupctl/internal/backup"
	"backupctl/internal/database"
	"backupctl/internal/logging"
	"backupctl/internal/models"
	"backupctl/internal/restore"
	"backupctl/internal/scheduler"
	"backupctl/internal/storage"
	"backupctl/internal/verify"

	"github.com/spf13/cobra"
)

func runAliasCmd() *cobra.Command {
	c := &cobra.Command{Use: "run [server] [job]", Short: "Run backup (alias for job run)"}
	c.Flags().String("server", "", "server name (run all its jobs)")
	c.RunE = func(cmd *cobra.Command, args []string) error {
		srvFlag, _ := cmd.Flags().GetString("server")
		var srv, job string
		if srvFlag != "" {
			srv = srvFlag
		} else if len(args) >= 1 {
			srv = args[0]
		}
		if len(args) >= 2 {
			job = args[1]
		}
		if srv == "" {
			return fail(1, fmt.Errorf("usage: backupctl run <server> [job] | backupctl run --server <server>"))
		}
		log := logging.New(logLevel)
		cfg, err := loadCfg()
		if err != nil {
			return fail(2, err)
		}
		db, err := openDB(cfg)
		if err != nil {
			return fail(1, err)
		}
		defer db.Close()
		if err := syncConfigToDB(cfg, db); err != nil {
			return fail(2, err)
		}
		s := scheduler.New(cfg, db, log)
		if job != "" {
			jb, _, err := db.GetJob(srv, job)
			if err != nil {
				return fail(1, err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Hour)
			defer cancel()
			if err := s.RunJob(ctx, jb.ID); err != nil {
				return fail(4, err)
			}
			fmt.Println("backup completed")
			return nil
		}
		sv, err := db.GetServerByName(srv)
		if err != nil {
			return fail(1, err)
		}
		jobs, _ := db.ListJobs(sv.ID)
		return runAllJobs(s, jobs)
	}
	return c
}

func runAllJobs(s *scheduler.Scheduler, jobs []models.BackupJob) error {
	failed := 0
	for _, jb := range jobs {
		if !jb.Enabled {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Hour)
		if err := s.RunJob(ctx, jb.ID); err != nil {
			fmt.Fprintf(os.Stderr, "job %s failed: %v\n", jb.Name, err)
			failed++
		}
		cancel()
	}
	if failed > 0 {
		return fail(4, fmt.Errorf("%d job(s) failed", failed))
	}
	fmt.Println("all backups completed")
	return nil
}

func restoreAliasCmd() *cobra.Command {
	c := &cobra.Command{Use: "restore <backup-id>", Short: "Restore backup (alias)"}
	c.Flags().String("dest", "", "restore destination (directory jobs)")
	c.Flags().String("database", "", "target database (postgres jobs)")
	c.Flags().Bool("yes", false, "skip confirmation")
	c.Args = cobra.ExactArgs(1)
	c.RunE = func(cmd *cobra.Command, args []string) error {
		dest, _ := cmd.Flags().GetString("dest")
		dbname, _ := cmd.Flags().GetString("database")
		yes, _ := cmd.Flags().GetBool("yes")
		cfg, err := loadCfg()
		if err != nil {
			return fail(2, err)
		}
		dbh, err := openDB(cfg)
		if err != nil {
			return fail(1, err)
		}
		defer dbh.Close()
		_ = syncConfigToDB(cfg, dbh)
		var id int64
		fmt.Sscanf(args[0], "%d", &id)
		bk, err := dbh.GetBackup(id)
		if err != nil {
			return fail(1, err)
		}
		jb, _ := dbh.GetJobByID(bk.JobID)
		srv, _ := dbh.GetServerByID(bk.ServerID)
		if !yes {
			fmt.Printf("WARNING! You are about to restore:\n\nServer: %s\nJob: %s\nBackup: %s\n\nThis may overwrite existing data.\nContinue? [y/N]: ", srv.Name, jb.Name, bk.Path)
			var ans string
			fmt.Scanln(&ans)
			if ans != "y" && ans != "Y" && ans != "yes" {
				fmt.Println("aborted")
				return nil
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
		defer cancel()
		if err := restore.RestoreBackup(ctx, dbh, id, backup.RestoreOptions{Destination: dest, Database: dbname}); err != nil {
			return fail(5, err)
		}
		fmt.Println("restore completed")
		return nil
	}
	return c
}

func verifyAliasCmd() *cobra.Command {
	return &cobra.Command{Use: "verify <backup-id>", Short: "Verify backup (alias)", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadCfg()
		if err != nil {
			return fail(2, err)
		}
		db, err := database.Open(cfg.Database.Path)
		if err != nil {
			return fail(1, err)
		}
		defer db.Close()
		var id int64
		fmt.Sscanf(args[0], "%d", &id)
		r, err := verify.Verify(db, id)
		if err != nil {
			fmt.Println("VERIFY FAILED:", err)
			return fail(6, err)
		}
		fmt.Printf("OK: %s size=%s sha256=%s\n", args[0], storage.FormatSize(r.Size), r.Actual)
		return nil
	}}
}
