// Package buildinfo exposes metadata injected at build time.
package buildinfo

import "regexp"

// Repository publishes the releases.
const Repository = "https://github.com/QwikByte/noryx"

// Version is set via -ldflags "-X github.com/QwikByte/noryx/internal/buildinfo.Version=v1.0.0".
var Version = "dev"

// release matches the versions of releases, like the installer and the release workflow do:
// pre-releases end in -alpha.N, -beta.N or -rc.N. Builds of a commit after a tag, of changed
// files (-dirty) or for development (-dev) aren't releases.
var release = regexp.MustCompile(`^v\d+\.\d+\.\d+(-(alpha|beta|rc)\.\d+)?$`)

// IsRelease tells whether v is the version of a release, e.g. v1.2.0 or v1.3.0-rc.1.
func IsRelease(v string) bool { return release.MatchString(v) }

// InstallScript is the URL of the installer of this version, or of the latest release for
// builds that aren't one.
func InstallScript() string {
	if IsRelease(Version) {
		return Repository + "/releases/download/" + Version + "/install.sh"
	}
	return Repository + "/releases/latest/download/install.sh"
}
