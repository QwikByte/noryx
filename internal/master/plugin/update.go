package plugin

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/master/httpapi"
	"github.com/QwikByte/noryx/internal/master/modrinth"
)

const (
	// maxChanges limits the versions whose changelogs are shown at once.
	maxChanges = 25
	// maxChangelog limits the changelog of a version, which its author writes, in characters.
	maxChangelog = 20_000
)

// Update updates the plugins or mods of servers, or only those of the given projects, to the
// newest version that suits each server in the channel it takes them in, releases unless
// chosen otherwise, together with the projects they newly require. Projects a server keeps at
// their version and turned-off files are left as they are. The result of each server tells
// the files it wrote, the pinned projects it left out and why projects failed.
func (s *Service) Update(ctx context.Context, servers []Ref, projects []string) []Result {
	run := &installation{Service: s, updates: true}
	return each(ctx, servers, func(ctx context.Context, res *Result) error { return run.update(ctx, res, projects) })
}

func (r *installation) update(ctx context.Context, res *Result, projects []string) error {
	o, err := r.open(ctx, res.Ref, true)
	if err != nil {
		return err
	}
	var outdated []string
	for _, f := range o.files {
		v := o.known[f.GetSha512()]
		switch {
		case f.GetDisabled() || !isNewer(o.newer.of(f.GetSha512(), o.kept.channel(v.ProjectID)), v) || projects != nil && !slices.Contains(projects, v.ProjectID):
		case o.kept.pinned[v.ProjectID]:
			res.Pinned = append(res.Pinned, r.title(ctx, v.ProjectID))
		case !slices.Contains(outdated, v.ProjectID):
			outdated = append(outdated, v.ProjectID)
		}
	}
	if len(outdated) == 0 {
		return nil
	}
	if o.present, err = r.present(ctx, o.files, o.known, slices.ContainsFunc(outdated, onHangar)); err != nil {
		return err
	}
	// Each project on its own, so that one that fails doesn't hold up the others.
	var failed []string
	for _, project := range outdated {
		installed, err := r.put(ctx, o, []string{project})
		for _, i := range installed {
			if i.written {
				res.Installed = append(res.Installed, i)
			}
		}
		if err != nil {
			failed = append(failed, fmt.Sprintf("%s: %s", r.title(ctx, project), httpapi.Message(err)))
		}
	}
	res.Restart = o.running && slices.ContainsFunc(res.Installed, Installed.loads)
	if len(failed) > 0 {
		return fmt.Errorf("%s", strings.Join(failed, " "))
	}
	return nil
}

// RemoveProject removes the files of a project from servers, the turned-off ones too.
func (s *Service) RemoveProject(ctx context.Context, project string, servers []Ref) []Result {
	run := &installation{Service: s}
	return each(ctx, servers, func(ctx context.Context, res *Result) error {
		o, err := run.open(ctx, res.Ref, false)
		if err != nil {
			return err
		}
		for _, f := range o.files {
			if o.known[f.GetSha512()].ProjectID != project {
				continue
			}
			if err := s.Remove(ctx, res.Ref, f.GetFileName(), f.GetDisabled()); err != nil {
				return err
			}
			res.Removed = append(res.Removed, f.GetFileName())
			res.Restart = res.Restart || o.running && !f.GetDisabled()
		}
		return nil
	})
}

// Change is a version of a project with what changed in it, as Markdown of its author.
type Change struct {
	Version
	Changelog string `json:"changelog"`
}

// Changes returns what changed in the versions of a project that run on servers of a type and
// Minecraft version, the newest first: those after the version from up to the version to, both
// optional, at most maxChanges. If from isn't among them, only to is.
func (s *Service) Changes(ctx context.Context, project string, typ noryxv1.ServerType, gameVersion, from, to string) ([]Change, error) {
	t, err := s.target(ctx, typ, gameVersion)
	if err != nil {
		return nil, err
	}
	found, err := s.catalogue.Changelogs(ctx, project, t.loaders, t.gameVersion)
	if err != nil {
		return nil, err
	}
	found = slices.DeleteFunc(found, func(v modrinth.Version) bool { return len(v.Files) == 0 })
	index := func(id string) int {
		return slices.IndexFunc(found, func(v modrinth.Version) bool { return v.ID == id })
	}
	start, end := 0, len(found)
	if to != "" {
		start = max(0, index(to))
	}
	if i := index(from); from != "" && i > start {
		end = i
	} else if from != "" {
		end = min(end, start+1)
	}
	changes := []Change{}
	for _, v := range found[start:min(end, start+maxChanges)] {
		changelog := v.Changelog
		if utf8.RuneCountInString(changelog) > maxChangelog {
			changelog = string([]rune(changelog)[:maxChangelog]) + "…"
		}
		changes = append(changes, Change{Version: toVersion(v), Changelog: changelog})
	}
	return changes, nil
}
