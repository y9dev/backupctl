package version

// Version подставляется при релизной сборке:
// go build -ldflags "-X backupctl/internal/version.Version=1.0.0" ./cmd/backupctl/
var Version = "dev"
