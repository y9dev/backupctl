package sshclient

import (
	"fmt"
	"strings"
)

// IsHostKeyError reports whether err comes from host key verification
// (as opposed to auth/network/key-file problems, which must not trigger
// the trust prompt).
func IsHostKeyError(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	for _, sub := range []string{
		"knownhosts", "known_hosts", "key is unknown",
		"key mismatch", "no matching host key", "host key",
	} {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// ConnectWithTrust dials the server. If the host key is unknown, it fetches
// the fingerprint via a handshake-only connection and asks allowTrust whether
// to save it to known_hosts and retry. A nil allowTrust disables the trust
// flow (plain dial error is returned).
func ConnectWithTrust(s ServerInfo, allowTrust func(fingerprint string) bool) error {
	client, err := Dial(s)
	if err == nil {
		client.Close()
		return nil
	}
	if !IsHostKeyError(err) || allowTrust == nil {
		return err
	}
	fp, key, terr := DialInsecureTrust(s)
	if terr != nil {
		return err
	}
	if !allowTrust(fp) {
		return fmt.Errorf("host key not trusted: %w", err)
	}
	if err := TrustHost(s.Host, s.Port, key); err != nil {
		return fmt.Errorf("save known_hosts: %w", err)
	}
	retry, err := Dial(s)
	if err != nil {
		return err
	}
	retry.Close()
	return nil
}
