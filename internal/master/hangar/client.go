// Package hangar is a client for Hangar (hangar.papermc.io), the plugin repository of
// PaperMC, for plugins of Paper and its forks, Velocity and Waterfall. It returns projects
// and versions in the shape of Modrinth's, which the plugin manager works with; their IDs
// start with Prefix, which tells them apart from Modrinth's.
package hangar

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QwikByte/noryx/internal/buildinfo"
	"github.com/QwikByte/noryx/internal/master/httpapi"
	"github.com/QwikByte/noryx/internal/master/modrinth"
)

const (
	DefaultAPI = "https://hangar.papermc.io/api/v1"
	DefaultCDN = "https://hangarcdn.papermc.io/"
	// Prefix starts the IDs of Hangar's projects and versions, e.g. hangar-31.
	Prefix = "hangar-"

	maxResponseBytes = 16 << 20
	maxIconBytes     = 1 << 20
	pageSize         = 20
	// maxVersions are the newest versions looked at, e.g. to find the one of a file.
	maxVersions = 25
	// maxCached projects are kept; the cache starts anew beyond.
	maxCached = 1000
)

var (
	id       = regexp.MustCompile(`^` + Prefix + `[0-9]{1,12}$`)
	iconName = regexp.MustCompile(`^[0-9]{1,12}\.(webp|png|jpe?g)$`)
	// platforms are Hangar's platforms of Modrinth's loaders.
	platforms = map[string]string{"paper": "PAPER", "velocity": "VELOCITY", "waterfall": "WATERFALL", "bungeecord": "WATERFALL"}
	// sorts are Hangar's orders of Modrinth's, the largest first.
	sorts      = map[string]string{"downloads": "-downloads", "follows": "-stars", "newest": "-newest", "updated": "-updated"}
	iconTypes  = map[string]string{"webp": "image/webp", "png": "image/png", "jpg": "image/jpeg", "jpeg": "image/jpeg"}
	errUnknown = httpapi.Errorf(http.StatusNotFound, "This project doesn't exist on Hangar.")
)

// ValidID reports whether id is a well-formed ID of a project or version of Hangar.
func ValidID(s string) bool { return id.MatchString(s) }

// Platform returns Hangar's platform of the first of loaders it has, or "".
func Platform(loaders []string) string {
	for _, l := range loaders {
		if p := platforms[l]; p != "" {
			return p
		}
	}
	return ""
}

type project struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Namespace struct {
		Owner string `json:"owner"`
		Slug  string `json:"slug"`
	} `json:"namespace"`
	Stats struct {
		Downloads int64 `json:"downloads"`
		Stars     int64 `json:"stars"`
	} `json:"stats"`
	Category           string              `json:"category"`
	Description        string              `json:"description"`
	LastUpdated        time.Time           `json:"lastUpdated"`
	SupportedPlatforms map[string][]string `json:"supportedPlatforms"`
	AvatarURL          string              `json:"avatarUrl"`
}

// loaders returns Modrinth's loaders of the platforms of a project.
func (p project) loaders() []string {
	var out []string
	for l, platform := range platforms {
		if _, ok := p.SupportedPlatforms[platform]; ok {
			out = append(out, l)
		}
	}
	slices.Sort(out)
	return out
}

func (p project) toModrinth() modrinth.Project {
	return modrinth.Project{
		ID: Prefix + strconv.FormatInt(p.ID, 10), Slug: p.Namespace.Owner + "/" + p.Namespace.Slug, Title: p.Name,
		Description: p.Description, IconURL: p.AvatarURL, Downloads: p.Stats.Downloads, Loaders: p.loaders(),
	}
}

type version struct {
	ID          int64     `json:"id"`
	ProjectID   int64     `json:"projectId"`
	Name        string    `json:"name"`
	Description string    `json:"description"` // the changelog, as Markdown
	CreatedAt   time.Time `json:"createdAt"`
	Channel     struct {
		Name string `json:"name"`
	} `json:"channel"`
	Downloads map[string]struct {
		FileInfo *struct {
			Name   string `json:"name"`
			Size   int64  `json:"sizeBytes"`
			SHA256 string `json:"sha256Hash"`
		} `json:"fileInfo"`
		DownloadURL string `json:"downloadUrl"`
	} `json:"downloads"`
	PluginDependencies map[string][]struct {
		ProjectID *int64 `json:"projectId"` // unset for plugins elsewhere than on Hangar
		Required  bool   `json:"required"`
	} `json:"pluginDependencies"`
	PlatformDependencies map[string][]string `json:"platformDependencies"`
}

// toModrinth returns a version for a platform; versions of other places than Hangar have no files.
func (v version) toModrinth(platform, loader string) modrinth.Version {
	out := modrinth.Version{
		ID: Prefix + strconv.FormatInt(v.ID, 10), ProjectID: Prefix + strconv.FormatInt(v.ProjectID, 10), VersionNumber: v.Name,
		VersionType: channel(v.Channel.Name), Published: v.CreatedAt, GameVersions: v.PlatformDependencies[platform], Loaders: []string{loader},
		Changelog: v.Description,
	}
	if d := v.Downloads[platform]; d.FileInfo != nil {
		f := modrinth.File{URL: d.DownloadURL, Filename: d.FileInfo.Name, Primary: true, Size: d.FileInfo.Size}
		f.Hashes.SHA256 = d.FileInfo.SHA256
		out.Files = []modrinth.File{f}
	}
	for _, d := range v.PluginDependencies[platform] {
		if d.Required && d.ProjectID != nil {
			out.Dependencies = append(out.Dependencies, modrinth.Dependency{ProjectID: Prefix + strconv.FormatInt(*d.ProjectID, 10), Type: "required"})
		}
	}
	return out
}

// channel returns Modrinth's type of a version in a channel: Release, Beta or anything else.
func channel(name string) string {
	switch strings.ToLower(name) {
	case "release":
		return "release"
	case "beta":
		return "beta"
	}
	return "alpha"
}

type Client struct {
	api, cdn string
	http     *http.Client

	mu       sync.Mutex
	projects map[string]project // by ID
}

// New returns a client for the API and CDN at the given base URLs. Calls of the API get
// modrinth.Timeout, downloads modrinth.DownloadTimeout, through their context.
func New(api, cdn string) *Client {
	return &Client{api: api, cdn: cdn, http: &http.Client{CheckRedirect: httpapi.SameOrigin}, projects: map[string]project{}}
}

// Search finds plugins for the platforms of the loaders; categories and the Minecraft
// versions of proxies don't narrow it.
func (c *Client) Search(ctx context.Context, s modrinth.Search) (modrinth.SearchResult, error) {
	res := modrinth.SearchResult{Hits: []modrinth.SearchHit{}}
	var found []string
	for _, l := range s.Loaders {
		if p := platforms[l]; p != "" && !slices.Contains(found, p) {
			found = append(found, p)
		}
	}
	if len(found) == 0 || s.Kind == "mods" || s.Kind == "modpacks" {
		return res, nil
	}
	q := url.Values{"limit": {strconv.Itoa(pageSize)}, "offset": {strconv.Itoa(s.Offset)}}
	platform := ""
	if len(found) == 1 {
		platform = found[0]
		q.Set("platform", platform)
	}
	if s.Query != "" {
		q.Set("query", s.Query)
	}
	if sort := sorts[s.Sort]; sort != "" || s.Query == "" {
		q.Set("sort", cmp.Or(sort, sorts["downloads"]))
	}
	if s.GameVersion != "" && platform == "PAPER" {
		q.Set("version", s.GameVersion)
	}
	var page struct {
		Pagination struct {
			Count int `json:"count"`
		} `json:"pagination"`
		Result []project `json:"result"`
	}
	if err := c.call(ctx, "/projects?"+q.Encode(), &page); err != nil {
		return res, err
	}
	res.Total = page.Pagination.Count
	for _, p := range page.Result {
		m := p.toModrinth()
		res.Hits = append(res.Hits, modrinth.SearchHit{
			ProjectID: m.ID, Slug: m.Slug, Title: m.Title, Description: p.Description, Author: p.Namespace.Owner, IconURL: p.AvatarURL,
			Downloads: p.Stats.Downloads, Follows: p.Stats.Stars, Updated: p.LastUpdated, ClientSide: "unsupported",
			Categories: m.Loaders, DisplayCategories: []string{p.Category},
		})
	}
	return res, nil
}

// Projects returns the projects with the given IDs; unknown ones are left out.
func (c *Client) Projects(ctx context.Context, ids []string) ([]modrinth.Project, error) {
	projects := []modrinth.Project{}
	for _, id := range ids {
		p, err := c.project(ctx, id)
		if errors.Is(err, errUnknown) {
			continue
		}
		if err != nil {
			return projects, err
		}
		projects = append(projects, p.toModrinth())
	}
	return projects, nil
}

func (c *Client) project(ctx context.Context, id string) (project, error) {
	c.mu.Lock()
	p, ok := c.projects[id]
	c.mu.Unlock()
	if ok {
		return p, nil
	}
	if !ValidID(id) {
		return p, errUnknown
	}
	if err := c.call(ctx, "/projects/"+strings.TrimPrefix(id, Prefix), &p); err != nil {
		return p, err
	}
	c.remember(id, p)
	return p, nil
}

// remember caches a project, as files are identified by their project.
func (c *Client) remember(id string, p project) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.projects) >= maxCached {
		clear(c.projects)
	}
	c.projects[id] = p
}

// Versions returns the newest versions of a project for the platform of the first loader
// Hangar has and, unless empty, Minecraft version, the newest first.
func (c *Client) Versions(ctx context.Context, project string, loaders []string, gameVersion string) ([]modrinth.Version, error) {
	platform := Platform(loaders)
	if !ValidID(project) || platform == "" {
		return nil, errUnknown
	}
	q := url.Values{"platform": {platform}, "limit": {strconv.Itoa(maxVersions)}}
	if gameVersion != "" && platform == "PAPER" {
		q.Set("platformVersion", gameVersion)
	}
	var page struct {
		Result []version `json:"result"`
	}
	if err := c.call(ctx, "/projects/"+strings.TrimPrefix(project, Prefix)+"/versions?"+q.Encode(), &page); err != nil {
		return nil, err
	}
	loader := loaders[slices.IndexFunc(loaders, func(l string) bool { return platforms[l] == platform })]
	versions := make([]modrinth.Version, 0, len(page.Result))
	for _, v := range page.Result {
		versions = append(versions, v.toModrinth(platform, loader))
	}
	slices.SortStableFunc(versions, func(a, b modrinth.Version) int { return b.Published.Compare(a.Published) })
	return versions, nil
}

// ProjectByHash returns the ID of the project of the file with a SHA-256 hash, or "" if
// Hangar doesn't know it.
func (c *Client) ProjectByHash(ctx context.Context, sha256 string) (string, error) {
	if len(sha256) != 64 {
		return "", nil
	}
	var p project
	err := c.call(ctx, "/versions/hash/"+url.PathEscape(sha256), &p)
	if errors.Is(err, errUnknown) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	id := p.toModrinth().ID
	c.remember(id, p)
	return id, nil
}

// OnCDN reports whether a file is on Hangar's CDN, from which Download downloads.
func (c *Client) OnCDN(fileURL string) bool { return strings.HasPrefix(fileURL, c.cdn) }

// Download fetches a file from the CDN and checks its size and hash.
func (c *Client) Download(ctx context.Context, f modrinth.File) ([]byte, error) {
	if !c.OnCDN(f.URL) || f.Size > modrinth.MaxFileSize || len(f.Hashes.SHA256) != sha256.Size*2 {
		return nil, httpapi.Errorf(http.StatusBadGateway, "%s can't be downloaded: it isn't on Hangar's CDN or is larger than %d MB.", f.Filename, modrinth.MaxFileSize>>20)
	}
	data, err := c.fetch(ctx, f.URL, modrinth.MaxFileSize, modrinth.DownloadTimeout(f.Size))
	if err != nil {
		return nil, err
	}
	if sum := sha256.Sum256(data); hex.EncodeToString(sum[:]) != strings.ToLower(f.Hashes.SHA256) {
		return nil, httpapi.Errorf(http.StatusBadGateway, "The download of %s is corrupted. Try again.", f.Filename)
	}
	return data, nil
}

// IconName returns the name of a project icon on the CDN, e.g. 31.webp, or "" for icons elsewhere.
func (c *Client) IconName(iconURL string) string {
	name, ok := strings.CutPrefix(iconURL, c.cdn+"avatars/project/")
	name, _, _ = strings.Cut(name, "?")
	if !ok || !iconName.MatchString(name) {
		return ""
	}
	return name
}

// Icon fetches a project icon by its name, see IconName, and returns its media type.
func (c *Client) Icon(ctx context.Context, name string) ([]byte, string, error) {
	if !iconName.MatchString(name) {
		return nil, "", httpapi.Errorf(http.StatusNotFound, "Icon not found.")
	}
	data, err := c.fetch(ctx, c.cdn+"avatars/project/"+name, maxIconBytes, modrinth.Timeout)
	return data, iconTypes[name[strings.LastIndexByte(name, '.')+1:]], err
}

func (c *Client) call(ctx context.Context, path string, out any) error {
	ctx, cancel := context.WithTimeout(ctx, modrinth.Timeout)
	defer cancel()
	res, err := c.do(ctx, c.api+path)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if err := json.NewDecoder(io.LimitReader(res.Body, maxResponseBytes)).Decode(out); err != nil {
		return unavailable(err)
	}
	return nil
}

func (c *Client) fetch(ctx context.Context, fileURL string, limit int64, timeout time.Duration) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	res, err := c.do(ctx, fileURL)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, io.LimitReader(res.Body, limit+1)); err != nil {
		return nil, unavailable(err)
	}
	if int64(buf.Len()) > limit {
		return nil, httpapi.Errorf(http.StatusBadGateway, "The file is larger than %d MB.", limit>>20)
	}
	return buf.Bytes(), nil
}

func (c *Client) do(ctx context.Context, target string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "QwikByte/noryx/"+buildinfo.Version+" (github.com/QwikByte/noryx)")
	res, err := c.http.Do(req)
	if err != nil {
		return nil, unavailable(cmp.Or(ctx.Err(), err))
	}
	if res.StatusCode != http.StatusOK {
		res.Body.Close()
		if res.StatusCode == http.StatusNotFound {
			return nil, errUnknown
		}
		return nil, unavailable(fmt.Errorf("status %d", res.StatusCode))
	}
	return res, nil
}

func unavailable(err error) error {
	return httpapi.Errorf(http.StatusBadGateway, "Hangar can't be reached right now (%v). Try again later.", err)
}
