package noryxv1

import (
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// A server publishes up to MaxExtraPorts ports of plugins, e.g. of a web map and a voice chat,
// each labelled with up to MaxExtraPortLabel characters.
const (
	MaxExtraPorts     = 5
	MaxExtraPortLabel = 32
)

// ContainerPorts are the ports that the container of a server uses itself, which no extra port
// publishes: the game port inside it, BungeeCord's too, and the console port (RCON), which the
// server image sets again on every start.
var ContainerPorts = []uint32{25565, 25575, 25577}

// CheckExtraPorts returns a message for the operator if the extra ports of a server with port
// and bedrockPort are invalid: each is a TCP or UDP port from 1024 to 65535 that the server
// doesn't use otherwise, listed once, with a label of plain text.
func CheckExtraPorts(ports []*ExtraPort, port, bedrockPort uint32) string {
	if len(ports) > MaxExtraPorts {
		return fmt.Sprintf("A server publishes up to %d extra ports.", MaxExtraPorts)
	}
	for i, p := range ports {
		n, label := p.GetPort(), p.GetLabel()
		switch {
		case n < 1024 || n > 65535:
			return "Choose extra ports from 1024 to 65535."
		case p.GetProtocol() != "tcp" && p.GetProtocol() != "udp":
			return fmt.Sprintf("Choose TCP or UDP for port %d.", n)
		case n == port || n == bedrockPort:
			return fmt.Sprintf("Port %d is the server's own port. Choose another extra port.", n)
		case slices.Contains(ContainerPorts, n):
			return fmt.Sprintf("The server's container uses port %d itself. Choose another extra port.", n)
		case slices.ContainsFunc(ports[:i], func(q *ExtraPort) bool { return q.GetPort() == n && q.GetProtocol() == p.GetProtocol() }):
			return fmt.Sprintf("Port %d/%s is listed twice.", n, p.GetProtocol())
		case utf8.RuneCountInString(label) > MaxExtraPortLabel || strings.ContainsFunc(label, func(r rune) bool { return unicode.In(r, unicode.C) }):
			return fmt.Sprintf("Label port %d with up to %d characters of plain text.", n, MaxExtraPortLabel)
		}
	}
	return ""
}

// Uses reports whether a server uses a port of its node: its own, the UDP port at which Bedrock
// players join a proxy, or one of its extra ports.
func (s *Server) Uses(port uint32) bool {
	return s.GetPort() == port || s.GetBedrockPort() != 0 && s.GetBedrockPort() == port ||
		slices.ContainsFunc(s.GetExtraPorts(), func(p *ExtraPort) bool { return p.GetPort() == port })
}
