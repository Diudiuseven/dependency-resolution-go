package resolve

import "github.com/Diudiuseven/dependency-resolution-go/semver"

// workspaceMemberVersion mirrors workspace.workspaceVersion; kept here to
// avoid an import cycle-free helper duplication concern.
func workspaceMemberVersion() semver.Version {
	return semver.MustParse("0.0.0-workspace")
}
