package version

import (
	"runtime/debug"
	"testing"

	"github.com/stretchr/testify/assert"
)

func mockBuildInfo(t *testing.T, revision string, modified bool) {
	t.Helper()
	orig := readBuildInfo
	t.Cleanup(func() { readBuildInfo = orig })

	settings := []debug.BuildSetting{
		{Key: settingRevision, Value: revision},
	}
	if modified {
		settings = append(settings, debug.BuildSetting{Key: settingModified, Value: "true"})
	}
	readBuildInfo = func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{Settings: settings}, true
	}
}

func TestVersionFromVCSPrefixesShortRevisionWithG(t *testing.T) {
	mockBuildInfo(t, "1697674abcdef", false)
	assert.Equal(t, "g1697674", versionFromVCS())
}

func TestVersionFromVCSPrefixesDirtyRevisionWithG(t *testing.T) {
	mockBuildInfo(t, "1697674abcdef", true)
	assert.Equal(t, "g1697674-dirty", versionFromVCS())
}
