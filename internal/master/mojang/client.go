// Package mojang is a client for Mojang's API, which knows the profiles of Java players, and for
// Minecraft's textures server, which serves the skins of players.
package mojang

import (
	"bytes"
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"time"

	"github.com/QwikByte/noryx/internal/buildinfo"
	"github.com/QwikByte/noryx/internal/master/httpapi"
)

const (
	API           = "https://api.mojang.com"
	SessionServer = "https://sessionserver.mojang.com"
	Textures      = "https://textures.minecraft.net/texture/"

	maxResponseBytes = 64 << 10
	// MaxSkinBytes is the largest skin downloaded; skins are images of 64 by 64 pixels.
	MaxSkinBytes = 256 << 10
)

var (
	profileID = regexp.MustCompile(`^[0-9a-f]{32}$`)
	// textureURL matches the address of a skin on the textures server, which profiles name with
	// plain HTTP; the client only fetches the ID it ends with, over HTTPS.
	textureURL = regexp.MustCompile(`^https?://textures\.minecraft\.net/texture/([0-9a-f]{1,64})$`)
	textureID  = regexp.MustCompile(`^[0-9a-f]{1,64}$`)
)

// ValidTexture reports whether id is that of a texture on the textures server.
func ValidTexture(id string) bool { return textureID.MatchString(id) }

type Client struct {
	api, session, textures string
	http                   *http.Client
}

// New returns a client for Mojang's API, its session server and the textures server at the given
// base URLs.
func New(api, session, textures string) *Client {
	return &Client{api: api, session: session, textures: textures, http: &http.Client{Timeout: 15 * time.Second, CheckRedirect: httpapi.SameOrigin}}
}

// SkinTexture returns the ID of the skin of a Java player on the textures server, or "" if
// nobody has the name or the player has the default skin.
func (c *Client) SkinTexture(ctx context.Context, name string) (string, error) {
	var profile struct {
		ID string `json:"id"`
	}
	if found, err := c.get(ctx, c.api+"/users/profiles/minecraft/"+url.PathEscape(name), &profile); err != nil || !found || !profileID.MatchString(profile.ID) {
		return "", err
	}
	var session struct {
		Properties []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"properties"`
	}
	if found, err := c.get(ctx, c.session+"/session/minecraft/profile/"+profile.ID, &session); err != nil || !found {
		return "", err
	}
	for _, p := range session.Properties {
		if p.Name != "textures" {
			continue
		}
		data, err := base64.StdEncoding.DecodeString(p.Value)
		var textures struct {
			Textures struct {
				Skin struct {
					URL string `json:"url"`
				} `json:"SKIN"`
			} `json:"textures"`
		}
		if err == nil {
			err = json.Unmarshal(data, &textures)
		}
		if err != nil {
			return "", unavailable(err)
		}
		if m := textureURL.FindStringSubmatch(textures.Textures.Skin.URL); m != nil {
			return m[1], nil
		}
	}
	return "", nil
}

// Skin downloads a skin from the textures server by its ID.
func (c *Client) Skin(ctx context.Context, id string) ([]byte, error) {
	if !ValidTexture(id) {
		return nil, httpapi.Errorf(http.StatusNotFound, "Skin not found.")
	}
	res, err := c.send(ctx, c.textures+id)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, unavailable(fmt.Errorf("status %d", res.StatusCode))
	}
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, io.LimitReader(res.Body, MaxSkinBytes+1)); err != nil {
		return nil, unavailable(err)
	}
	if buf.Len() > MaxSkinBytes {
		return nil, unavailable(fmt.Errorf("skin larger than %d KiB", MaxSkinBytes>>10))
	}
	return buf.Bytes(), nil
}

// get decodes the JSON at target into v, and reports false if it isn't found.
func (c *Client) get(ctx context.Context, target string, v any) (bool, error) {
	res, err := c.send(ctx, target)
	if err != nil {
		return false, err
	}
	defer res.Body.Close()
	switch res.StatusCode {
	case http.StatusOK:
	case http.StatusNoContent, http.StatusNotFound:
		return false, nil
	default:
		return false, unavailable(fmt.Errorf("status %d", res.StatusCode))
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, maxResponseBytes)).Decode(v); err != nil {
		return false, unavailable(err)
	}
	return true, nil
}

func (c *Client) send(ctx context.Context, target string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "QwikByte/noryx/"+buildinfo.Version+" (github.com/QwikByte/noryx)")
	res, err := c.http.Do(req)
	if err != nil {
		return nil, unavailable(cmp.Or(ctx.Err(), err))
	}
	return res, nil
}

func unavailable(err error) error {
	return httpapi.Errorf(http.StatusBadGateway, "Mojang can't be reached right now (%v). Try again later.", err)
}
