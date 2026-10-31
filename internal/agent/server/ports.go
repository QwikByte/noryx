package server

import (
	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/agent/runtime"
)

// extraPorts returns the extra ports of a request, which checkSettings checks.
func extraPorts(ports []*noryxv1.ExtraPort) []runtime.ExtraPort {
	var extra []runtime.ExtraPort
	for _, p := range ports {
		extra = append(extra, runtime.ExtraPort{Port: p.GetPort(), Protocol: p.GetProtocol(), Label: p.GetLabel()})
	}
	return extra
}

// protoPorts returns the extra ports of a server for the API.
func protoPorts(ports []runtime.ExtraPort) []*noryxv1.ExtraPort {
	var extra []*noryxv1.ExtraPort
	for _, p := range ports {
		extra = append(extra, &noryxv1.ExtraPort{Port: p.Port, Protocol: p.Protocol, Label: p.Label})
	}
	return extra
}

// portsOf returns the ports of the node that the settings of a server take: its own, those of
// the players of a Bedrock server and its extra ports. The Bedrock port of a proxy is its
// network's.
func portsOf(spec runtime.Spec) []uint32 {
	var ports []uint32
	for port := spec.Port; port <= noryxv1.LastPort(spec.Type, spec.Port, spec.MaxPlayers); port++ {
		ports = append(ports, port)
	}
	for _, p := range spec.ExtraPorts {
		ports = append(ports, p.Port)
	}
	return ports
}
