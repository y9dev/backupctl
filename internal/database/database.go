package database

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"backupctl/internal/models"

	_ "modernc.org/sqlite"
)

type DB struct {
	SQL *sql.DB
}

func Open(path string) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)", path)
	sdb, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db := &DB{SQL: sdb}
	if err := db.migrate(); err != nil {
		sdb.Close()
		return nil, err
	}
	return db, nil
}

func (d *DB) Close() error { return d.SQL.Close() }

func (d *DB) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS servers (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT UNIQUE NOT NULL, host TEXT NOT NULL DEFAULT '', port INTEGER NOT NULL DEFAULT 22, username TEXT NOT NULL DEFAULT '', ssh_key TEXT NOT NULL DEFAULT '', local INTEGER NOT NULL DEFAULT 0, enabled INTEGER NOT NULL DEFAULT 1, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS backup_jobs (id INTEGER PRIMARY KEY AUTOINCREMENT, server_id INTEGER NOT NULL REFERENCES servers(id) ON DELETE CASCADE, name TEXT NOT NULL, type TEXT NOT NULL, schedule TEXT NOT NULL DEFAULT '', enabled INTEGER NOT NULL DEFAULT 1, timeout TEXT NOT NULL DEFAULT '', config_json TEXT NOT NULL DEFAULT '{}', created_at TEXT NOT NULL, updated_at TEXT NOT NULL, UNIQUE(server_id, name))`,
		`CREATE TABLE IF NOT EXISTS backup_runs (id INTEGER PRIMARY KEY AUTOINCREMENT, job_id INTEGER NOT NULL, server_id INTEGER NOT NULL, started_at TEXT NOT NULL, finished_at TEXT NOT NULL DEFAULT '', status TEXT NOT NULL, backup_path TEXT NOT NULL DEFAULT '', backup_size INTEGER NOT NULL DEFAULT 0, error TEXT NOT NULL DEFAULT '')`,
		`CREATE TABLE IF NOT EXISTS backups (id INTEGER PRIMARY KEY AUTOINCREMENT, job_id INTEGER NOT NULL, server_id INTEGER NOT NULL, path TEXT NOT NULL, created_at TEXT NOT NULL, size INTEGER NOT NULL DEFAULT 0, checksum TEXT NOT NULL DEFAULT '')`,
		`CREATE INDEX IF NOT EXISTS idx_runs_job ON backup_runs(job_id, started_at)`,
		`CREATE INDEX IF NOT EXISTS idx_backups_job ON backups(job_id, created_at)`,
	}
	for _, s := range stmts {
		if _, err := d.SQL.Exec(s); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	// Backward-compatible schema upgrades for DBs created before a column existed.
	if err := d.ensureColumn("servers", "local", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := d.ensureColumn("servers", "ssh_key", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	// Older DBs declared host/username NOT NULL without default; relax by
	// ensuring new rows can store empty values for local servers. SQLite cannot
	// alter NOT NULL directly, but empty-string defaults on insert are handled
	// in code; nothing more needed here.
	return nil
}

func (d *DB) ensureColumn(table, column, ddlType string) error {
	rows, err := d.SQL.Query(fmt.Sprintf(`PRAGMA table_info(%s)`, table))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull, pk int
		var dflt any
		var extra any
		// PRAGMA table_info returns 6 columns; scan tolerantly.
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			// fallback: try scanning with extra
			_ = extra
			return err
		}
		if name == column {
			return rows.Err()
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = d.SQL.Exec(fmt.Sprintf(`ALTER TABLE %s ADD COLUMN %s %s`, table, column, ddlType))
	return err
}

func nowUTC() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// --- servers ---

func (d *DB) UpsertServer(s models.Server) (int64, error) {
	now := nowUTC()
	var id int64
	err := d.SQL.QueryRow(`SELECT id FROM servers WHERE name=?`, s.Name).Scan(&id)
	if err == sql.ErrNoRows {
		res, err := d.SQL.Exec(`INSERT INTO servers(name,host,port,username,ssh_key,local,enabled,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?)`,
			s.Name, s.Host, s.Port, s.Username, s.SSHKey, boolToInt(s.Local), boolToInt(s.Enabled), now, now)
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}
	if err != nil {
		return 0, err
	}
	_, err = d.SQL.Exec(`UPDATE servers SET host=?,port=?,username=?,ssh_key=?,local=?,enabled=?,updated_at=? WHERE id=?`,
		s.Host, s.Port, s.Username, s.SSHKey, boolToInt(s.Local), boolToInt(s.Enabled), now, id)
	return id, err
}

func (d *DB) ListServers() ([]models.Server, error) {
	rows, err := d.SQL.Query(`SELECT id,name,host,port,username,ssh_key,local,enabled,created_at,updated_at FROM servers ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Server
	for rows.Next() {
		var s models.Server
		var en, loc int
		var ca, ua string
		if err := rows.Scan(&s.ID, &s.Name, &s.Host, &s.Port, &s.Username, &s.SSHKey, &loc, &en, &ca, &ua); err != nil {
			return nil, err
		}
		s.Local = loc == 1
		s.Enabled = en == 1
		s.CreatedAt, _ = time.Parse(time.RFC3339Nano, ca)
		s.UpdatedAt, _ = time.Parse(time.RFC3339Nano, ua)
		out = append(out, s)
	}
	return out, rows.Err()
}

func (d *DB) GetServerByName(name string) (models.Server, error) {
	var s models.Server
	var en, loc int
	var ca, ua string
	err := d.SQL.QueryRow(`SELECT id,name,host,port,username,ssh_key,local,enabled,created_at,updated_at FROM servers WHERE name=?`, name).
		Scan(&s.ID, &s.Name, &s.Host, &s.Port, &s.Username, &s.SSHKey, &loc, &en, &ca, &ua)
	if err != nil {
		return s, err
	}
	s.Local = loc == 1
	s.Enabled = en == 1
	s.CreatedAt, _ = time.Parse(time.RFC3339Nano, ca)
	s.UpdatedAt, _ = time.Parse(time.RFC3339Nano, ua)
	return s, nil
}

func (d *DB) DeleteServer(name string) error {
	_, err := d.SQL.Exec(`DELETE FROM servers WHERE name=?`, name)
	return err
}

// --- jobs ---

func (d *DB) SyncJobs(serverID int64, name, typ, schedule, timeout string, enabled bool, cfg map[string]any) (int64, error) {
	js, _ := json.Marshal(cfg)
	now := nowUTC()
	var id int64
	err := d.SQL.QueryRow(`SELECT id FROM backup_jobs WHERE server_id=? AND name=?`, serverID, name).Scan(&id)
	if err == sql.ErrNoRows {
		res, err := d.SQL.Exec(`INSERT INTO backup_jobs(server_id,name,type,schedule,enabled,timeout,config_json,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?)`,
			serverID, name, typ, schedule, boolToInt(enabled), timeout, string(js), now, now)
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}
	if err != nil {
		return 0, err
	}
	_, err = d.SQL.Exec(`UPDATE backup_jobs SET type=?,schedule=?,enabled=?,timeout=?,config_json=?,updated_at=? WHERE id=?`,
		typ, schedule, boolToInt(enabled), timeout, string(js), now, id)
	return id, err
}

func (d *DB) ListJobs(serverID int64) ([]models.BackupJob, error) {
	var rows *sql.Rows
	var err error
	if serverID == 0 {
		rows, err = d.SQL.Query(`SELECT id,server_id,name,type,schedule,enabled,timeout,config_json,created_at,updated_at FROM backup_jobs ORDER BY server_id,name`)
	} else {
		rows, err = d.SQL.Query(`SELECT id,server_id,name,type,schedule,enabled,timeout,config_json,created_at,updated_at FROM backup_jobs WHERE server_id=? ORDER BY name`, serverID)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.BackupJob
	for rows.Next() {
		var j models.BackupJob
		var en int
		var ca, ua string
		if err := rows.Scan(&j.ID, &j.ServerID, &j.Name, &j.Type, &j.Schedule, &en, &j.Timeout, &j.ConfigJSON, &ca, &ua); err != nil {
			return nil, err
		}
		j.Enabled = en == 1
		j.CreatedAt, _ = time.Parse(time.RFC3339Nano, ca)
		j.UpdatedAt, _ = time.Parse(time.RFC3339Nano, ua)
		var m map[string]any
		_ = json.Unmarshal([]byte(j.ConfigJSON), &m)
		if m == nil {
			m = map[string]any{}
		}
		j.Config = m
		out = append(out, j)
	}
	return out, rows.Err()
}

func (d *DB) GetJob(serverName, jobName string) (models.BackupJob, models.Server, error) {
	srv, err := d.GetServerByName(serverName)
	if err != nil {
		return models.BackupJob{}, srv, err
	}
	var j models.BackupJob
	var en int
	var ca, ua string
	err = d.SQL.QueryRow(`SELECT id,server_id,name,type,schedule,enabled,timeout,config_json,created_at,updated_at FROM backup_jobs WHERE server_id=? AND name=?`, srv.ID, jobName).
		Scan(&j.ID, &j.ServerID, &j.Name, &j.Type, &j.Schedule, &en, &j.Timeout, &j.ConfigJSON, &ca, &ua)
	if err != nil {
		return j, srv, err
	}
	j.Enabled = en == 1
	j.CreatedAt, _ = time.Parse(time.RFC3339Nano, ca)
	j.UpdatedAt, _ = time.Parse(time.RFC3339Nano, ua)
	var m map[string]any
	_ = json.Unmarshal([]byte(j.ConfigJSON), &m)
	if m == nil {
		m = map[string]any{}
	}
	j.Config = m
	return j, srv, nil
}

func (d *DB) DeleteJob(id int64) error {
	_, err := d.SQL.Exec(`DELETE FROM backup_jobs WHERE id=?`, id)
	return err
}

// --- runs & backups ---

func (d *DB) CreateRun(jobID, serverID int64) (int64, error) {
	res, err := d.SQL.Exec(`INSERT INTO backup_runs(job_id,server_id,started_at,status) VALUES(?,?,?,?)`,
		jobID, serverID, nowUTC(), string(models.RunRunning))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (d *DB) FinishRun(id int64, status models.RunStatus, path string, size int64, errMsg string) error {
	_, err := d.SQL.Exec(`UPDATE backup_runs SET finished_at=?,status=?,backup_path=?,backup_size=?,error=? WHERE id=?`,
		nowUTC(), string(status), path, size, errMsg, id)
	return err
}

func (d *DB) MarkStaleRunning() (int64, error) {
	res, err := d.SQL.Exec(`UPDATE backup_runs SET status=?, finished_at=?, error=? WHERE status=?`,
		string(models.RunFailed), nowUTC(), "daemon restarted: marked stale", string(models.RunRunning))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (d *DB) AddBackup(jobID, serverID int64, path string, size int64, checksum string, created time.Time) (int64, error) {
	if created.IsZero() {
		created = time.Now().UTC()
	}
	res, err := d.SQL.Exec(`INSERT INTO backups(job_id,server_id,path,created_at,size,checksum) VALUES(?,?,?,?,?,?)`,
		jobID, serverID, path, created.UTC().Format(time.RFC3339Nano), size, checksum)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// BackupFilter selects a page of the backup history (newest first).
// Limit <= 0 means no limit; Offset < 0 is treated as 0.
type BackupFilter struct {
	JobID    int64
	ServerID int64
	Limit    int
	Offset   int
}

func (d *DB) ListBackups(jobID int64, limit int) ([]models.Backup, error) {
	return d.ListBackupsPaged(BackupFilter{JobID: jobID, Limit: limit})
}

// ListBackupsPaged returns one page of backups with optional job/server filters.
func (d *DB) ListBackupsPaged(f BackupFilter) ([]models.Backup, error) {
	q := `SELECT id,job_id,server_id,path,created_at,size,checksum FROM backups`
	var conds []string
	var args []any
	if f.JobID != 0 {
		conds = append(conds, `job_id=?`)
		args = append(args, f.JobID)
	}
	if f.ServerID != 0 {
		conds = append(conds, `server_id=?`)
		args = append(args, f.ServerID)
	}
	if len(conds) > 0 {
		q += ` WHERE ` + strings.Join(conds, ` AND `)
	}
	// id DESC tie-break keeps pagination stable for equal timestamps.
	q += ` ORDER BY created_at DESC, id DESC`
	if f.Limit > 0 {
		q += fmt.Sprintf(` LIMIT %d`, f.Limit)
	}
	if f.Offset > 0 {
		if f.Limit <= 0 {
			q += ` LIMIT -1`
		}
		q += fmt.Sprintf(` OFFSET %d`, f.Offset)
	}
	rows, err := d.SQL.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Backup
	for rows.Next() {
		var b models.Backup
		var ca string
		if err := rows.Scan(&b.ID, &b.JobID, &b.ServerID, &b.Path, &ca, &b.Size, &b.Checksum); err != nil {
			return nil, err
		}
		b.CreatedAt, _ = time.Parse(time.RFC3339Nano, ca)
		out = append(out, b)
	}
	return out, rows.Err()
}

// CountBackups returns the total number of backups matching the filters.
func (d *DB) CountBackups(jobID, serverID int64) (int, error) {
	q := `SELECT COUNT(*) FROM backups`
	var conds []string
	var args []any
	if jobID != 0 {
		conds = append(conds, `job_id=?`)
		args = append(args, jobID)
	}
	if serverID != 0 {
		conds = append(conds, `server_id=?`)
		args = append(args, serverID)
	}
	if len(conds) > 0 {
		q += ` WHERE ` + strings.Join(conds, ` AND `)
	}
	var n int
	if err := d.SQL.QueryRow(q, args...).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

func (d *DB) GetBackup(id int64) (models.Backup, error) {
	var b models.Backup
	var ca string
	err := d.SQL.QueryRow(`SELECT id,job_id,server_id,path,created_at,size,checksum FROM backups WHERE id=?`, id).
		Scan(&b.ID, &b.JobID, &b.ServerID, &b.Path, &ca, &b.Size, &b.Checksum)
	if err != nil {
		return b, err
	}
	b.CreatedAt, _ = time.Parse(time.RFC3339Nano, ca)
	return b, nil
}

func (d *DB) DeleteBackup(id int64) error {
	_, err := d.SQL.Exec(`DELETE FROM backups WHERE id=?`, id)
	return err
}

// RunFilter selects a page of the run history (newest first).
type RunFilter struct {
	JobID    int64
	ServerID int64
	Status   string
	Limit    int
	Offset   int
}

func (d *DB) ListRuns(limit int) ([]models.BackupRun, error) {
	return d.ListRunsPaged(RunFilter{Limit: limit})
}

// ListRunsPaged returns one page of backup runs with optional filters.
func (d *DB) ListRunsPaged(f RunFilter) ([]models.BackupRun, error) {
	q := `SELECT id,job_id,server_id,started_at,finished_at,status,backup_path,backup_size,error FROM backup_runs`
	var conds []string
	var args []any
	if f.JobID != 0 {
		conds = append(conds, `job_id=?`)
		args = append(args, f.JobID)
	}
	if f.ServerID != 0 {
		conds = append(conds, `server_id=?`)
		args = append(args, f.ServerID)
	}
	if f.Status != "" {
		conds = append(conds, `status=?`)
		args = append(args, f.Status)
	}
	if len(conds) > 0 {
		q += ` WHERE ` + strings.Join(conds, ` AND `)
	}
	q += ` ORDER BY started_at DESC, id DESC`
	if f.Limit > 0 {
		q += fmt.Sprintf(` LIMIT %d`, f.Limit)
	} else if f.Offset > 0 {
		q += ` LIMIT -1`
	}
	if f.Offset > 0 {
		q += fmt.Sprintf(` OFFSET %d`, f.Offset)
	}
	rows, err := d.SQL.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.BackupRun
	for rows.Next() {
		var r models.BackupRun
		var sa, fa string
		if err := rows.Scan(&r.ID, &r.JobID, &r.ServerID, &sa, &fa, &r.Status, &r.BackupPath, &r.BackupSize, &r.Error); err != nil {
			return nil, err
		}
		r.StartedAt, _ = time.Parse(time.RFC3339Nano, sa)
		if fa != "" {
			r.FinishedAt, _ = time.Parse(time.RFC3339Nano, fa)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// CountRuns returns the total number of runs matching the filters.
func (d *DB) CountRuns(jobID, serverID int64, status string) (int, error) {
	q := `SELECT COUNT(*) FROM backup_runs`
	var conds []string
	var args []any
	if jobID != 0 {
		conds = append(conds, `job_id=?`)
		args = append(args, jobID)
	}
	if serverID != 0 {
		conds = append(conds, `server_id=?`)
		args = append(args, serverID)
	}
	if status != "" {
		conds = append(conds, `status=?`)
		args = append(args, status)
	}
	if len(conds) > 0 {
		q += ` WHERE ` + strings.Join(conds, ` AND `)
	}
	var n int
	if err := d.SQL.QueryRow(q, args...).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

func (d *DB) GetJobByID(id int64) (models.BackupJob, error) {
	var j models.BackupJob
	var en int
	var ca, ua string
	err := d.SQL.QueryRow(`SELECT id,server_id,name,type,schedule,enabled,timeout,config_json,created_at,updated_at FROM backup_jobs WHERE id=?`, id).
		Scan(&j.ID, &j.ServerID, &j.Name, &j.Type, &j.Schedule, &en, &j.Timeout, &j.ConfigJSON, &ca, &ua)
	if err != nil {
		return j, err
	}
	j.Enabled = en == 1
	var m map[string]any
	_ = json.Unmarshal([]byte(j.ConfigJSON), &m)
	if m == nil {
		m = map[string]any{}
	}
	j.Config = m
	return j, nil
}

func (d *DB) GetServerByID(id int64) (models.Server, error) {
	var s models.Server
	var en, loc int
	var ca, ua string
	err := d.SQL.QueryRow(`SELECT id,name,host,port,username,ssh_key,local,enabled,created_at,updated_at FROM servers WHERE id=?`, id).
		Scan(&s.ID, &s.Name, &s.Host, &s.Port, &s.Username, &s.SSHKey, &loc, &en, &ca, &ua)
	if err != nil {
		return s, err
	}
	s.Local = loc == 1
	s.Enabled = en == 1
	return s, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
