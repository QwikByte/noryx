package plugin

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"slices"
	"strings"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/agent/datadir"
)

const (
	// maxJarRead limits what is read of a jar to find the name of its plugin: its index and the
	// file that names it. The server writes its jars, so it could make an index huge.
	maxJarRead = 4 << 20
	// maxDescriptor limits the file that names a plugin, which has a few kilobytes.
	maxDescriptor = 64 << 10
)

var errBudget = errors.New("read too much of the jar")

// descriptors returns the files of a plugin's jar that name the plugin on servers of a type, the
// one the server reads first. Mods have none, as they keep their settings elsewhere.
func descriptors(t noryxv1.ServerType) []string {
	switch {
	case t == noryxv1.ServerType_SERVER_TYPE_VELOCITY:
		return []string{"velocity-plugin.json"}
	case t.Bungee():
		return []string{"bungee.yml", "plugin.yml"}
	case folders[t] == "plugins":
		return []string{"paper-plugin.yml", "plugin.yml"}
	}
	return nil
}

// pluginName returns the name a plugin gives itself in the first of descriptors its jar has,
// which also names the folder of its settings, or "". It reads at most maxJarRead bytes of the
// jar, and only accepts a name that can be such a folder.
func pluginName(jar io.ReaderAt, size int64, descriptors []string) string {
	if len(descriptors) == 0 {
		return ""
	}
	zr, err := zip.NewReader(&budget{jar, maxJarRead}, size)
	if err != nil {
		return ""
	}
	for _, d := range descriptors {
		i := slices.IndexFunc(zr.File, func(f *zip.File) bool { return f.Name == d })
		if i < 0 {
			continue
		}
		r, err := zr.File[i].Open()
		if err != nil {
			return ""
		}
		data, err := io.ReadAll(io.LimitReader(r, maxDescriptor+1))
		r.Close()
		if name := nameIn(d, data); err == nil && len(data) <= maxDescriptor && noryxv1.ValidPluginFolder(name) {
			return name
		}
		return ""
	}
	return ""
}

// nameIn returns the name in a descriptor: the id of velocity-plugin.json, or the top-level
// name of a YAML file, which is read line by line rather than parsed.
func nameIn(descriptor string, data []byte) string {
	if strings.HasSuffix(descriptor, ".json") {
		var d struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(data, &d)
		return d.ID
	}
	for line := range strings.Lines(strings.TrimPrefix(string(data), "\uFEFF")) {
		if value, ok := strings.CutPrefix(line, "name:"); ok {
			value, _, _ = strings.Cut(value, " #")
			value = strings.TrimSpace(value)
			if len(value) >= 2 && strings.ContainsRune(`"'`, rune(value[0])) && value[len(value)-1] == value[0] {
				value = value[1 : len(value)-1]
			}
			return value
		}
	}
	return ""
}

// settings returns name if it is a folder in the plugin folder, the folder of a plugin's
// settings, or "".
func settings(dir *datadir.Dir, folder, name string) string {
	if name == "" {
		return ""
	}
	if info, err := dir.Lstat(filepath.Join(folder, name)); err != nil || !info.IsDir() {
		return ""
	}
	return name
}

// budget reads at most n bytes of r in all.
type budget struct {
	r io.ReaderAt
	n int64
}

func (b *budget) ReadAt(p []byte, off int64) (int, error) {
	if int64(len(p)) > b.n {
		return 0, errBudget
	}
	b.n -= int64(len(p))
	return b.r.ReadAt(p, off)
}
