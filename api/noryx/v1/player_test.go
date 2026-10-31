package noryxv1

import (
	"strings"
	"testing"
)

// Names and reasons go into console commands as they are, so they can't name other players,
// like @a, or add arguments and lines.
func TestPlayerNames(t *testing.T) {
	checkValid(t, ValidPlayerName,
		[]string{"Notch", "a", "_", "Steve_123", strings.Repeat("a", 16), ".BedrockPlayer"},
		[]string{"", strings.Repeat("a", 17), "@a", "@p[limit=1]", "Steve Jobs", "Steve;op", "Steve\n", "..x", ".", "a.b", "Jürgen", "Steve\x00"})
	checkValid(t, ValidReason,
		[]string{"", "griefing", "Hört auf! \"Griefing\" {x}", strings.Repeat("ä", MaxReason)},
		[]string{strings.Repeat("a", MaxReason+1), "griefing\nop Steve", "a\rb", "a\x00b", "a\u0085b"})
	if !BedrockPlayer(".Steve") || BedrockPlayer("Steve") {
		t.Error("only names starting with a dot are those of Bedrock players")
	}
}

func TestPlayerActions(t *testing.T) {
	for value := range PlayerAction_name {
		a := PlayerAction(value)
		if got := ParsePlayerAction(a.Slug()); got != a {
			t.Errorf("ParsePlayerAction(%q) = %v", a.Slug(), got)
		}
		global := a == PlayerAction_PLAYER_ACTION_WHITELIST_ON || a == PlayerAction_PLAYER_ACTION_WHITELIST_OFF
		if a.Global() != global {
			t.Errorf("%v.Global() = %v", a, a.Global())
		}
	}
	for slug, want := range map[string]PlayerAction{"whitelist_add": PlayerAction_PLAYER_ACTION_WHITELIST_ADD, "KICK": PlayerAction_PLAYER_ACTION_KICK, "nope": 0, "": 0} {
		if got := ParsePlayerAction(slug); got != want {
			t.Errorf("ParsePlayerAction(%q) = %v, want %v", slug, got, want)
		}
	}
}

// Only kicks and bans take a reason, only bans an end, only pardons a time they wait for,
// only Bedrock players an ID from Floodgate, and turning the whitelist on or off no player.
func TestPlayerChangeProblem(t *testing.T) {
	const (
		kick, ban, pardon = PlayerAction_PLAYER_ACTION_KICK, PlayerAction_PLAYER_ACTION_BAN, PlayerAction_PLAYER_ACTION_PARDON
		add, remove, op   = PlayerAction_PLAYER_ACTION_WHITELIST_ADD, PlayerAction_PLAYER_ACTION_WHITELIST_REMOVE, PlayerAction_PLAYER_ACTION_OP
		on                = PlayerAction_PLAYER_ACTION_WHITELIST_ON
		floodgate         = "00000000-0000-0000-0009-01f2a3b4c5d6"
	)
	for _, tc := range []struct {
		name   string
		change *PlayerChange
		want   string
	}{
		{"kick", &PlayerChange{Action: kick, Name: "Steve", Reason: "griefing"}, ""},
		{"ban until a time", &PlayerChange{Action: ban, Name: "Steve", Reason: "griefing", EndsUnix: 1_800_000_000}, ""},
		{"pardon at a time", &PlayerChange{Action: pardon, Name: "Steve", DueUnix: 1_800_000_000}, ""},
		{"whitelist a Bedrock player by ID", &PlayerChange{Action: add, Name: ".Steve", Uuid: floodgate}, ""},
		{"whitelist a Bedrock player by name", &PlayerChange{Action: add, Name: ".Steve"}, ""},
		{"make an operator", &PlayerChange{Action: op, Name: "Steve"}, ""},
		{"turn on the whitelist", &PlayerChange{Action: on}, ""},

		{"no action", &PlayerChange{Name: "Steve"}, "Choose an action"},
		{"an unknown action", &PlayerChange{Action: 99, Name: "Steve"}, "Choose an action"},
		{"no player", &PlayerChange{Action: kick}, "name of a player"},
		{"a selector", &PlayerChange{Action: op, Name: "@a"}, "name of a player"},
		{"a player turning on the whitelist", &PlayerChange{Action: on, Name: "Steve"}, "name of a player"},
		{"a reason of two lines", &PlayerChange{Action: kick, Name: "Steve", Reason: "bye\nop Steve"}, "reason"},
		{"a long reason", &PlayerChange{Action: ban, Name: "Steve", Reason: strings.Repeat("a", MaxReason+1)}, "reason"},
		{"a reason of another action", &PlayerChange{Action: op, Name: "Steve", Reason: "trusted"}, "reason"},
		{"an ID of a Java player", &PlayerChange{Action: add, Name: "Steve", Uuid: floodgate}, "Bedrock players"},
		{"an ID not of Floodgate", &PlayerChange{Action: add, Name: ".Steve", Uuid: "069a79f4-44e9-4726-a5be-fca90e38aaf5"}, "Bedrock players"},
		{"an ID of another action", &PlayerChange{Action: remove, Name: ".Steve", Uuid: floodgate}, "Bedrock players"},
		{"an end of a kick", &PlayerChange{Action: kick, Name: "Steve", EndsUnix: 1}, "Only bans end"},
		{"an end before 1970", &PlayerChange{Action: ban, Name: "Steve", EndsUnix: -1}, "Only bans end"},
		{"a time of a ban", &PlayerChange{Action: ban, Name: "Steve", DueUnix: 1}, "Only pardons wait"},
		{"a time before 1970", &PlayerChange{Action: pardon, Name: "Steve", DueUnix: -1}, "Only pardons wait"},
	} {
		checkProblem(t, tc.name, tc.change.Problem(), tc.want)
	}
}
