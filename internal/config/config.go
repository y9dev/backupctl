package config

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/robfig/cron/v3"
	"gopkg.in/yaml.v3"
)

type StorageConfig struct {
	Path         string `yaml:"path"`
	MinFreeSpace string `yaml:"min_free_space,omitempty"`
	MinFreeBytes int64  `yaml:"-"`
}

type DatabaseConfig struct {
	Path string `yaml:"path"`
}

type SchedulerConfig struct {
	MaxParallelJobs int `yaml:"max_parallel_jobs"`
}

type RetentionRule struct {
	Count  int           `yaml:"count"`
	Window string        `yaml:"window"`
	Dur    time.Duration `yaml:"-"`
}

type RetentionConfig struct {
	Hourly   RetentionRule `yaml:"hourly"`
	Weekly   RetentionRule `yaml:"weekly"`
	Monthly  RetentionRule `yaml:"monthly"`
	Orphaned string        `yaml:"orphaned_backups,omitempty"`
}

type ServerConfig struct {
	Name     string      `yaml:"name"`
	Host     string      `yaml:"host,omitempty"`
	Port     int         `yaml:"port,omitempty"`
	Username string      `yaml:"username,omitempty"`
	SSHKey   string      `yaml:"ssh_key,omitempty"`
	Local    bool        `yaml:"local,omitempty"`
	Enabled  *bool       `yaml:"enabled"`
	Jobs     []JobConfig `yaml:"jobs"`
}

type JobConfig struct {
	Name     string         `yaml:"name"`
	Type     string         `yaml:"type"`
	Enabled  *bool          `yaml:"enabled"`
	Schedule string         `yaml:"schedule"`
	Timeout  string         `yaml:"timeout,omitempty"`
	Config   map[string]any `yaml:"config"`
}

type Config struct {
	Storage   StorageConfig   `yaml:"storage"`
	Database  DatabaseConfig  `yaml:"database"`
	Scheduler SchedulerConfig `yaml:"scheduler"`
	Retention RetentionConfig `yaml:"retention"`
	Timezone  string          `yaml:"timezone,omitempty"`
	Retry     RetryConfig     `yaml:"retry,omitempty"`
	Servers   []ServerConfig  `yaml:"servers"`
	path      string
}

type RetryConfig struct {
	Attempts int           `yaml:"attempts"`
	Delay    string        `yaml:"delay"`
	DelayDur time.Duration `yaml:"-"`
}

func DefaultConfig() *Config {
	t := true
	_ = t
	return &Config{
		Storage:   StorageConfig{Path: "/var/backups"},
		Database:  DatabaseConfig{Path: "/var/lib/backupctl/backup.db"},
		Scheduler: SchedulerConfig{MaxParallelJobs: 4},
		Retention: RetentionConfig{
			Hourly:  RetentionRule{Count: 10, Window: "12h"},
			Weekly:  RetentionRule{Count: 1, Window: "168h"},
			Monthly: RetentionRule{Count: 2, Window: "720h"},
		},
		Retry: RetryConfig{Attempts: 3, Delay: "30s"},
	}
}

func DefaultPath() string {
	if v := os.Getenv("BACKUPCTL_CONFIG"); v != "" {
		return v
	}
	return "/etc/backupctl/config.yaml"
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	c.path = path
	if err := c.normalize(); err != nil {
		return nil, err
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) normalize() error {
	if c.Scheduler.MaxParallelJobs <= 0 {
		c.Scheduler.MaxParallelJobs = 4
	}
	parse := func(r *RetentionRule) error {
		if r.Window == "" {
			return nil
		}
		d, err := parseDuration(r.Window)
		if err != nil {
			return err
		}
		r.Dur = d
		return nil
	}
	if err := parse(&c.Retention.Hourly); err != nil {
		return fmt.Errorf("retention.hourly.window: %w", err)
	}
	if err := parse(&c.Retention.Weekly); err != nil {
		return fmt.Errorf("retention.weekly.window: %w", err)
	}
	if err := parse(&c.Retention.Monthly); err != nil {
		return fmt.Errorf("retention.monthly.window: %w", err)
	}
	if c.Storage.MinFreeSpace != "" {
		b, err := parseBytes(c.Storage.MinFreeSpace)
		if err != nil {
			return fmt.Errorf("storage.min_free_space: %w", err)
		}
		c.Storage.MinFreeBytes = b
	}
	if c.Retry.Delay != "" {
		d, err := time.ParseDuration(c.Retry.Delay)
		if err != nil {
			return fmt.Errorf("retry.delay: %w", err)
		}
		c.Retry.DelayDur = d
	} else {
		c.Retry.DelayDur = 30 * time.Second
	}
	if c.Retry.Attempts == 0 {
		c.Retry.Attempts = 3
	}
	if c.Timezone != "" {
		if _, err := time.LoadLocation(c.Timezone); err != nil {
			return fmt.Errorf("timezone: %w", err)
		}
	}
	for i := range c.Servers {
		s := &c.Servers[i]
		if s.Port == 0 {
			s.Port = 22
		}
		if s.Enabled == nil {
			v := true
			s.Enabled = &v
		}
		// Local servers don't need SSH connection details; keep port default
		// for display but don't require host/username/key (checked in Validate).
		for j := range s.Jobs {
			jb := &s.Jobs[j]
			if jb.Enabled == nil {
				v := true
				jb.Enabled = &v
			}
		}
	}
	return nil
}

// Save writes atomically via tmp+rename.
func (c *Config) Save(path string) error {
	if path == "" {
		path = c.path
	}
	if path == "" {
		path = DefaultPath()
	}
	if err := c.normalize(); err != nil {
		return err
	}
	if err := c.Validate(); err != nil {
		return err
	}
	data, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func parseDuration(s string) (time.Duration, error) {
	// support d suffix (days)
	if len(s) > 1 && (s[len(s)-1] == 'd' || s[len(s)-1] == 'D') {
		var n float64
		_, err := fmt.Sscanf(s[:len(s)-1], "%f", &n)
		if err != nil {
			return 0, err
		}
		return time.Duration(n * 24 * float64(time.Hour)), nil
	}
	return time.ParseDuration(s)
}

func parseBytes(s string) (int64, error) {
	var n float64
	var unit string
	_, err := fmt.Sscanf(s, "%f%s", &n, &unit)
	if err != nil {
		return 0, err
	}
	switch unit {
	case "", "B", "b":
		return int64(n), nil
	case "KB", "K", "k", "kb":
		return int64(n * 1024), nil
	case "MB", "M", "m", "mb":
		return int64(n * 1024 * 1024), nil
	case "GB", "G", "g", "gb":
		return int64(n * 1024 * 1024 * 1024), nil
	case "TB", "T":
		return int64(n * 1024 * 1024 * 1024 * 1024), nil
	}
	return 0, fmt.Errorf("unknown size unit %q", unit)
}

var validTypes = map[string]bool{"directory": true, "postgresql": true, "postgres": true, "command": true}

// expandUser expands leading ~/ and env vars for validation (matches TUI wizard behavior).
func expandUser(p string) string {
	if len(p) >= 2 && p[0] == '~' && (p[1] == '/' || p[1] == '\\') {
		if h, err := os.UserHomeDir(); err == nil && h != "" {
			p = filepath.Join(h, p[2:])
		}
	}
	return os.ExpandEnv(p)
}

func (c *Config) Validate() error {
	if c.Storage.Path == "" {
		return fmt.Errorf("storage.path is required")
	}
	if c.Database.Path == "" {
		return fmt.Errorf("database.path is required")
	}
	if c.Retention.Hourly.Count < 0 || c.Retention.Weekly.Count < 0 || c.Retention.Monthly.Count < 0 {
		return fmt.Errorf("retention counts must be >= 0")
	}
	if c.Retention.Hourly.Dur < 0 || c.Retention.Weekly.Dur < 0 || c.Retention.Monthly.Dur < 0 {
		return fmt.Errorf("retention windows must be >= 0")
	}
	seen := map[string]bool{}
	for _, s := range c.Servers {
		if s.Name == "" {
			return fmt.Errorf("server name is required")
		}
		if seen[s.Name] {
			return fmt.Errorf("duplicate server name %q", s.Name)
		}
		seen[s.Name] = true
		if s.Local {
			// Local mode: backup runs directly on this machine, no SSH.
			// Host/username/key are not required and are ignored.
			if s.SSHKey != "" {
				if _, err := os.Stat(expandUser(s.SSHKey)); err != nil {
					return fmt.Errorf("server %q: ssh_key %q: %w", s.Name, s.SSHKey, err)
				}
			}
		} else {
			if s.Host == "" {
				return fmt.Errorf("server %q: host is required (or set local: true for on-machine backups)", s.Name)
			}
			if s.Username == "" {
				return fmt.Errorf("server %q: username is required", s.Name)
			}
			if s.SSHKey != "" {
				if _, err := os.Stat(expandUser(s.SSHKey)); err != nil {
					// only error if file:// path exists check fails AND config file exists on disk?
					// For validation strictness keep error, but allow missing in tests via env? Keep error.
					return fmt.Errorf("server %q: ssh_key %q: %w", s.Name, s.SSHKey, err)
				}
			}
		}
		jseen := map[string]bool{}
		for _, j := range s.Jobs {
			if j.Name == "" {
				return fmt.Errorf("server %q: job name is required", s.Name)
			}
			if jseen[j.Name] {
				return fmt.Errorf("server %q: duplicate job name %q", s.Name, j.Name)
			}
			jseen[j.Name] = true
			if !validTypes[j.Type] {
				return fmt.Errorf("server %q job %q: unknown type %q", s.Name, j.Name, j.Type)
			}
			if _, err := cron.ParseStandard(j.Schedule); err != nil {
				return fmt.Errorf("server %q job %q: invalid schedule %q: %w", s.Name, j.Name, j.Schedule, err)
			}
			if j.Timeout != "" {
				if _, err := time.ParseDuration(j.Timeout); err != nil {
					return fmt.Errorf("server %q job %q: invalid timeout: %w", s.Name, j.Name, err)
				}
			}
			if err := validateProviderConfig(j.Type, j.Config); err != nil {
				return fmt.Errorf("server %q job %q: %w", s.Name, j.Name, err)
			}
		}
	}
	return nil
}

func validateProviderConfig(typ string, cfg map[string]any) error {
	switch typ {
	case "directory":
		src, _ := cfg["source"].(string)
		if src == "" {
			return fmt.Errorf("directory provider: source is required")
		}
	case "postgresql", "postgres":
		for _, k := range []string{"database", "username"} {
			if v, _ := cfg[k].(string); v == "" {
				// allow nested connection map
				if conn, ok := cfg["connection"].(map[string]any); ok {
					if cv, _ := conn[k].(string); cv != "" {
						continue
					}
				}
				return fmt.Errorf("postgresql provider: %s is required", k)
			}
		}
	case "command":
		if _, ok := cfg["command"]; !ok {
			return fmt.Errorf("command provider: command is required")
		}
	}
	return nil
}
