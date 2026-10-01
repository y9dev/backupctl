package models

import "time"

type Server struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Host      string    `json:"host"`
	Port      int       `json:"port"`
	Username  string    `json:"username"`
	SSHKey    string    `json:"ssh_key"`
	Local     bool      `json:"local"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type BackupJob struct {
	ID         int64          `json:"id"`
	ServerID   int64          `json:"server_id"`
	Name       string         `json:"name"`
	Type       string         `json:"type"`
	Enabled    bool           `json:"enabled"`
	Schedule   string         `json:"schedule"`
	Timeout    string         `json:"timeout"`
	Config     map[string]any `json:"config"`
	RawConfig  map[string]any `json:"-"`
	ConfigJSON string         `json:"config_json"`
	CreatedAt  time.Time      `json:"created_at"`
	UpdatedAt  time.Time      `json:"updated_at"`
}

type RunStatus string

const (
	RunRunning   RunStatus = "running"
	RunSuccess   RunStatus = "success"
	RunFailed    RunStatus = "failed"
	RunCancelled RunStatus = "cancelled"
)

type BackupRun struct {
	ID         int64     `json:"id"`
	JobID      int64     `json:"job_id"`
	ServerID   int64     `json:"server_id"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
	Status     RunStatus `json:"status"`
	BackupPath string    `json:"backup_path"`
	BackupSize int64     `json:"backup_size"`
	Error      string    `json:"error"`
}

type Backup struct {
	ID        int64     `json:"id"`
	JobID     int64     `json:"job_id"`
	ServerID  int64     `json:"server_id"`
	Path      string    `json:"path"`
	CreatedAt time.Time `json:"created_at"`
	Size      int64     `json:"size"`
	Checksum  string    `json:"checksum"`
}
