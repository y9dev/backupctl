package backup

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// IsLocal reports whether the job must run directly on this machine
// instead of over SSH.
func IsLocal(server Server) bool { return server.Local }

// runLocal executes argv on this machine, streaming stdout to w (if non-nil).
// Stderr is captured for error messages (never contains secrets by contract:
// callers must not put secret values into argv; PGPASSWORD is passed via env).
func runLocal(ctx context.Context, argv []string, stdout io.Writer) (string, error) {
	if len(argv) == 0 {
		return "", fmt.Errorf("empty command")
	}
	env, clean := splitEnvPrefix(argv)
	cmd := exec.CommandContext(ctx, clean[0], clean[1:]...)
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	if stdout != nil {
		cmd.Stdout = stdout
	} else {
		var out bytes.Buffer
		cmd.Stdout = &out
	}
	if err := cmd.Run(); err != nil {
		return errBuf.String(), err
	}
	return errBuf.String(), nil
}

// runLocalWithStdin executes argv on this machine, feeding stdin into the
// process stdin. Combined stderr is captured for error messages.
func runLocalWithStdin(ctx context.Context, argv []string, stdin io.Reader) (string, error) {
	if len(argv) == 0 {
		return "", fmt.Errorf("empty command")
	}
	env, clean := splitEnvPrefix(argv)
	cmd := exec.CommandContext(ctx, clean[0], clean[1:]...)
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	cmd.Stdin = stdin
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return errBuf.String(), err
	}
	return errBuf.String(), nil
}

// splitEnvPrefix handles argv built as ["env", "VAR=val", ...] (used for
// PGPASSWORD without leaking it into ps output on remote hosts). For local
// execution the VARs become process env instead of an `env` wrapper, so the
// value never appears in the local process table either.
func splitEnvPrefix(argv []string) (env []string, clean []string) {
	if len(argv) >= 3 && argv[0] == "env" {
		i := 1
		for ; i < len(argv); i++ {
			k, _, ok := strings.Cut(argv[i], "=")
			if !ok || k == "" || strings.Contains(k, " ") || strings.Contains(k, "/") {
				break
			}
			// Only treat leading VAR= assignments as env; stop at binary name.
			// Heuristic: value assignments contain "=" and key is all-caps/_/digits.
			if !isEnvKey(k) {
				break
			}
			env = append(env, argv[i])
		}
		clean = argv[i:]
		if len(clean) == 0 {
			return env, argv
		}
		return env, clean
	}
	return nil, argv
}

func isEnvKey(k string) bool {
	for _, r := range k {
		if !(r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_') {
			return false
		}
	}
	return true
}
