package plugin

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/master/geysermc"
	"github.com/QwikByte/noryx/internal/master/hangar"
	"github.com/QwikByte/noryx/internal/master/modrinth"
)

const (
	// hangarLookups is how many files are identified on Hangar at once, one request each.
	hangarLookups = 4
	// geysermcLookup is how long identifying Floodgate may take.
	geysermcLookup = 10 * time.Second
)

// catalogue finds plugins and mods on Modrinth, plugins on Hangar, whose IDs start with
// hangar.Prefix, and Floodgate on GeyserMC's download server, which isn't searched.
type catalogue struct {
	modrinth *modrinth.Client
	hangar   *hangar.Client
	geysermc *geysermc.Client
}

// ValidProjectID reports whether id is a well-formed ID of a project or version of Modrinth,
// Hangar or GeyserMC.
func ValidProjectID(id string) bool {
	return modrinth.ValidProjectID(id) || hangar.ValidID(id) || geysermc.ValidID(id)
}

func onHangar(id string) bool { return strings.HasPrefix(id, hangar.Prefix) }

func onGeyserMC(id string) bool { return strings.HasPrefix(id, geysermc.Prefix) }

// Projects returns the projects with the given IDs; unknown ones are left out.
func (c catalogue) Projects(ctx context.Context, ids []string) ([]modrinth.Project, error) {
	var onModrinth, others []string
	var fixed []modrinth.Project
	for _, id := range ids {
		switch p, ok := geysermc.Project(id); {
		case ok:
			fixed = append(fixed, p)
		case onHangar(id):
			others = append(others, id)
		default:
			onModrinth = append(onModrinth, id)
		}
	}
	projects, err := c.modrinth.Projects(ctx, onModrinth)
	if err != nil {
		return projects, err
	}
	more, err := c.hangar.Projects(ctx, others)
	return slices.Concat(projects, more, fixed), err
}

// Versions returns the versions of a project for loaders and, unless empty, a Minecraft
// version, the newest first.
func (c catalogue) Versions(ctx context.Context, project string, loaders []string, gameVersion string) ([]modrinth.Version, error) {
	switch {
	case onHangar(project):
		return c.hangar.Versions(ctx, project, loaders, gameVersion)
	case onGeyserMC(project):
		return c.geysermc.Latest(ctx, project, loaders)
	}
	return c.modrinth.Versions(ctx, project, loaders, gameVersion)
}

// Changelogs returns the versions of a project like Versions, with what changed in each, as
// far as the catalogue tells: Hangar's versions have it always, GeyserMC's never.
func (c catalogue) Changelogs(ctx context.Context, project string, loaders []string, gameVersion string) ([]modrinth.Version, error) {
	if onHangar(project) || onGeyserMC(project) {
		return c.Versions(ctx, project, loaders, gameVersion)
	}
	return c.modrinth.Changelogs(ctx, project, loaders, gameVersion)
}

// Download fetches a file of a version and checks its hash.
func (c catalogue) Download(ctx context.Context, f modrinth.File) ([]byte, error) {
	switch {
	case c.geysermc.OnServer(f.URL):
		return c.geysermc.Download(ctx, f)
	case f.Hashes.SHA256 != "":
		return c.hangar.Download(ctx, f)
	}
	return c.modrinth.Download(ctx, f)
}

// identify returns the version of each file known to Modrinth, Hangar or GeyserMC, by the
// file's SHA-512 hash, and the newest version of its project for a server in each of channels.
func (c catalogue) identify(ctx context.Context, files []*noryxv1.PluginFile, t target, channels []string) (known map[string]modrinth.Version, newer updates, err error) {
	hashes := make([]string, len(files))
	for i, f := range files {
		hashes[i] = f.GetSha512()
	}
	if known, err = c.modrinth.VersionsByHash(ctx, hashes); err != nil {
		return nil, nil, err
	}
	newer = updates{}
	for _, channel := range channels {
		found, err := c.modrinth.Updates(ctx, hashes, t.loaders, t.gameVersion, versionTypes(channel))
		if err != nil {
			return nil, nil, err
		}
		for hash, v := range found {
			newer.add(hash, channel, v)
		}
	}
	if hangar.Platform(t.loaders) == "" {
		return known, newer, nil
	}
	unknown := slices.DeleteFunc(slices.Clone(files), func(f *noryxv1.PluginFile) bool {
		_, ok := known[f.GetSha512()]
		return ok || f.GetSha256() == ""
	})
	var (
		mu    sync.Mutex
		wg    sync.WaitGroup
		slots = make(chan struct{}, hangarLookups)
	)
	for _, f := range unknown {
		slots <- struct{}{}
		wg.Go(func() {
			defer func() { <-slots }()
			v, newest, lookupErr := c.identifyOnHangar(ctx, f.GetSha256(), t, channels)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case lookupErr != nil:
				err = lookupErr
			case v.ProjectID != "":
				known[f.GetSha512()] = v
				for channel, u := range newest {
					newer.add(f.GetSha512(), channel, u)
				}
			}
		})
	}
	wg.Wait()
	unknown = slices.DeleteFunc(unknown, func(f *noryxv1.PluginFile) bool { _, ok := known[f.GetSha512()]; return ok })
	if len(unknown) > 0 {
		c.identifyOnGeyserMC(ctx, unknown, t, known)
	}
	return known, newer, err
}

// identifyOnGeyserMC recognises the newest build of Floodgate among files, at best: GeyserMC's
// download server only tells its newest builds, and a slow one mustn't hold up the listing.
func (c catalogue) identifyOnGeyserMC(ctx context.Context, files []*noryxv1.PluginFile, t target, known map[string]modrinth.Version) {
	ctx, cancel := context.WithTimeout(ctx, geysermcLookup)
	defer cancel()
	floodgate, _ := c.geysermc.Latest(ctx, geysermc.Floodgate, t.loaders)
	for _, f := range files {
		if len(floodgate) > 0 && floodgate[0].Files[0].Hashes.SHA256 == f.GetSha256() {
			known[f.GetSha512()] = floodgate[0]
		}
	}
}

// identifyOnHangar returns the version of a file on Hangar among the newest of its project,
// or only the project if it is older, and the newest version for the server in each of
// channels, by channel.
func (c catalogue) identifyOnHangar(ctx context.Context, sha256 string, t target, channels []string) (v modrinth.Version, newest map[string]modrinth.Version, err error) {
	project, err := c.hangar.ProjectByHash(ctx, sha256)
	if project == "" || err != nil {
		return v, nil, err
	}
	versions, err := c.hangar.Versions(ctx, project, t.loaders, "")
	if err != nil {
		return v, nil, err
	}
	v = modrinth.Version{ProjectID: project}
	if i := slices.IndexFunc(versions, func(v modrinth.Version) bool { return len(v.Files) > 0 && v.Files[0].Hashes.SHA256 == sha256 }); i >= 0 {
		v = versions[i]
	}
	newest = map[string]modrinth.Version{}
	for _, channel := range channels {
		if i := slices.IndexFunc(versions, func(v modrinth.Version) bool {
			return takes(channel, v) && len(v.Files) > 0 && (t.gameVersion == "" || slices.Contains(v.GameVersions, t.gameVersion))
		}); i >= 0 {
			newest[channel] = versions[i]
		}
	}
	return v, newest, nil
}
