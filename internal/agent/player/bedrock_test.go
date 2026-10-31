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

func TestWhitelistBedrock(t *testing.T) {
	ctx := t.Context()
	rt := &fakeRuntime{dir: t.TempDir()}
	rt.add(t, up, paper, running)
	rt.add(t, down, paper, stopped)
	s := NewService(rt)
	const id = "00000000-0000-0000-0009-01f64f65c7c3"
	add := &noryxv1.PlayerChange{Action: noryxv1.PlayerAction_PLAYER_ACTION_WHITELIST_ADD, Name: ".Tim203", Uuid: id}
	remove := newChange(noryxv1.PlayerAction_PLAYER_ACTION_WHITELIST_REMOVE, ".tim203", "")
	change := func(server string, c *noryxv1.PlayerChange) string {
		t.Helper()
		res, err := changeOne(ctx, s, server, c)
		if err != nil || res.GetPending() {
			t.Fatalf("%v on %s = %v, %v", c, server[:1], res, err)
		}
		return res.GetOutput()
	}
	lists := func(server string) *noryxv1.GetPlayerListsResponse {
		t.Helper()
		lists, err := s.GetPlayerLists(ctx, &noryxv1.GetPlayerListsRequest{ServerId: server})
		if err != nil {
			t.Fatal(err)
		}
		return lists
	}
	whitelist := func(server string) []*noryxv1.ListedPlayer { return lists(server).GetWhitelisted() }
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	check(os.WriteFile(filepath.Join(rt.dir, up, whitelistFile), []byte(`[{"uuid":"u1","name":"Alex"}]`), 0o600))

	// The ID goes into the file, once, and a running server reloads it.
	for range 2 {
		if out := change(up, add); out != "Added .Tim203 to the whitelist" {
			t.Fatalf("output = %q", out)
		}
	}
	if w := whitelist(up); len(w) != 2 || w[0].GetName() != "Alex" || w[1].GetUuid() != id || w[1].GetName() != ".Tim203" {
		t.Fatalf("whitelist = %v", w)
	}
	if got := rt.sent(); !slices.Equal(got, []string{"a minecraft:whitelist reload", "a minecraft:whitelist reload"}) {
		t.Fatalf("commands = %q", got)
	}
	// A server that doesn't run gets it once it does, as it reads the file when it starts;
	// removing goes by name.
	if res, err := changeOne(ctx, s, down, add); err != nil || !res.GetPending() {
		t.Fatalf("change on a stopped server = %v, %v", res, err)
	}
	rt.servers[1].State = running
	s.applyWaiting(ctx)
	s.wg.Wait()
	if w := whitelist(down); len(w) != 1 || w[0].GetUuid() != id || len(lists(down).GetPending()) != 0 {
		t.Fatalf("whitelist = %v", w)
	}
	if out := change(down, remove); out != "Removed .tim203 from the whitelist" || len(whitelist(down)) != 0 {
		t.Fatalf("output = %q, whitelist = %v", out, whitelist(down))
	}
	if out := change(down, remove); out != "Player is not whitelisted" || len(rt.sent()) != 4 {
		t.Fatalf("output = %q, commands = %q", out, rt.sent())
	}

	// Only Bedrock players are whitelisted by an ID, which must be Floodgate's.
	for _, c := range []*noryxv1.PlayerChange{
		{Action: noryxv1.PlayerAction_PLAYER_ACTION_WHITELIST_ADD, Name: "Alex", Uuid: id},
		{Action: noryxv1.PlayerAction_PLAYER_ACTION_OP, Name: ".Tim203", Uuid: id},
		{Action: noryxv1.PlayerAction_PLAYER_ACTION_WHITELIST_ADD, Name: ".Tim203", Uuid: "069a79f4-44e9-4726-a5be-fca90e38aaf5"},
	} {
		if _, err := changeOne(ctx, s, up, c); status.Code(err) != codes.InvalidArgument {
			t.Errorf("%v: %v", c, err)
		}
	}
}
