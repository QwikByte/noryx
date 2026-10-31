package player

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
)

// A Bedrock server kicks, makes operators and changes its allowlist with the commands of
// Bedrock Dedicated Server, without a namespace; it has no bans and knows no names of
// Floodgate. Its allowlist is its whitelist.
func TestBedrockServer(t *testing.T) {
	ctx := t.Context()
	rt := &fakeRuntime{dir: t.TempDir()}
	rt.add(t, up, noryxv1.ServerType_SERVER_TYPE_BEDROCK, running)
	s := NewService(rt)
	for _, c := range []*noryxv1.PlayerChange{
		newChange(noryxv1.PlayerAction_PLAYER_ACTION_KICK, "Steve", "AFK"),
		newChange(noryxv1.PlayerAction_PLAYER_ACTION_WHITELIST_ADD, "Steve", ""),
		newChange(noryxv1.PlayerAction_PLAYER_ACTION_WHITELIST_REMOVE, "Steve", ""),
		newChange(noryxv1.PlayerAction_PLAYER_ACTION_OP, "Steve", ""),
		newChange(noryxv1.PlayerAction_PLAYER_ACTION_DEOP, "Steve", ""),
		newChange(noryxv1.PlayerAction_PLAYER_ACTION_WHITELIST_ON, "", ""),
	} {
		if _, err := s.ChangePlayer(ctx, &noryxv1.ChangePlayerRequest{ServerId: up, Change: c}); err != nil {
			t.Fatalf("%v: %v", c.GetAction(), err)
		}
	}
	want := []string{"a kick Steve AFK", "a allowlist add Steve", "a allowlist remove Steve", "a op Steve", "a deop Steve", "a allowlist on"}
	if got := rt.sent(); !slices.Equal(got, want) {
		t.Errorf("commands %q, want %q", got, want)
	}
	for _, c := range []*noryxv1.PlayerChange{
		newChange(noryxv1.PlayerAction_PLAYER_ACTION_BAN, "Steve", "Griefing"),
		newChange(noryxv1.PlayerAction_PLAYER_ACTION_PARDON, "Steve", ""),
		newChange(noryxv1.PlayerAction_PLAYER_ACTION_WHITELIST_ADD, ".Steve", ""),
	} {
		if _, err := s.ChangePlayer(ctx, &noryxv1.ChangePlayerRequest{ServerId: up, Change: c}); status.Code(err) != codes.FailedPrecondition {
			t.Errorf("%v of %s: %v", c.GetAction(), c.GetName(), err)
		}
	}
	data := filepath.Join(rt.dir, up)
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(data, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("allowlist.json", `[{"ignoresPlayerLimit":false,"name":"Steve","xuid":"2535453759792258"}]`)
	write("server.properties", "allow-list=true\n")
	write("permissions.json", `[{"permission":"operator","xuid":"2535453759792258"}]`)
	lists, err := s.GetPlayerLists(ctx, &noryxv1.GetPlayerListsRequest{ServerId: up})
	if err != nil {
		t.Fatal(err)
	}
	if w := lists.GetWhitelisted(); len(w) != 1 || w[0].GetName() != "Steve" || !lists.GetWhitelistEnabled() ||
		len(lists.GetBanned()) != 0 || len(lists.GetOperators()) != 0 {
		t.Errorf("lists %v", lists)
	}
}
