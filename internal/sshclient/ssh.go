package sshclient

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

type ServerInfo struct {
	Host     string
	Port     int
	Username string
	KeyPath  string
}

func KnownHostsPath() string {
	if v := os.Getenv("BACKUPCTL_KNOWN_HOSTS"); v != "" {
		return v
	}
	h, _ := os.UserHomeDir()
	if h == "" {
		h = "/root"
	}
	return filepath.Join(h, ".ssh", "known_hosts")
}

// Dial connects with key auth and known_hosts verification.
func Dial(s ServerInfo) (*ssh.Client, error) {
	key, err := os.ReadFile(s.KeyPath)
	if err != nil {
		return nil, fmt.Errorf("read ssh key %s: %w", s.KeyPath, err)
	}
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("parse ssh key: %w", err)
	}
	kh := KnownHostsPath()
	var hkCallback ssh.HostKeyCallback
	if _, err := os.Stat(kh); err == nil {
		hkCallback, err = knownhosts.New(kh)
		if err != nil {
			return nil, fmt.Errorf("load known_hosts: %w", err)
		}
	} else {
		return nil, fmt.Errorf("known_hosts not found at %s: trust host first (use `backupctl server test`)", kh)
	}
	cfg := &ssh.ClientConfig{
		User:            s.Username,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: hkCallback,
		Timeout:         15 * time.Second,
	}
	addr := fmt.Sprintf("%s:%d", s.Host, s.Port)
	conn, err := ssh.Dial("tcp", addr, cfg)
	if err != nil {
		return nil, fmt.Errorf("ssh dial %s: %w", addr, err)
	}
	return conn, nil
}

// DialInsecure is used only for initial trust flow (test with explicit confirm).
func DialInsecureTrust(s ServerInfo) (fingerprint string, hostKey ssh.PublicKey, err error) {
	key, err := os.ReadFile(s.KeyPath)
	if err != nil {
		return "", nil, err
	}
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		return "", nil, err
	}
	var captured ssh.PublicKey
	cfg := &ssh.ClientConfig{
		User: s.Username,
		Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: func(host string, ra net.Addr, k ssh.PublicKey) error {
			captured = k
			return fmt.Errorf("capture")
		},
		Timeout: 15 * time.Second,
	}
	addr := fmt.Sprintf("%s:%d", s.Host, s.Port)
	_, _ = ssh.Dial("tcp", addr, cfg)
	if captured == nil {
		return "", nil, fmt.Errorf("could not retrieve host key")
	}
	fp := ssh.FingerprintSHA256(captured)
	return fp, captured, nil
}

func TrustHost(host string, port int, key ssh.PublicKey) error {
	kh := KnownHostsPath()
	if err := os.MkdirAll(filepath.Dir(kh), 0o700); err != nil {
		return err
	}
	var hostport string
	if port == 22 {
		hostport = host
	} else {
		hostport = fmt.Sprintf("[%s]:%d", host, port)
	}
	line := knownhosts.Line([]string{hostport}, key) + "\n"
	f, err := os.OpenFile(kh, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.WriteString(f, line)
	return err
}

// Run executes argv on remote without shell interpolation. Returns stdout bytes streamed to w (if non-nil), stderr captured.
func Run(ctx context.Context, client *ssh.Client, argv []string, stdout io.Writer) (stderr string, err error) {
	if len(argv) == 0 {
		return "", fmt.Errorf("empty command")
	}
	sess, err := client.NewSession()
	if err != nil {
		return "", err
	}
	defer sess.Close()
	// Escape each arg safely for remote shell (no interpolation).
	var sb strings.Builder
	for i, a := range argv {
		if i > 0 {
			sb.WriteByte(' ')
		}
		sb.WriteString(shellEscape(a))
	}
	var errBuf bytes.Buffer
	sess.Stderr = &errBuf
	if stdout != nil {
		sess.Stdout = stdout
	} else {
		var out bytes.Buffer
		sess.Stdout = &out
	}
	done := make(chan error, 1)
	go func() { done <- sess.Run(sb.String()) }()
	select {
	case <-ctx.Done():
		_ = sess.Signal(ssh.SIGKILL)
		sess.Close()
		return errBuf.String(), ctx.Err()
	case err := <-done:
		return errBuf.String(), err
	}
}

// RunWithCombined runs and returns stdout bytes (for small outputs).
func RunWithCombined(ctx context.Context, client *ssh.Client, argv []string) (stdout []byte, stderr string, err error) {
	var out bytes.Buffer
	stderr, err = Run(ctx, client, argv, &out)
	return out.Bytes(), stderr, err
}

func shellEscape(s string) string {
	if s == "" {
		return "''"
	}
	// safe chars
	safe := true
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
			r == '_' || r == '-' || r == '.' || r == '/' || r == ':' || r == '=' || r == ',') {
			safe = false
			break
		}
	}
	if safe {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}
