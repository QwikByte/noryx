package docker

import (
	"fmt"
	"strconv"

	"github.com/moby/moby/api/types/container"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
)

// outputLimit is how much output the runtime keeps of a container: files of up to fileMB MB,
// of which it keeps files. 0 leaves it to the runtime's configuration.
type outputLimit struct{ fileMB, files uint32 }

// LimitLogs sets how much output the runtime keeps of each container created from now on.
// Containers keep the limit they were created with.
func (d *Docker) LimitLogs(fileMB, files uint32) { d.output.Store(&outputLimit{fileMB, files}) }

// logConfig returns how much output the runtime keeps of a new container, in json-file, which
// both runtimes read the last lines of and follow across rotations. Podman takes it as k8s-file,
// a single file that starts empty once it is full, so it gets the size of all files: the output
// takes as much space at most on both.
func (d *Docker) logConfig() container.LogConfig {
	l := d.output.Load()
	if l == nil { // until the agent applies the settings of the node
		defaults := noryxv1.DefaultNodeSettings()
		l = &outputLimit{defaults.GetLogFileMb(), defaults.GetLogFiles()}
	}
	switch {
	case l.files == 0:
		return container.LogConfig{}
	case d.podman != nil:
		return container.LogConfig{Type: "json-file", Config: map[string]string{"max-size": fmt.Sprintf("%dm", l.fileMB*l.files)}}
	}
	return container.LogConfig{Type: "json-file", Config: map[string]string{"max-size": fmt.Sprintf("%dm", l.fileMB), "max-file": strconv.Itoa(int(l.files))}}
}
