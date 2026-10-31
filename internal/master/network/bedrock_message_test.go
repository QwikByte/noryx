package network

import (
	"slices"
	"testing"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
)

// Bedrock servers show messages with their own commands, which take raw text, and the chat of
// everyone with say, as Java servers do.
func TestBedrockMessageCommands(t *testing.T) {
	for _, tc := range []struct {
		m      Message
		target string
		want   []string
	}{
		{Message{Text: "Restart soon"}, Everyone, []string{"say Restart soon"}},
		{Message{Text: `Hi "there"`}, "Steve", []string{
			`tellraw Steve {"rawtext":[{"text":"§7§o"},{"translate":"commands.message.display.incoming","with":["Server","Hi \"there\""]}]}`,
		}},
		{Message{Kind: "title", Text: "Welcome", Subtitle: "to the server"}, Everyone, []string{
			`titleraw @a subtitle {"rawtext":[{"text":"to the server"}]}`, `titleraw @a title {"rawtext":[{"text":"Welcome"}]}`,
		}},
		{Message{Kind: "actionbar", Text: "Hi"}, "Steve", []string{`titleraw Steve actionbar {"rawtext":[{"text":"Hi"}]}`}},
	} {
		got, err := tc.m.CommandsOn(noryxv1.ServerType_SERVER_TYPE_BEDROCK, tc.target)
		if err != nil || !slices.Equal(got, tc.want) {
			t.Errorf("%+v to %s = %q, %v, want %q", tc.m, tc.target, got, err, tc.want)
		}
	}
	if got, err := (Message{Kind: "bossbar", Text: "Hi"}).CommandsOn(noryxv1.ServerType_SERVER_TYPE_BEDROCK, Everyone); err == nil {
		t.Errorf("a boss bar = %q, want an error", got)
	}
}
