package main

import (
	"fmt"
	"strings"

	"backupctl/internal/models"
	"backupctl/internal/sshclient"
)

func testSSH(srv models.Server) error {
	return testSSHWithTrust(srv, false)
}

// testSSHWithTrust dials the server. If the host key is unknown, it shows
// the fingerprint and asks whether to trust it (spec §20), saves it to
// known_hosts and retries. autoTrust skips the prompt (automation).
func testSSHWithTrust(srv models.Server, autoTrust bool) error {
	info := sshclient.ServerInfo{Host: srv.Host, Port: srv.Port, Username: srv.Username, KeyPath: srv.SSHKey}
	if err := sshclient.ConnectWithTrust(info, func(fp string) bool {
		if autoTrust {
			fmt.Printf("Trusting unknown host key: %s\n", fp)
			return true
		}
		fmt.Printf("Host key is unknown.\n\nFingerprint:\n  %s\n\nTrust this host? [y/N]: ", fp)
		var ans string
		fmt.Scanln(&ans)
		a := strings.ToLower(strings.TrimSpace(ans))
		return a == "y" || a == "yes"
	}); err != nil {
		return fmt.Errorf("%w", err)
	}
	return nil
}
