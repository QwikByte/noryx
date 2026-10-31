package noryxv1

import (
	"strings"
	"testing"
)

// Extra ports are TCP or UDP ports from 1024 that neither the server nor its container
// uses otherwise, each listed once with a label of plain text.
func TestCheckExtraPorts(t *testing.T) {
	const port, bedrock = 25566, 19132
	p := func(n uint32, protocol, label string) *ExtraPort {
		return &ExtraPort{Port: n, Protocol: protocol, Label: label}
	}
	six := []*ExtraPort{p(8100, "tcp", ""), p(8101, "tcp", ""), p(8102, "tcp", ""), p(8103, "tcp", ""), p(8104, "tcp", "")}
	for _, tc := range []struct {
		name  string
		ports []*ExtraPort
		want  string
	}{
		{"none", nil, ""},
		{"a web map", []*ExtraPort{p(8100, "tcp", "BlueMap")}, ""},
		{"the same port with both protocols", []*ExtraPort{p(24454, "udp", "Voice chat"), p(24454, "tcp", "")}, ""},
		{"the lowest and highest", []*ExtraPort{p(1024, "tcp", ""), p(65535, "udp", "")}, ""},
		{"as many as allowed", six[:5], ""},
		{"a label of characters, not bytes", []*ExtraPort{p(8100, "tcp", strings.Repeat("ä", MaxExtraPortLabel))}, ""},

		{"too many", append(six[:5:5], p(8105, "tcp", "")), "up to 5"},
		{"a privileged port", []*ExtraPort{p(1023, "tcp", "")}, "from 1024 to 65535"},
		{"port 0", []*ExtraPort{p(0, "tcp", "")}, "from 1024 to 65535"},
		{"beyond the last port", []*ExtraPort{p(65536, "tcp", "")}, "from 1024 to 65535"},
		{"no protocol", []*ExtraPort{p(8100, "", "")}, "TCP or UDP"},
		{"another protocol", []*ExtraPort{p(8100, "sctp", "")}, "TCP or UDP"},
		{"an upper-case protocol", []*ExtraPort{p(8100, "TCP", "")}, "TCP or UDP"},
		{"the server's port", []*ExtraPort{p(port, "tcp", "")}, "server's own port"},
		{"the server's Bedrock port", []*ExtraPort{p(bedrock, "udp", "")}, "server's own port"},
		{"the game port in the container", []*ExtraPort{p(25565, "tcp", "")}, "container uses port 25565"},
		{"the console port in the container", []*ExtraPort{p(25575, "tcp", "")}, "container uses port 25575"},
		{"the proxy port in the container", []*ExtraPort{p(25577, "udp", "")}, "container uses port 25577"},
		{"twice", []*ExtraPort{p(8100, "tcp", "a"), p(8100, "tcp", "b")}, "listed twice"},
		{"a long label", []*ExtraPort{p(8100, "tcp", strings.Repeat("a", MaxExtraPortLabel+1))}, "up to 32 characters"},
		{"a label of two lines", []*ExtraPort{p(8100, "tcp", "Map\nVoice")}, "plain text"},
		{"a label that turns text around", []*ExtraPort{p(8100, "tcp", "pam\u202e")}, "plain text"},
		{"a label with an invisible character", []*ExtraPort{p(8100, "tcp", "Map\u200b")}, "plain text"},
	} {
		checkProblem(t, tc.name, CheckExtraPorts(tc.ports, port, bedrock), tc.want)
	}
}

func TestServerUses(t *testing.T) {
	s := &Server{Port: 25566, ExtraPorts: []*ExtraPort{{Port: 8100, Protocol: "tcp"}}}
	checkValid(t, s.Uses, []uint32{25566, 8100}, []uint32{0, 19132, 25565, 8101})
	s.BedrockPort = 19132
	checkValid(t, s.Uses, []uint32{25566, 8100, 19132}, []uint32{0, 19133})
}
