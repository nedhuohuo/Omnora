package buildinfo

import "strings"

// Version is replaced through -ldflags for release images and update packages.
// Development binaries deliberately retain "dev" so downgrade enforcement is
// skipped when there is no trustworthy installed version identity.
var Version = "dev"

func CurrentVersion() string {
	version := strings.TrimSpace(Version)
	if version == "" {
		return "dev"
	}
	return version
}
