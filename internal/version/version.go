package version

import "runtime/debug"

const (
	defaultVersion  = "dev"
	shortHashLen    = 7
	settingRevision = "vcs.revision"
	settingModified = "vcs.modified"
	dirtySuffix     = "-dirty"
)

var readBuildInfo = debug.ReadBuildInfo

// Version is the build identifier shared by the CLI, daemon, and TUI.
// Release builds can override it with ldflags; development builds use
// Go's embedded VCS metadata.
var Version = defaultVersion

func init() {
	if Version != defaultVersion {
		return
	}
	Version = versionFromVCS()
}

func versionFromVCS() string {
	info, ok := readBuildInfo()
	if !ok {
		return defaultVersion
	}
	var rev string
	var dirty bool
	for _, s := range info.Settings {
		switch s.Key {
		case settingRevision:
			rev = s.Value
		case settingModified:
			dirty = s.Value == "true"
		}
	}
	if rev == "" {
		return defaultVersion
	}
	if len(rev) > shortHashLen {
		rev = rev[:shortHashLen]
	}
	rev = "g" + rev
	if dirty {
		rev += dirtySuffix
	}
	return rev
}
