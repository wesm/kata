package version

import (
	"runtime/debug"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestVersionFromVCSPrefixesShortRevisionWithG(t *testing.T) {
	origReadBuildInfo := readBuildInfo
	defer func() { readBuildInfo = origReadBuildInfo }()

	readBuildInfo = func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{Settings: []debug.BuildSetting{
			{Key: settingRevision, Value: "1697674abcdef"},
		}}, true
	}

	assert.Equal(t, "g1697674", versionFromVCS())
}

func TestVersionFromVCSPrefixesDirtyRevisionWithG(t *testing.T) {
	origReadBuildInfo := readBuildInfo
	defer func() { readBuildInfo = origReadBuildInfo }()

	readBuildInfo = func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{Settings: []debug.BuildSetting{
			{Key: settingRevision, Value: "1697674abcdef"},
			{Key: settingModified, Value: "true"},
		}}, true
	}

	assert.Equal(t, "g1697674-dirty", versionFromVCS())
}
