// Package modrinth is a client for the API of Modrinth (https://modrinth.com), the
// catalogue the panel installs plugins and mods from. Files are only downloaded from
// Modrinth's CDN and only used if their SHA-512 hash matches the one of the API.
package modrinth

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
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

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/buildinfo"
	"github.com/QwikByte/noryx/internal/master/httpapi"
)

const (
	DefaultAPI = "https://api.modrinth.com/v2"
	DefaultCDN = "https://cdn.modrinth.com/"
	// MaxFileSize matches the limit of the agents.
	MaxFileSize = 256 << 20
	// Timeout bounds a call of an API; downloads get DownloadTimeout.
	Timeout          = 2 * time.Minute
	maxResponseBytes = 16 << 20
	maxIconBytes     = 1 << 20
	pageSize         = 20
	releaseTTL       = time.Hour
)

var (
	// loaders run the plugins or mods of a server type, the most specific first.
	loaders = map[noryxv1.ServerType][]string{
		noryxv1.ServerType_SERVER_TYPE_PAPER:      {"paper", "spigot", "bukkit"},
		noryxv1.ServerType_SERVER_TYPE_PURPUR:     {"purpur", "paper", "spigot", "bukkit"},
		noryxv1.ServerType_SERVER_TYPE_FOLIA:      {"folia"}, // other plugins don't run on Folia
		noryxv1.ServerType_SERVER_TYPE_LEAF:       {"paper", "spigot", "bukkit"},
		noryxv1.ServerType_SERVER_TYPE_VELOCITY:   {"velocity"},
		noryxv1.ServerType_SERVER_TYPE_BUNGEECORD: {"bungeecord", "waterfall"},
		noryxv1.ServerType_SERVER_TYPE_WATERFALL:  {"waterfall", "bungeecord"},
		noryxv1.ServerType_SERVER_TYPE_FABRIC:     {"fabric"},
		noryxv1.ServerType_SERVER_TYPE_QUILT:      {"quilt", "fabric"},
		noryxv1.ServerType_SERVER_TYPE_FORGE:      {"forge"},
		noryxv1.ServerType_SERVER_TYPE_NEOFORGE:   {"neoforge"},
	}
	projectID = regexp.MustCompile(`^[A-Za-z0-9]{1,32}$`)
	category  = regexp.MustCompile(`^[a-z-]{1,32}$`)
	// Sorts are the orders of search results; without a query, relevance means downloads.
	Sorts = []string{"relevance", "downloads", "follows", "newest", "updated"}
	// iconName matches the path of a project icon on the CDN, e.g. AABBCCDD/icon.png.
	iconName   = regexp.MustCompile(`^[A-Za-z0-9]{1,32}/[A-Za-z0-9_-]{1,128}\.(png|jpe?g|webp|gif)$`)
	iconTypes  = map[string]string{"png": "image/png", "jpg": "image/jpeg", "jpeg": "image/jpeg", "webp": "image/webp", "gif": "image/gif"}
	errUnknown = httpapi.Errorf(http.StatusNotFound, "This project doesn't exist on Modrinth.")
)

// Loaders returns the loaders whose plugins or mods run on a server type; none for
// vanilla servers.
func Loaders(t noryxv1.ServerType) []string { return loaders[t] }

// AllLoaders returns the loaders of the server types that load a kind, plugins, mods or
// modpacks, or of all server types if kind is empty.
func AllLoaders(kind string) []string {
	var all []string
	for t, names := range loaders {
		if kind == "" || t.Modded() == (kind != "plugins") { // mods and modpacks need a mod loader
			all = append(all, names...)
		}
	}
	slices.Sort(all)
	return slices.Compact(all)
}

// ValidProjectID reports whether id is a well-formed project or version ID.
func ValidProjectID(id string) bool { return projectID.MatchString(id) }

type Project struct {
	ID          string   `json:"id"`
	Slug        string   `json:"slug"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	IconURL     string   `json:"icon_url"`
	Downloads   int64    `json:"downloads"`
	Loaders     []string `json:"loaders"`
}

type SearchResult struct {
	Hits  []SearchHit `json:"hits"`
	Total int         `json:"total_hits"`
}

type SearchHit struct {
	ProjectID   string    `json:"project_id"`
	Slug        string    `json:"slug"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Author      string    `json:"author"`
	IconURL     string    `json:"icon_url"`
	Downloads   int64     `json:"downloads"`
	Follows     int64     `json:"follows"`
	Updated     time.Time `json:"date_modified"`
	ClientSide  string    `json:"client_side"` // required, optional, unsupported or unknown
	Categories  []string  `json:"categories"`  // includes the loaders
	// DisplayCategories are the main categories and loaders.
	DisplayCategories []string `json:"display_categories"`
}

// Search is a search for server-side plugins, mods and modpacks.
type Search struct {
	Query string
	Kind  string // plugins, mods or modpacks; plugins and mods if empty
	// Loaders are those the projects must support, one of them.
	Loaders     []string
	GameVersion string // empty for any version
	// Categories are those the projects must all be in, e.g. economy.
	Categories []string
	Sort       string // one of Sorts, relevance if empty
	// ServerOnly limits the search to projects that players don't have to install.
	ServerOnly bool
	Offset     int
}

// Valid reports whether the kind, the categories and the sort are well-formed.
func (s Search) Valid() bool {
	return slices.Contains([]string{"", "plugins", "mods", "modpacks"}, s.Kind) && (s.Sort == "" || slices.Contains(Sorts, s.Sort)) &&
		len(s.Categories) <= 10 && !slices.ContainsFunc(s.Categories, func(c string) bool { return !category.MatchString(c) })
}

type Version struct {
	ID            string       `json:"id"`
	ProjectID     string       `json:"project_id"`
	VersionNumber string       `json:"version_number"`
	VersionType   string       `json:"version_type"` // release, beta or alpha
	Published     time.Time    `json:"date_published"`
	Files         []File       `json:"files"`
	Dependencies  []Dependency `json:"dependencies"`
	GameVersions  []string     `json:"game_versions"`
	Loaders       []string     `json:"loaders"`
	// Changelog tells what changed in the version, as Markdown of its author; see Changelogs.
	Changelog string `json:"changelog"`
}

// File returns the primary file of a version.
func (v Version) File() (File, bool) {
	if len(v.Files) == 0 {
		return File{}, false
	}
	return v.Files[max(0, slices.IndexFunc(v.Files, func(f File) bool { return f.Primary }))], true
}

type File struct {
	URL      string `json:"url"`
	Filename string `json:"filename"`
	Primary  bool   `json:"primary"`
	Size     int64  `json:"size"`
	Hashes   struct {
		SHA512 string `json:"sha512"`
		// SHA256 is set instead for files of Hangar, see package hangar.
		SHA256 string `json:"-"`
	} `json:"hashes"`
}

type Dependency struct {
	ProjectID string `json:"project_id"`
	// VersionID is a version of the project, which some name instead of the project.
	VersionID string `json:"version_id"`
	Type      string `json:"dependency_type"` // required, optional, incompatible or embedded
}

type Client struct {
	api, cdn string
	http     *http.Client

	mu         sync.Mutex
	releases   []string // of Minecraft, the newest first
	releasesAt time.Time
}

// New returns a client for the API and CDN at the given base URLs. Calls of the API get Timeout,
// downloads DownloadTimeout, through their context.
func New(api, cdn string) *Client {
	return &Client{api: api, cdn: cdn, http: &http.Client{CheckRedirect: httpapi.SameOrigin}}
}

// DownloadTimeout is how long the download of a file of size bytes may take: Timeout and a second
// for each 256 KiB, so that only links slower than 256 KiB/s fail. A file of up to MaxFileSize
// gets about 19 minutes, as does one of unknown size.
func DownloadTimeout(size int64) time.Duration {
	if size <= 0 || size > MaxFileSize {
		size = MaxFileSize
	}
	return Timeout + time.Duration(size>>18)*time.Second
}

// Search finds server-side plugins and mods. Without a query, the most downloaded
// projects are the most relevant.
func (c *Client) Search(ctx context.Context, s Search) (SearchResult, error) {
	// Modrinth names the project types in the singular.
	types := []string{"project_type:plugin", "project_type:mod"}
	if s.Kind != "" {
		types = []string{"project_type:" + strings.TrimSuffix(s.Kind, "s")}
	}
	facets := [][]string{prefixed("categories:", s.Loaders), {"server_side:required", "server_side:optional"}, types}
	for _, c := range s.Categories {
		facets = append(facets, []string{"categories:" + c})
	}
	if s.GameVersion != "" {
		facets = append(facets, []string{"versions:" + s.GameVersion})
	}
	if s.ServerOnly {
		facets = append(facets, []string{"client_side:optional", "client_side:unsupported"})
	}
	index := cmp.Or(s.Sort, "relevance")
	if s.Query == "" && index == "relevance" {
		index = "downloads"
	}
	q := url.Values{
		"query": {s.Query}, "facets": {jsonText(facets)}, "index": {index},
		"limit": {strconv.Itoa(pageSize)}, "offset": {strconv.Itoa(s.Offset)},
	}
	var res SearchResult
	return res, c.call(ctx, http.MethodGet, "/search?"+q.Encode(), nil, &res)
}

// Projects returns the projects with the given IDs; unknown ones are left out.
func (c *Client) Projects(ctx context.Context, ids []string) ([]Project, error) {
	projects := []Project{}
	if len(ids) == 0 {
		return projects, nil
	}
	return projects, c.call(ctx, http.MethodGet, "/projects?"+url.Values{"ids": {jsonText(ids)}}.Encode(), nil, &projects)
}

// Versions returns the versions of a project for the given loaders and, unless empty,
// Minecraft version, the newest first.
func (c *Client) Versions(ctx context.Context, project string, loaderNames []string, gameVersion string) ([]Version, error) {
	return c.versions(ctx, project, loaderNames, gameVersion, false)
}

// Changelogs returns the versions like Versions, with their changelogs.
func (c *Client) Changelogs(ctx context.Context, project string, loaderNames []string, gameVersion string) ([]Version, error) {
	return c.versions(ctx, project, loaderNames, gameVersion, true)
}

func (c *Client) versions(ctx context.Context, project string, loaderNames []string, gameVersion string, changelogs bool) ([]Version, error) {
	if !ValidProjectID(project) {
		return nil, errUnknown
	}
	q := url.Values{"loaders": {jsonText(loaderNames)}, "include_changelog": {strconv.FormatBool(changelogs)}}
	if gameVersion != "" {
		q.Set("game_versions", jsonText([]string{gameVersion}))
	}
	var versions []Version
	err := c.call(ctx, http.MethodGet, "/project/"+project+"/version?"+q.Encode(), nil, &versions)
	slices.SortStableFunc(versions, func(a, b Version) int { return b.Published.Compare(a.Published) })
	return versions, err
}

// Version returns a version by its ID.
func (c *Client) Version(ctx context.Context, id string) (Version, error) {
	var v Version
	if !ValidProjectID(id) {
		return v, errUnknown
	}
	return v, c.call(ctx, http.MethodGet, "/version/"+id, nil, &v)
}

// VersionsByHash identifies files by their SHA-512 hashes. Unknown files are left out.
func (c *Client) VersionsByHash(ctx context.Context, hashes []string) (map[string]Version, error) {
	versions := map[string]Version{}
	return versions, c.call(ctx, http.MethodPost, "/version_files", map[string]any{"hashes": hashes, "algorithm": "sha512"}, &versions)
}

// Updates returns the newest version of the given types, e.g. release and beta, for the given
// loaders and, unless empty, Minecraft version of the projects that the files with the given
// hashes belong to.
func (c *Client) Updates(ctx context.Context, hashes, loaderNames []string, gameVersion string, versionTypes []string) (map[string]Version, error) {
	body := map[string]any{"hashes": hashes, "algorithm": "sha512", "loaders": loaderNames, "version_types": versionTypes}
	if gameVersion != "" {
		body["game_versions"] = []string{gameVersion}
	}
	versions := map[string]Version{}
	return versions, c.call(ctx, http.MethodPost, "/version_files/update", body, &versions)
}

// Releases returns the releases of Minecraft, the newest first. Callers must not change
// the list.
func (c *Client) Releases(ctx context.Context) ([]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Since(c.releasesAt) < releaseTTL {
		return c.releases, nil
	}
	var versions []gameVersion
	if err := c.call(ctx, http.MethodGet, "/tag/game_version", nil, &versions); err != nil {
		return nil, err
	}
	var releases []string
	for _, v := range versions {
		if v.Type == "release" {
			releases = append(releases, v.Version)
		}
	}
	if len(releases) == 0 {
		return nil, httpapi.Errorf(http.StatusBadGateway, "Modrinth lists no Minecraft release.")
	}
	c.releases, c.releasesAt = releases, time.Now()
	return releases, nil
}

// LatestRelease returns the newest release of Minecraft.
func (c *Client) LatestRelease(ctx context.Context) (string, error) {
	releases, err := c.Releases(ctx)
	if err != nil {
		return "", err
	}
	return releases[0], nil
}

// gameVersion is a Minecraft version, the newest first.
type gameVersion struct {
	Version string `json:"version"`
	Type    string `json:"version_type"` // release or snapshot
}

// OnCDN reports whether a file is on Modrinth's CDN, from which Download downloads.
func (c *Client) OnCDN(fileURL string) bool { return strings.HasPrefix(fileURL, c.cdn) }

// Download fetches a file from the CDN and checks its size and hash.
func (c *Client) Download(ctx context.Context, f File) ([]byte, error) {
	if !c.OnCDN(f.URL) || f.Size > MaxFileSize || len(f.Hashes.SHA512) != sha512.Size*2 {
		return nil, httpapi.Errorf(http.StatusBadGateway, "%s can't be downloaded: it isn't on Modrinth's CDN or is larger than %d MB.", f.Filename, MaxFileSize>>20)
	}
	data, err := c.fetch(ctx, f.URL, MaxFileSize, DownloadTimeout(f.Size))
	if err != nil {
		return nil, err
	}
	if sum := sha512.Sum512(data); hex.EncodeToString(sum[:]) != strings.ToLower(f.Hashes.SHA512) {
		return nil, httpapi.Errorf(http.StatusBadGateway, "The download of %s is corrupted. Try again.", f.Filename)
	}
	return data, nil
}

// ProjectOf returns the project of a file on the CDN, from its address
// data/<project>/versions/<version>/<file>, or "".
func (c *Client) ProjectOf(fileURL string) string {
	rest, ok := strings.CutPrefix(fileURL, c.cdn+"data/")
	project, rest, _ := strings.Cut(rest, "/")
	if !ok || !strings.HasPrefix(rest, "versions/") || !ValidProjectID(project) {
		return ""
	}
	return project
}

// IconName returns the name of a project icon on the CDN, e.g. AABBCCDD/icon.png, or ""
// for icons elsewhere.
func (c *Client) IconName(iconURL string) string {
	name, ok := strings.CutPrefix(iconURL, c.cdn+"data/")
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
	data, err := c.fetch(ctx, c.cdn+"data/"+name, maxIconBytes, Timeout)
	return data, iconTypes[name[strings.LastIndexByte(name, '.')+1:]], err
}

// call sends a request to the API and decodes its JSON response into out, within Timeout.
func (c *Client) call(ctx context.Context, method, path string, body, out any) error {
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	var r io.Reader
	if body != nil {
		r = strings.NewReader(jsonText(body))
	}
	res, err := c.do(ctx, method, c.api+path, r)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if err := json.NewDecoder(io.LimitReader(res.Body, maxResponseBytes)).Decode(out); err != nil {
		return unavailable(err)
	}
	return nil
}

// fetch downloads a file of up to limit bytes within timeout.
func (c *Client) fetch(ctx context.Context, fileURL string, limit int64, timeout time.Duration) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	res, err := c.do(ctx, http.MethodGet, fileURL, nil)
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

func (c *Client) do(ctx context.Context, method, target string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, err
	}
	// Modrinth asks clients to identify themselves.
	req.Header.Set("User-Agent", "QwikByte/noryx/"+buildinfo.Version+" (github.com/QwikByte/noryx)")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
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
	return httpapi.Errorf(http.StatusBadGateway, "Modrinth can't be reached right now (%v). Try again later.", err)
}

func prefixed(prefix string, values []string) []string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = prefix + v
	}
	return out
}

func jsonText(v any) string {
	data, _ := json.Marshal(v) //nolint:errchkjson // only strings, slices and maps of them
	return string(data)
}
