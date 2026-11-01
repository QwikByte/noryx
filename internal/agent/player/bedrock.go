package player

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/agent/datadir"
)

// whitelisted is a player on Minecraft's whitelist.
type whitelisted struct {
	UUID string `json:"uuid"`
	Name string `json:"name"`
}

// bedrockWhitelist reports whether a change whitelists a Bedrock player by their ID or
// removes one, which the servers behind a proxy can't do by name: they can't look them up.
func bedrockWhitelist(c *noryxv1.PlayerChange) bool {
	return c.GetUuid() != "" || noryxv1.BedrockPlayer(c.GetName()) && c.GetAction() == noryxv1.PlayerAction_PLAYER_ACTION_WHITELIST_REMOVE
}

// whitelistBedrock changes the whitelist file of a running server for a Bedrock player and
// makes it reload the file. The caller holds the server's lock, so that no other change of the
// whitelist makes the server write its own list in between. Its messages are Minecraft's.
func (s *Service) whitelistBedrock(ctx context.Context, id string, dir *datadir.Dir, c *noryxv1.PlayerChange) (*noryxv1.PlayerChangeResult, error) {
	list := []whitelisted{}
	if err := readJSON(dir, whitelistFile, &list); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	before := len(list)
	list = slices.DeleteFunc(list, func(p whitelisted) bool {
		return strings.EqualFold(p.Name, c.GetName()) || c.GetUuid() != "" && p.UUID == c.GetUuid()
	})
	out := "Removed " + c.GetName() + " from the whitelist"
	switch {
	case c.GetUuid() != "":
		list, out = append(list, whitelisted{c.GetUuid(), c.GetName()}), "Added "+c.GetName()+" to the whitelist"
	case len(list) == before:
		return &noryxv1.PlayerChangeResult{Output: "Player is not whitelisted"}, nil
	}
	data, err := json.MarshalIndent(append([]whitelisted{}, list...), "", "  ")
	if err == nil {
		err = dir.WriteFile(whitelistFile, data)
	}
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	if _, err := s.rt.SendCommand(ctx, id, "minecraft:whitelist reload"); err != nil {
		return nil, status.Errorf(codes.Unavailable, "The whitelist changed, but the server can't reload it: %v", err)
	}
	return &noryxv1.PlayerChangeResult{Output: out}, nil
}
