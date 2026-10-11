package plugin

import (
	"maps"
	"net/http"
	"slices"

	"github.com/QwikByte/noryx/internal/master/httpapi"
	"github.com/QwikByte/noryx/internal/master/modrinth"
)

// Channels are the channels a server takes a project in, from the most stable: release takes
// only releases, beta betas too and alpha every version. They are Modrinth's types of
// versions; Hangar's channels other than Release and Beta count as alpha.
var Channels = []string{"release", "beta", "alpha"}

var errChannel = httpapi.Errorf(http.StatusBadRequest, "Choose the channel release, beta or alpha.")

// ValidChannel reports whether c is one of Channels.
func ValidChannel(c string) bool { return slices.Contains(Channels, c) }

// versionTypes returns the types of the versions a channel takes.
func versionTypes(channel string) []string { return Channels[:slices.Index(Channels, channel)+1] }

// takes reports whether a channel takes a version.
func takes(channel string, v modrinth.Version) bool {
	return slices.Contains(versionTypes(channel), v.VersionType)
}

// inUse returns the channels that servers take their projects in: release, and the others
// they keep.
func inUse(servers ...kept) []string {
	in := []string{"release"}
	for _, k := range servers {
		in = slices.AppendSeq(in, maps.Values(k.channels))
	}
	return slices.Compact(slices.Sorted(slices.Values(in)))
}

// updates are the newest versions of the projects of files for a server, by the SHA-512 hash
// of a file and the channel they are the newest in.
type updates map[string]map[string]modrinth.Version

// of returns the newest version of the project of a file in a channel, if it is known.
func (u updates) of(sha512, channel string) modrinth.Version { return u[sha512][channel] }

func (u updates) add(sha512, channel string, v modrinth.Version) {
	if u[sha512] == nil {
		u[sha512] = map[string]modrinth.Version{}
	}
	u[sha512][channel] = v
}

// checkChannels checks the channels chosen for the projects to install, by project ID.
func checkChannels(projects []string, channels map[string]string) error {
	for project, channel := range channels {
		if !slices.Contains(projects, project) || !ValidChannel(channel) {
			return httpapi.Errorf(http.StatusBadRequest, "Choose the channel release, beta or alpha, and only for the projects to install.")
		}
	}
	return nil
}
