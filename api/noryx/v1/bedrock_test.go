package noryxv1

import "testing"

// Versions end up in a variable of the image: the image's own words and versions of Bedrock
// Dedicated Server with four numbers pass, nothing else.
func TestValidBedrockVersion(t *testing.T) {
	checkValid(t, ValidBedrockVersion,
		[]string{"LATEST", "PREVIEW", "1.26.61.1", "1.21.31.04", "1.20.81.01"},
		[]string{"", "latest", "EXISTING", "1.21.4", "1.26.61", "1.26.61.1.2", "1.26.61.1 ", "1.26.61.1;id", "$(id)", "1.26.61.a", "12345.1.1.1"},
	)
}

// A Bedrock server uses its port and one after it for each player; other servers only their port.
func TestLastPort(t *testing.T) {
	bedrock := ServerType_SERVER_TYPE_BEDROCK
	for _, tc := range []struct {
		typ           ServerType
		port, players uint32
		want          uint32
	}{
		{bedrock, 19132, 0, 19141},
		{bedrock, 19132, 1, 19132},
		{bedrock, 19132, 100, 19231},
		{ServerType_SERVER_TYPE_PAPER, 25565, 0, 25565},
		{ServerType_SERVER_TYPE_PAPER, 25565, 20, 25565},
	} {
		if got := LastPort(tc.typ, tc.port, tc.players); got != tc.want {
			t.Errorf("LastPort(%v, %d, %d) = %d, want %d", tc.typ, tc.port, tc.players, got, tc.want)
		}
	}
	s := &Server{Type: bedrock, Port: 19132, MaxPlayers: 5}
	checkValid(t, s.Uses, []uint32{19132, 19134, 19136}, []uint32{19131, 19137, 25565})
}

// Bedrock servers run without Java and plugins, so they refuse their settings, and only they
// take a number of players, whose ports end at 65535.
func TestBedrockSettings(t *testing.T) {
	bedrock, paper := ServerType_SERVER_TYPE_BEDROCK, ServerType_SERVER_TYPE_PAPER
	for _, tc := range []struct {
		name string
		s    BedrockSettings
		want string
	}{
		{"a Bedrock server", BedrockSettings{Type: bedrock, Port: 19132}, ""},
		{"with players", BedrockSettings{Type: bedrock, Port: 19132, MaxPlayers: MaxBedrockPlayers}, ""},
		{"at the last ports", BedrockSettings{Type: bedrock, Port: 65526}, ""},
		{"a Paper server", BedrockSettings{Type: paper, Port: 25565, Java: "21", AikarFlags: true, JVMOptions: 2, InitMemoryMB: 1024, ExtraPorts: 1}, ""},

		{"too many players", BedrockSettings{Type: bedrock, Port: 19132, MaxPlayers: MaxBedrockPlayers + 1}, "1 to 100 players"},
		{"ports beyond 65535", BedrockSettings{Type: bedrock, Port: 65527}, "up to 65535"},
		{"a Java version", BedrockSettings{Type: bedrock, Java: "21"}, "don't run Java"},
		{"Aikar's flags", BedrockSettings{Type: bedrock, AikarFlags: true}, "don't run Java"},
		{"JVM options", BedrockSettings{Type: bedrock, JVMOptions: 1}, "don't run Java"},
		{"an initial heap", BedrockSettings{Type: bedrock, InitMemoryMB: 512}, "don't run Java"},
		{"a mod loader", BedrockSettings{Type: bedrock, LoaderVersion: "0.16.10"}, "don't run Java"},
		{"extra ports", BedrockSettings{Type: bedrock, ExtraPorts: 1}, "no extra ports"},
		{"players of a Paper server", BedrockSettings{Type: paper, MaxPlayers: 20}, "Only Bedrock servers"},
	} {
		checkProblem(t, tc.name, tc.s.Problem(), tc.want)
	}
}

// The image of Bedrock servers sets other properties than that of Java servers.
func TestManagedPropertiesOf(t *testing.T) {
	if got := ManagedPropertiesOf(ServerType_SERVER_TYPE_BEDROCK); got["max-players"] == "" || got["enable-rcon"] != "" {
		t.Errorf("managed properties of Bedrock servers: %v", got)
	}
	if got := ManagedPropertiesOf(ServerType_SERVER_TYPE_PAPER); got["enable-rcon"] == "" || got["max-players"] != "" {
		t.Errorf("managed properties of Paper servers: %v", got)
	}
}
