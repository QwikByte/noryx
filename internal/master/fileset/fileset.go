// Package fileset keeps file sets: named, versioned collections of files, such as the
// configuration of a plugin or an image, that the master puts on the servers of tags and
// networks. It fills in the variables of each server and of the set, and where the server
// reaches the databases of its network; the agents fill in the secrets of a set and the
// passwords of databases, which the API never returns, and hide the files that hold them.
package fileset

import (
	"cmp"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/master/httpapi"
	"github.com/QwikByte/noryx/internal/master/tag"
)

const (
	maxDescription = 500
	maxTargets     = 50
	// keepVersions is how many versions of a set are kept.
	keepVersions = 20

	// Kinds of targets, and of the servers that a value of a variable is for.
	KindTag     = "tag"
	KindNetwork = "network"
	KindServer  = "server"
	KindAll     = "all"
	// Roles of the servers of a network that a set is for.
	RoleServers = "servers"
	RoleProxy   = "proxy"
)

// File is a file of a set, at a path relative to the data of a server: text, or binary data.
type File struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	// Data is the content of a binary file, e.g. an image, in which nothing is filled in.
	Data []byte `json:"data,omitempty"`
	// OnlyIfMissing writes the file only to servers that don't have it, for files that
	// plugins rewrite.
	OnlyIfMissing bool `json:"onlyIfMissing"`
}

// Target names servers that a set is for: those with a tag, or the game servers or the
// proxy of a network.
type Target struct {
	Kind  string `json:"kind"`
	Value string `json:"value"` // the tag, or the ID of the network
	Role  string `json:"role"`  // for networks
}

// Secret is a secret of a set; its value never leaves the master but to the agents.
type Secret struct {
	Name      string    `json:"name"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Version is a saved state of the files of a set.
type Version struct {
	Version   int64     `json:"version"`
	User      string    `json:"user"`
	CreatedAt time.Time `json:"createdAt"`
	Files     []File    `json:"files,omitempty"`
}

type Set struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Version     int64      `json:"version"`
	Files       []File     `json:"files"`
	Targets     []Target   `json:"targets"`
	Variables   []Variable `json:"variables"`
	Secrets     []Secret   `json:"secrets"`
	// Versions are the kept versions, newest first, without their files.
	Versions  []Version `json:"versions"`
	CreatedAt time.Time `json:"createdAt"`
}

// Summary describes a set in the list of sets.
type Summary struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Version     int64     `json:"version"`
	Paths       []string  `json:"paths"`
	Targets     []Target  `json:"targets"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// Input is a new set, or a change of one.
type Input struct {
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Files       []File     `json:"files"`
	Targets     []Target   `json:"targets"`
	Variables   []Variable `json:"variables"`
	// Version is the version a change is based on; if the set has a newer one, the change
	// is refused, so that it doesn't undo what someone else saved.
	Version int64 `json:"version"`
}

// check validates a set and puts it into its canonical form. Targets and values of networks
// that don't exist, e.g. as they were deleted, are left out.
func (in *Input) check(exists func(network string) bool) error {
	in.Name, in.Description = strings.TrimSpace(in.Name), strings.TrimSpace(in.Description)
	switch {
	case in.Name == "" || len(in.Name) > 64 || strings.ContainsFunc(in.Name, unicode.IsControl):
		return httpapi.FieldErrorf(http.StatusBadRequest, "name", "Enter a name with up to 64 characters.")
	case len(in.Description) > maxDescription:
		return httpapi.FieldErrorf(http.StatusBadRequest, "description", "Keep the description below %d characters.", maxDescription)
	case len(in.Files) > noryxv1.MaxFileSetFiles:
		return httpapi.FieldErrorf(http.StatusBadRequest, "files", "A file set has up to %d files.", noryxv1.MaxFileSetFiles)
	case len(in.Targets) > maxTargets:
		return httpapi.FieldErrorf(http.StatusBadRequest, "targets", "A file set has up to %d targets.", maxTargets)
	}
	size := 0
	for i, f := range in.Files {
		p, problem := noryxv1.CleanFileSetPath(f.Path)
		if size += len(f.Content) + len(f.Data); problem == "" && size > noryxv1.MaxFileSetSize {
			problem = fmt.Sprintf("A file set has up to %d MiB.", noryxv1.MaxFileSetSize>>20)
		}
		switch {
		case len(f.Data) == 0:
			problem = cmp.Or(problem, noryxv1.FileSetContentProblem(p, f.Content), placeholderProblem(p, f.Content))
			in.Files[i].Data = nil
		case f.Content != "":
			problem = cmp.Or(problem, p+" is text and binary at once.")
		default:
			problem = cmp.Or(problem, noryxv1.FileSetDataProblem(p, f.Data))
		}
		if problem == "" && slices.ContainsFunc(in.Files[:i], func(o File) bool { return o.Path == p }) {
			problem = p + " is there twice."
		}
		if problem != "" {
			return httpapi.FieldErrorf(http.StatusBadRequest, "files", "%s", problem)
		}
		in.Files[i].Path = p
	}
	slices.SortFunc(in.Files, func(a, b File) int { return strings.Compare(a.Path, b.Path) })
	for i, t := range in.Targets {
		switch t.Kind {
		case KindTag:
			tags, err := tag.Normalize([]string{t.Value})
			if err != nil {
				return httpapi.Within("targets", err)
			}
			in.Targets[i].Value, in.Targets[i].Role = tags[0], ""
		case KindNetwork:
			if t.Role != RoleServers && t.Role != RoleProxy {
				return httpapi.FieldErrorf(http.StatusBadRequest, "targets", "Choose the game servers or the proxy of a network.")
			}
		default:
			return httpapi.FieldErrorf(http.StatusBadRequest, "targets", "Choose a tag or a network as target.")
		}
	}
	in.Targets = slices.DeleteFunc(in.Targets, func(t Target) bool { return t.Kind == KindNetwork && !exists(t.Value) })
	slices.SortFunc(in.Targets, compareTargets)
	in.Targets = slices.Compact(in.Targets)
	if err := checkVariables(in.Variables, exists); err != nil {
		return httpapi.Within("variables", err)
	}
	slices.SortFunc(in.Variables, func(a, b Variable) int { return strings.Compare(a.Name, b.Name) })
	if in.Files == nil {
		in.Files = []File{}
	}
	if in.Variables == nil {
		in.Variables = []Variable{}
	}
	return nil
}

func compareTargets(a, b Target) int {
	return cmp.Or(strings.Compare(a.Kind, b.Kind), strings.Compare(a.Value, b.Value), strings.Compare(a.Role, b.Role))
}
