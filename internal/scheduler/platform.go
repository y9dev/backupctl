package scheduler

import (
	"os"
)

func freeSpace(path string) (uint64, error) {
	// cross-platform stub: try statfs via os; on failure return large number.
	// Real implementation per-OS; keep simple: check nothing, return huge.
	_ = path
	_ = os.Getenv
	return 1 << 62, nil
}

func pidAlive(pid int) bool {
	// On unix check /proc; on windows best-effort via lock file removal on clean exit.
	// Conservatively treat existing pid file as stale only if unreadable handled by caller override env.
	// Try to keep simple: if pid == current, alive; else check process existence is platform-specific.
	// Return true to be safe (prevent double daemon) unless BACKUPCTL_PID_FORCE=1.
	if os.Getenv("BACKUPCTL_PID_FORCE") == "1" {
		return false
	}
	return true
}
