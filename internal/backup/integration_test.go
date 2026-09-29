//go:build integration

package backup

import (
	"context"
	"testing"
	"time"
)

func TestIntegrationPostgres(t *testing.T) {
	t.Skip("manual: configure BACKUPCTL_CONFIG with test server and run `backupctl run test pg` then `backupctl verify`")
	_ = context.Background
	_ = time.Now
}
