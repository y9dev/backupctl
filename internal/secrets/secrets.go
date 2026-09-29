package secrets

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

func DefaultPath() string {
	if v := os.Getenv("BACKUPCTL_SECRETS"); v != "" {
		return v
	}
	return "/etc/backupctl/secrets.yaml"
}

// Load reads secrets.yaml (0600). Returns empty map if file missing.
func Load(path string) (map[string]string, error) {
	if path == "" {
		path = DefaultPath()
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, err
	}
	var m map[string]string
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse secrets: %w", err)
	}
	if m == nil {
		m = map[string]string{}
	}
	return m, nil
}

func Save(path string, m map[string]string) error {
	if path == "" {
		path = DefaultPath()
	}
	data, err := yaml.Marshal(m)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
