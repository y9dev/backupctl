package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"backupctl/internal/backup"
	_ "backupctl/internal/backup" // register providers
	"backupctl/internal/config"
	"backupctl/internal/database"
	"backupctl/internal/logging"
	"backupctl/internal/models"
	"backupctl/internal/restore"
	"backupctl/internal/retention"
	"backupctl/internal/scheduler"
	"backupctl/internal/secrets"
	"backupctl/internal/storage"
	"backupctl/internal/tui"
	"backupctl/internal/verify"
	"backupctl/internal/version"

	"github.com/spf13/cobra"
)

var (
	cfgPath  string
	logLevel string
	exitCode = 0
)

func main() {
	root := &cobra.Command{
		Use: "backupctl",
		Run: func(cmd *cobra.Command, args []string) {
			if err := runTUI(); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
		},
		SilenceUsage: true,
	}
	root.PersistentFlags().StringVar(&cfgPath, "config", config.DefaultPath(), "config path")
	root.PersistentFlags().StringVar(&logLevel, "log-level", "INFO", "DEBUG|INFO|WARN|ERROR")

	root.AddCommand(tuiCmd(), daemonCmd(), serverCmd(), jobCmd(), backupCmd(), retentionCmd(), configCmd())
	root.AddCommand(runAliasCmd(), restoreAliasCmd(), verifyAliasCmd())
	root.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println("backupctl", version.Version)
		},
	})
	if err := root.Execute(); err != nil {
		os.Exit(exitCodeOr(1))
	}
}

func exitCodeOr(d int) int {
	if exitCode != 0 {
		return exitCode
	}
	return d
}

func fail(code int, err error) error {
	exitCode = code
	return err
}

func loadCfg() (*config.Config, error) {
	if cfgPath == "" {
		cfgPath = config.DefaultPath()
	}
	return config.Load(cfgPath)
}

func openDB(c *config.Config) (*database.DB, error) {
	return database.Open(c.Database.Path)
}

// syncConfigToDB mirrors YAML servers/jobs into SQLite (source of truth: YAML declarative).
func syncConfigToDB(c *config.Config, db *database.DB) error {
	for _, s := range c.Servers {
		en := true
		if s.Enabled != nil {
			en = *s.Enabled
		}
		id, err := db.UpsertServer(models.Server{Name: s.Name, Host: s.Host, Port: s.Port, Username: s.Username, SSHKey: s.SSHKey, Enabled: en})
		if err != nil {
			return err
		}
		for _, j := range s.Jobs {
			jen := true
			if j.Enabled != nil {
				jen = *j.Enabled
			}
			typ := j.Type
			if typ == "postgres" {
				typ = "postgresql"
			}
			cfg := j.Config
			if cfg == nil {
				cfg = map[string]any{}
			}
			if _, err := db.SyncJobs(id, j.Name, typ, j.Schedule, j.Timeout, jen, cfg); err != nil {
				return err
			}
		}
	}
	return nil
}

func tuiCmd() *cobra.Command {
	c := &cobra.Command{Use: "tui", Aliases: []string{"ui"}, Short: "Open interactive TUI", RunE: func(cmd *cobra.Command, args []string) error {
		return runTUI()
	}}
	return c
}

func runTUI() error {
	return tui.Run(cfgPath)
}

func daemonCmd() *cobra.Command {
	return &cobra.Command{Use: "daemon", Short: "Run scheduler daemon", RunE: func(cmd *cobra.Command, args []string) error {
		log := logging.New(logLevel)
		c, err := loadCfg()
		if err != nil {
			return fail(2, err)
		}
		release, err := scheduler.AcquireLock()
		if err != nil {
			return fail(7, err)
		}
		defer release()
		db, err := openDB(c)
		if err != nil {
			return fail(1, err)
		}
		defer db.Close()
		if err := syncConfigToDB(c, db); err != nil {
			return fail(2, err)
		}
		s := scheduler.New(c, db, log)
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		log.Info("daemon started")
		if err := s.Start(ctx); err != nil {
			return err
		}
		log.Info("daemon stopped")
		return nil
	}}
}

func serverCmd() *cobra.Command {
	s := &cobra.Command{Use: "server", Short: "Manage servers"}
	s.AddCommand(
		&cobra.Command{Use: "list", RunE: func(cmd *cobra.Command, args []string) error {
			c, err := loadCfg()
			if err != nil {
				return fail(2, err)
			}
			db, err := openDB(c)
			if err != nil {
				return fail(1, err)
			}
			defer db.Close()
			_ = syncConfigToDB(c, db)
			servers, _ := db.ListServers()
			fmt.Printf("%-15s %-20s %-5s %-8s\n", "NAME", "HOST", "JOBS", "STATUS")
			for _, srv := range servers {
				jobs, _ := db.ListJobs(srv.ID)
				st := "OK"
				if !srv.Enabled {
					st = "DISABLED"
				}
				fmt.Printf("%-15s %-20s %-5d %-8s\n", srv.Name, srv.Host, len(jobs), st)
			}
			return nil
		}},
		&cobra.Command{Use: "test [name]", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			trust, _ := cmd.Flags().GetBool("trust")
			c, err := loadCfg()
			if err != nil {
				return fail(2, err)
			}
			db, err := openDB(c)
			if err != nil {
				return fail(1, err)
			}
			defer db.Close()
			_ = syncConfigToDB(c, db)
			srv, err := db.GetServerByName(args[0])
			if err != nil {
				return fail(1, err)
			}
			if err := testSSHWithTrust(srv, trust); err != nil {
				fmt.Println("[x] Connection failed:", err)
				return fail(3, err)
			}
			fmt.Println("[✓] Connection successful")
			return nil
		}},
		&cobra.Command{Use: "add", RunE: func(cmd *cobra.Command, args []string) error {
			return fmt.Errorf("use TUI (`backupctl tui`) for interactive server add")
		}},
		&cobra.Command{Use: "remove [name]", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			c, err := loadCfg()
			if err != nil {
				return fail(2, err)
			}
			db, err := openDB(c)
			if err != nil {
				return fail(1, err)
			}
			defer db.Close()
			// remove from YAML (keep backups in DB/files)
			found := false
			kept := []config.ServerConfig{}
			for _, s := range c.Servers {
				if s.Name == args[0] {
					found = true
					continue
				}
				kept = append(kept, s)
			}
			if !found {
				return fail(1, fmt.Errorf("server %q not found", args[0]))
			}
			c.Servers = kept
			if err := c.Save(cfgPath); err != nil {
				return fail(2, err)
			}
			fmt.Println("Server removed. Existing backups were NOT deleted.")
			return nil
		}},
	)
	for _, sub := range s.Commands() {
		if strings.HasPrefix(sub.Use, "test") {
			sub.Flags().Bool("trust", false, "auto-trust unknown host key without prompt")
		}
	}
	return s
}

func jobCmd() *cobra.Command {
	j := &cobra.Command{Use: "job", Short: "Manage jobs"}
	j.AddCommand(
		&cobra.Command{Use: "list", RunE: func(cmd *cobra.Command, args []string) error {
			c, err := loadCfg()
			if err != nil {
				return fail(2, err)
			}
			db, err := openDB(c)
			if err != nil {
				return fail(1, err)
			}
			defer db.Close()
			_ = syncConfigToDB(c, db)
			jobs, _ := db.ListJobs(0)
			fmt.Printf("%-15s %-15s %-12s %-12s %-8s\n", "SERVER", "JOB", "TYPE", "SCHEDULE", "STATUS")
			for _, jb := range jobs {
				srv, _ := db.GetServerByID(jb.ServerID)
				fmt.Printf("%-15s %-15s %-12s %-12s %-8v\n", srv.Name, jb.Name, jb.Type, jb.Schedule, jb.Enabled)
			}
			return nil
		}},
		&cobra.Command{Use: "run <server> [job]", Args: cobra.RangeArgs(1, 2), RunE: func(cmd *cobra.Command, args []string) error {
			log := logging.New(logLevel)
			c, err := loadCfg()
			if err != nil {
				return fail(2, err)
			}
			db, err := openDB(c)
			if err != nil {
				return fail(1, err)
			}
			defer db.Close()
			if err := syncConfigToDB(c, db); err != nil {
				return fail(2, err)
			}
			s := scheduler.New(c, db, log)
			if len(args) == 2 {
				jb, _, err := db.GetJob(args[0], args[1])
				if err != nil {
					return fail(1, fmt.Errorf("job not found: %w", err))
				}
				ctx, cancel := context.WithTimeout(context.Background(), 4*time.Hour)
				defer cancel()
				if err := s.RunJob(ctx, jb.ID); err != nil {
					return fail(4, err)
				}
				fmt.Println("backup completed")
				return nil
			}
			// run all enabled jobs of server
			srv, err := db.GetServerByName(args[0])
			if err != nil {
				return fail(1, err)
			}
			jobs, _ := db.ListJobs(srv.ID)
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
		}},
	)
	return j
}

func backupCmd() *cobra.Command {
	b := &cobra.Command{Use: "backup", Short: "Backups"}
	b.AddCommand(
		&cobra.Command{Use: "list", RunE: func(cmd *cobra.Command, args []string) error {
			c, err := loadCfg()
			if err != nil {
				return fail(2, err)
			}
			db, err := openDB(c)
			if err != nil {
				return fail(1, err)
			}
			defer db.Close()
			list, _ := db.ListBackups(0, 50)
			fmt.Printf("%-5s %-19s %-12s %-12s %-10s\n", "ID", "TIME", "SERVER", "JOB", "SIZE")
			for _, bk := range list {
				srv, _ := db.GetServerByID(bk.ServerID)
				jb, _ := db.GetJobByID(bk.JobID)
				fmt.Printf("%-5d %-19s %-12s %-12s %-10s\n", bk.ID, bk.CreatedAt.Local().Format("02.01 15:04"), srv.Name, jb.Name, storage.FormatSize(bk.Size))
			}
			return nil
		}},
		&cobra.Command{Use: "verify <id>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			c, err := loadCfg()
			if err != nil {
				return fail(2, err)
			}
			db, err := openDB(c)
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
			// postgres extra check
			bk, _ := db.GetBackup(id)
			jb, _ := db.GetJobByID(bk.JobID)
			if jb.Type == "postgresql" {
				msg, err := verify.CheckPostgres(bk.Path)
				if err != nil {
					fmt.Println("pg check FAILED:", err)
					return fail(6, err)
				}
				fmt.Println(msg)
			}
			return nil
		}},
		&cobra.Command{Use: "restore <id>", RunE: func(cmd *cobra.Command, args []string) error {
			dest, _ := cmd.Flags().GetString("dest")
			database, _ := cmd.Flags().GetString("database")
			yes, _ := cmd.Flags().GetBool("yes")
			if len(args) < 1 {
				return fail(1, fmt.Errorf("backup id required"))
			}
			c, err := loadCfg()
			if err != nil {
				return fail(2, err)
			}
			dbh, err := openDB(c)
			if err != nil {
				return fail(1, err)
			}
			defer dbh.Close()
			_ = syncConfigToDB(c, dbh)
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
			if err := restore.RestoreBackup(ctx, dbh, id, backup.RestoreOptions{Destination: dest, Database: database}); err != nil {
				return fail(5, err)
			}
			fmt.Println("restore completed")
			return nil
		}},
	)
	b.Commands()[2].Flags().String("dest", "", "restore destination (directory jobs)")
	b.Commands()[2].Flags().String("database", "", "target database (postgres jobs)")
	b.Commands()[2].Flags().Bool("yes", false, "skip confirmation")
	// alias: backupctl restore <id>
	return b
}

func retentionCmd() *cobra.Command {
	r := &cobra.Command{Use: "retention", Short: "Retention"}
	r.AddCommand(&cobra.Command{Use: "run", RunE: func(cmd *cobra.Command, args []string) error {
		log := logging.New(logLevel)
		c, err := loadCfg()
		if err != nil {
			return fail(2, err)
		}
		db, err := openDB(c)
		if err != nil {
			return fail(1, err)
		}
		defer db.Close()
		_ = syncConfigToDB(c, db)
		jobs, _ := db.ListJobs(0)
		pol := retention.Policy{
			Hourly:  retention.Rule{Count: c.Retention.Hourly.Count, Window: c.Retention.Hourly.Dur},
			Weekly:  retention.Rule{Count: c.Retention.Weekly.Count, Window: c.Retention.Weekly.Dur},
			Monthly: retention.Rule{Count: c.Retention.Monthly.Count, Window: c.Retention.Monthly.Dur},
		}
		for _, jb := range jobs {
			if _, err := retention.Run(db, jb.ID, pol, log); err != nil {
				fmt.Fprintf(os.Stderr, "retention job %d: %v\n", jb.ID, err)
			}
		}
		fmt.Println("retention completed")
		return nil
	}})
	return r
}

func configCmd() *cobra.Command {
	cc := &cobra.Command{Use: "config", Short: "Config"}
	cc.AddCommand(&cobra.Command{Use: "validate", RunE: func(cmd *cobra.Command, args []string) error {
		c, err := loadCfg()
		if err != nil {
			fmt.Println("INVALID:", err)
			return fail(2, err)
		}
		fmt.Printf("Configuration valid.\n\nServers: %d\n", len(c.Servers))
		nj := 0
		for _, s := range c.Servers {
			nj += len(s.Jobs)
		}
		fmt.Printf("Jobs: %d\nStorage: %s\nDatabase: %s\n", nj, c.Storage.Path, c.Database.Path)
		if _, err := secrets.Load(""); err != nil {
			fmt.Println("secrets warning:", err)
		}
		return nil
	}})
	return cc
}
