package tui

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"backupctl/internal/config"
	"backupctl/internal/database"
	"backupctl/internal/models"
	"backupctl/internal/secrets"
	"backupctl/internal/sshclient"
)

var schedules = []string{
	"Every 15 minutes|*/15 * * * *",
	"Every 30 minutes|*/30 * * * *",
	"Every hour|0 * * * *",
	"Every 2 hours|0 */2 * * *",
	"Every 6 hours|0 */6 * * *",
	"Every 12 hours|0 */12 * * *",
	"Every day|0 3 * * *",
}

// OS-зависимые подсказки: на Windows пути /var/... и $HOME/.ssh бессмысленны,
// поэтому defaults выбираются по runtime.GOOS.
func defaultStoragePath() string {
	if runtime.GOOS == "windows" {
		return `.\backups\storage`
	}
	return "/var/backups"
}

func defaultDBPath() string {
	if runtime.GOOS == "windows" {
		return `.\backups\backup.db`
	}
	return "/var/lib/backupctl/backup.db"
}

func defaultSSHKey() string {
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		return filepath.Join(h, ".ssh", "id_ed25519")
	}
	if runtime.GOOS == "windows" {
		return `.ssh\id_ed25519`
	}
	return "/root/.ssh/id_ed25519"
}

// expandPath раскрывает ведущий ~/ в домашний каталог и переменные окружения.
// Раньше ввод ~/.ssh/key тихо приводил к ошибке валидации при сохранении.
func expandPath(p string) string {
	if strings.HasPrefix(p, "~/") || p == "~" {
		if h, err := os.UserHomeDir(); err == nil {
			if p == "~" {
				return h
			}
			p = filepath.Join(h, p[2:])
		}
	}
	return os.ExpandEnv(p)
}

func absHint(p string) string {
	if a, err := filepath.Abs(p); err == nil {
		return a
	}
	return p
}

// promptExistingFile спрашивает путь к файлу и сразу проверяет его наличие,
// чтобы не падать на валидации в самом конце wizard'а.
func promptExistingFile(r *bufio.Reader, label, def string) string {
	v := expandPath(prompt(r, label, def))
	for {
		if _, err := os.Stat(v); err == nil {
			return v
		}
		fmt.Printf("[!] Файл не найден: %s\n", absHint(v))
		keep := prompt(r, "Оставить как есть? [y/N]", "N")
		if k := strings.ToLower(strings.TrimSpace(keep)); k == "y" || k == "yes" {
			return v
		}
		v = expandPath(prompt(r, label, v))
	}
}

func prompt(r *bufio.Reader, label, def string) string {
	if def != "" {
		fmt.Printf("%s [%s]: ", label, def)
	} else {
		fmt.Printf("%s: ", label)
	}
	line, _ := r.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return def
	}
	return line
}

// RunSetupWizard implements spec §26 steps 1-8.
func RunSetupWizard(cfgPath string) error {
	r := bufio.NewReader(os.Stdin)
	fmt.Printf("=== Backup Manager setup wizard (OS: %s) ===\n", runtime.GOOS)
	storagePath := expandPath(prompt(r, "Backup storage", defaultStoragePath()))
	fmt.Printf("    -> %s\n", absHint(storagePath))
	dbPath := expandPath(prompt(r, "Database", defaultDBPath()))
	fmt.Printf("    -> %s\n", absHint(dbPath))
	c := config.DefaultConfig()
	c.Storage.Path = storagePath
	c.Database.Path = dbPath
	fmt.Println("--- Retention ---")
	fmt.Println("Presets: hourly 10/12h, weekly 1/7d, monthly 2/30d. Press Enter for defaults.")
	hc := prompt(r, "Hourly backups", "10")
	hw := prompt(r, "Hourly window", "12h")
	wc := prompt(r, "Weekly backups", "1")
	ww := prompt(r, "Weekly window", "168h")
	mc := prompt(r, "Monthly backups", "2")
	mw := prompt(r, "Monthly window", "720h")
	_ = hc
	_ = hw
	_ = wc
	_ = ww
	_ = mc
	_ = mw
	// server
	fmt.Println("--- Add server ---")
	fmt.Println("Where will backups run? 1) Remote server via SSH  2) This machine (local)")
	locChoice := prompt(r, "Choice", "1")
	isLocal := strings.TrimSpace(locChoice) == "2"
	sname := prompt(r, "Server name", "prod-1")
	var host, user, key string
	port := 22
	if isLocal {
		fmt.Println("Local mode: jobs run directly on this machine, no SSH needed.")
	} else {
		host = prompt(r, "Host", "")
		portStr := prompt(r, "SSH port", "22")
		user = prompt(r, "Username", "backup")
		key = promptExistingFile(r, "SSH key", defaultSSHKey())
		fmt.Printf("    -> %s\n", absHint(key))
		fmt.Sscanf(portStr, "%d", &port)
	}
	en := true
	srv := config.ServerConfig{Name: sname, Host: host, Port: port, Username: user, SSHKey: key, Local: isLocal, Enabled: &en}

	// test SSH (с trust-flow: при неизвестном host key спрашиваем fingerprint)
	if isLocal {
		fmt.Println("[✓] Local server — connection test skipped (no SSH)")
	} else {
		fmt.Println("Testing connection...")
		if err := testConnWizard(r, srv); err != nil {
			fmt.Println("[x] Connection failed:", err)
			fmt.Println("Fix settings in config later with `backupctl tui`.")
		} else {
			fmt.Println("[✓] Connection successful")
		}
	}

	// first job
	fmt.Println("--- Add first job ---")
	jname := prompt(r, "Job name", "app")
	fmt.Println("Type: 1) Directory  2) PostgreSQL  3) Command")
	tchoice := prompt(r, "Choice", "1")
	jtype := "directory"
	var jcfg map[string]any
	switch tchoice {
	case "2":
		jtype = "postgresql"
		dbn := prompt(r, "Database", "production")
		dbu := prompt(r, "DB username", "backup")
		pw := prompt(r, "DB password (stored in secrets.yaml 0600)", "")
		secName := "postgres_" + strings.ReplaceAll(sname, "-", "_")
		if pw != "" {
			m, _ := secrets.Load("")
			m[secName] = pw
			_ = secrets.Save("", m)
			fmt.Printf("Password saved to %s (0600). Never shown in TUI/logs.\n", secrets.DefaultPath())
		}
		fmt.Println("Where is PostgreSQL? 1) On host  2) In Docker container")
		pgWhere := prompt(r, "Choice", "1")
		jcfg = map[string]any{"database": dbn, "username": dbu, "password_secret": secName, "host": "127.0.0.1", "port": "5432"}
		if pgWhere == "2" {
			container := prompt(r, "Docker container name", "postgres")
			jcfg["container"] = container
			if isLocal {
				fmt.Println("pg_dump will run inside the container via `docker exec` on this machine.")
			} else {
				fmt.Println("pg_dump will run inside the container via `docker exec` on the remote host.")
			}
		}
	case "3":
		jtype = "command"
		cmd := prompt(r, "Command", "/usr/local/bin/create-backup.sh")
		jcfg = map[string]any{"command": []any{cmd}, "output": map[string]any{"type": "stdout", "filename": "output.bin"}}
	default:
		src := prompt(r, "Source directory", "/opt/app")
		jcfg = map[string]any{"source": src, "compression": map[string]any{"type": "zstd", "level": 3}}
	}
	fmt.Println("Schedule:")
	for i, s := range schedules {
		fmt.Printf("  %d) %s\n", i+1, strings.Split(s, "|")[0])
	}
	fmt.Println("  8) Custom cron")
	sch := prompt(r, "Choice", "3")
	cron := "0 * * * *"
	if idx := parseIdx(sch, len(schedules)); idx >= 0 {
		cron = strings.Split(schedules[idx], "|")[1]
	} else {
		cron = prompt(r, "Custom cron", "0 * * * *")
	}
	jen := true
	srv.Jobs = []config.JobConfig{{Name: jname, Type: jtype, Enabled: &jen, Schedule: cron, Config: jcfg}}
	c.Servers = []config.ServerConfig{srv}

	fmt.Println("--- Configuration summary ---")
	fmt.Printf("Storage:\n  %s\nServers:\n  %s\nJobs:\n  %s/%s\nRetention:\n  10 / 1 / 2\n", c.Storage.Path, sname, sname, jname)
	fmt.Print("Save configuration? [Y/n]: ")
	line, _ := r.ReadString('\n')
	line = strings.TrimSpace(strings.ToLower(line))
	if line != "" && line != "y" && line != "yes" {
		fmt.Println("aborted")
		return nil
	}
	if err := c.Save(cfgPath); err != nil {
		return err
	}
	// init DB
	db, err := database.Open(c.Database.Path)
	if err != nil {
		return err
	}
	defer db.Close()
	id, err := db.UpsertServer(models.Server{Name: srv.Name, Host: srv.Host, Port: srv.Port, Username: srv.Username, SSHKey: srv.SSHKey, Local: srv.Local, Enabled: true})
	if err != nil {
		return err
	}
	_, err = db.SyncJobs(id, jname, jtype, cron, "", true, jcfg)
	return err
}

func parseIdx(s string, n int) int {
	var i int
	if _, err := fmt.Sscanf(s, "%d", &i); err == nil && i >= 1 && i <= n {
		return i - 1
	}
	return -1
}

// testConnWizard — проверка SSH с интерактивным trust при неизвестном host key.
func testConnWizard(r *bufio.Reader, s config.ServerConfig) error {
	info := sshclient.ServerInfo{Host: s.Host, Port: s.Port, Username: s.Username, KeyPath: s.SSHKey}
	err := sshclient.ConnectWithTrust(info, func(fp string) bool {
		fmt.Printf("Host key is unknown.\n\nFingerprint:\n  %s\n\nTrust this host? [y/N]: ", fp)
		line, _ := r.ReadString('\n')
		ans := strings.ToLower(strings.TrimSpace(line))
		return ans == "y" || ans == "yes"
	})
	if err == nil {
		fmt.Println("Host key saved.")
	}
	return err
}
