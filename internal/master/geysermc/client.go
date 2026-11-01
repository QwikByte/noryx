// Package geysermc is a client for GeyserMC, who make Geyser and Floodgate, which let
// Bedrock players join: for the download server (download.geysermc.org), the only source of
// Floodgate for proxies, and for the global API (api.geysermc.org), which knows the IDs of
// Bedrock players. It returns the newest build in the shape of a Modrinth version, which the
// plugin manager works with; its project IDs start with Prefix.
package geysermc

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QwikByte/noryx/internal/buildinfo"
	"github.com/QwikByte/noryx/internal/master/httpapi"
	"github.com/QwikByte/noryx/internal/master/modrinth"
	"github.com/QwikByte/noryx/internal/master/mojang"
)

const (
	DownloadAPI = "https://download.geysermc.org/v2"
	GlobalAPI   = "https://api.geysermc.org/v2"
	// Prefix starts the IDs of GeyserMC's projects, e.g. geysermc-floodgate.
	Prefix = "geysermc-"
	// Floodgate is the project that vouches for Bedrock players on a proxy.
	Floodgate = Prefix + "floodgate"

	// CacheTime is how long the newest build stays known, as files are identified by it.
	CacheTime = 10 * time.Minute

	maxResponseBytes = 1 << 20
)

var (
	// platforms are GeyserMC's names of Modrinth's loaders of proxies.
	platforms = map[string]string{"velocity": "velocity", "bungeecord": "bungee", "waterfall": "bungee"}
	id        = regexp.MustCompile(`^geysermc-[a-z]{1,32}(-[0-9A-Za-z.+-]{1,64})?$`)
)

// ValidID reports whether id is a well-formed ID of a project or version of GeyserMC.
func ValidID(s string) bool { return id.MatchString(s) }

// Platform returns GeyserMC's platform of the first of loaders it has, or "".
func Platform(loaders []string) (platform, loader string) {
	for _, l := range loaders {
		if p := platforms[l]; p != "" {
			return p, l
		}
	}
	return "", ""
}

// Project describes a project of GeyserMC.
func Project(id string) (modrinth.Project, bool) {
	if id != Floodgate {
		return modrinth.Project{}, false
	}
	return modrinth.Project{ID: id, Slug: "floodgate", Title: "Floodgate", Loaders: []string{"bungeecord", "velocity", "waterfall"}}, true
}

type build struct {
	Version   string    `json:"version"`
	Build     int       `json:"build"`
	Time      time.Time `json:"time"`
	Downloads map[string]struct {
		Name   string `json:"name"`
		SHA256 string `json:"sha256"`
	} `json:"downloads"`
}

type cached struct {
	build build
	at    time.Time
}

type Client struct {
	api, globalAPI string
	http           *http.Client
	cache          time.Duration

	mu     sync.Mutex
	builds map[string]cached // the newest build by project
}

// New returns a client for the download server and the global API at the given base URLs,
// which keeps the newest builds for the time cache. Calls of the APIs get modrinth.Timeout,
// downloads modrinth.DownloadTimeout, through their context.
func New(api, global string, cache time.Duration) *Client {
	return &Client{api: api, globalAPI: global, http: &http.Client{}, cache: cache, builds: map[string]cached{}}
}

// Latest returns the newest build of a project for the platform of the first loader
// GeyserMC has, or none.
func (c *Client) Latest(ctx context.Context, project string, loaders []string) ([]modrinth.Version, error) {
	platform, loader := Platform(loaders)
	name, ok := strings.CutPrefix(project, Prefix)
	if _, known := Project(project); !known || !ok || platform == "" {
		return nil, nil
	}
	b, err := c.latest(ctx, name)
	d, found := b.Downloads[platform]
	if err != nil || !found {
		return nil, err
	}
	f := modrinth.File{URL: fmt.Sprintf("%s/projects/%s/versions/%s/builds/%d/downloads/%s", c.api, name, b.Version, b.Build, platform), Filename: d.Name, Primary: true}
	f.Hashes.SHA256 = d.SHA256
	number := b.Version + "-b" + strconv.Itoa(b.Build)
	return []modrinth.Version{{
		ID: project + "-" + number, ProjectID: project, VersionNumber: number, VersionType: "release", Published: b.Time,
		Files: []modrinth.File{f}, Loaders: []string{loader},
	}}, nil
}

func (c *Client) latest(ctx context.Context, project string) (build, error) {
	c.mu.Lock()
	known, ok := c.builds[project]
	c.mu.Unlock()
	if ok && time.Since(known.at) < c.cache {
		return known.build, nil
	}
	var b build
	ctx, cancel := context.WithTimeout(ctx, modrinth.Timeout)
	defer cancel()
	res, err := c.do(ctx, c.api+"/projects/"+project+"/versions/latest/builds/latest")
	if err != nil {
		return b, err
	}
	defer res.Body.Close()
	if err := json.NewDecoder(io.LimitReader(res.Body, maxResponseBytes)).Decode(&b); err != nil {
		return b, unavailable(err)
	}
	c.mu.Lock()
	c.builds[project] = cached{b, time.Now()}
	c.mu.Unlock()
	return b, nil
}

// OnServer reports whether a file is on GeyserMC's download server, from which Download downloads.
func (c *Client) OnServer(fileURL string) bool { return strings.HasPrefix(fileURL, c.api+"/") }

// Download fetches a file and checks its size and hash.
func (c *Client) Download(ctx context.Context, f modrinth.File) ([]byte, error) {
	if !c.OnServer(f.URL) || len(f.Hashes.SHA256) != sha256.Size*2 {
		return nil, httpapi.Errorf(http.StatusBadGateway, "%s can't be downloaded: it isn't on GeyserMC's download server.", f.Filename)
	}
	ctx, cancel := context.WithTimeout(ctx, modrinth.DownloadTimeout(f.Size))
	defer cancel()
	res, err := c.do(ctx, f.URL)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, io.LimitReader(res.Body, modrinth.MaxFileSize+1)); err != nil {
		return nil, unavailable(err)
	}
	if buf.Len() > modrinth.MaxFileSize {
		return nil, httpapi.Errorf(http.StatusBadGateway, "%s is larger than %d MB.", f.Filename, modrinth.MaxFileSize>>20)
	}
	if sum := sha256.Sum256(buf.Bytes()); hex.EncodeToString(sum[:]) != strings.ToLower(f.Hashes.SHA256) {
		return nil, httpapi.Errorf(http.StatusBadGateway, "The download of %s is corrupted. Try again.", f.Filename)
	}
	return buf.Bytes(), nil
}

// PlayerID returns the ID Floodgate gives the Bedrock player with a gamertag: their XUID,
// which GeyserMC knows of those who joined a server with Geyser before.
func (c *Client) PlayerID(ctx context.Context, gamertag string) (string, error) {
	xuid, err := c.xuid(ctx, gamertag)
	if err == nil && xuid == 0 {
		err = httpapi.Errorf(http.StatusNotFound,
			"GeyserMC doesn't know the Bedrock player %s yet. They need to join a server with Geyser once, e.g. one of yours with its whitelist off.", gamertag)
	}
	return fmt.Sprintf("00000000-0000-0000-%04x-%012x", xuid>>48, xuid&(1<<48-1)), err
}

// SkinTexture returns the ID of the skin of a Bedrock player on Minecraft's textures server,
// to which Geyser uploads the skins of the players who join, or "" if GeyserMC knows none.
func (c *Client) SkinTexture(ctx context.Context, gamertag string) (string, error) {
	xuid, err := c.xuid(ctx, gamertag)
	if err != nil || xuid == 0 {
		return "", err
	}
	var skin struct {
		TextureID string `json:"texture_id"` // missing without a skin
	}
	if err := c.fromGlobal(ctx, fmt.Sprintf("/skin/%d", xuid), &skin); err != nil || !mojang.ValidTexture(skin.TextureID) {
		return "", err
	}
	return skin.TextureID, nil
}

// xuid returns the XUID of the Bedrock player with a gamertag, or 0 if GeyserMC doesn't know them.
func (c *Client) xuid(ctx context.Context, gamertag string) (uint64, error) {
	var body struct {
		XUID uint64 `json:"xuid"` // missing for players GeyserMC doesn't know
	}
	err := c.fromGlobal(ctx, "/xbox/xuid/"+url.PathEscape(gamertag), &body)
	return body.XUID, err
}

// fromGlobal decodes the answer of the global API at path into v, whatever its status: GeyserMC
// answers players it doesn't know with an error and a message.
func (c *Client) fromGlobal(ctx context.Context, path string, v any) error {
	ctx, cancel := context.WithTimeout(ctx, modrinth.Timeout)
	defer cancel()
	res, err := c.send(ctx, c.globalAPI+path)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if err := json.NewDecoder(io.LimitReader(res.Body, maxResponseBytes)).Decode(v); err != nil {
		return unavailable(fmt.Errorf("status %d", res.StatusCode))
	}
	return nil
}

// do gets a URL and checks that the response is OK.
func (c *Client) do(ctx context.Context, target string) (*http.Response, error) {
	res, err := c.send(ctx, target)
	if err == nil && res.StatusCode != http.StatusOK {
		res.Body.Close()
		return nil, unavailable(fmt.Errorf("status %d", res.StatusCode))
	}
	return res, err
}

func (c *Client) send(ctx context.Context, target string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "QwikByte/noryx/"+buildinfo.Version+" (github.com/QwikByte/noryx)")
	res, err := c.http.Do(req) // follows the redirect of "latest" to the build
	if err != nil {
		return nil, unavailable(cmp.Or(ctx.Err(), err))
	}
	return res, nil
}

func unavailable(err error) error {
	return httpapi.Errorf(http.StatusBadGateway, "GeyserMC can't be reached right now (%v). Try again later.", err)
}
