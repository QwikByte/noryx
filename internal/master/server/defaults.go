package server

import (
	"net/http"
	"regexp"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/master/access"
	"github.com/QwikByte/noryx/internal/master/httpapi"
)

var templateID = regexp.MustCompile(`^[a-z0-9]{0,64}$`)

// NewServers are what the panel creates servers with, as the settings of the master say,
// unless a template is chosen.
type NewServers struct {
	// Type is the software, e.g. paper.
	Type string `json:"type"`
	// MemoryMB is the memory of game servers, ProxyMemoryMB that of proxies.
	MemoryMB      uint32 `json:"memoryMb"`
	ProxyMemoryMB uint32 `json:"proxyMemoryMb"`
	// Java is the Java version of game servers; empty is the newest.
	Java string `json:"java"`
	// StopTimeout is how many seconds servers get to stop gracefully.
	StopTimeout uint32 `json:"stopTimeout"`
	// TimeZone is an IANA time zone such as Europe/Berlin; empty means UTC.
	TimeZone string `json:"timeZone"`
	// Template is the ID of the template the panel chooses instead, for those who may see
	// templates; empty for none. A template that doesn't exist (any more) is ignored.
	Template string `json:"template"`
}

// DefaultNewServers apply until the settings change them.
func DefaultNewServers() NewServers {
	return NewServers{Type: "paper", MemoryMB: 2048, ProxyMemoryMB: 512, StopTimeout: uint32(noryxv1.DefaultStopTimeout.Seconds())}
}

// Validate checks the settings of new servers with the rules of a server's own settings.
func (n NewServers) Validate() error {
	typ := noryxv1.ParseServerType(n.Type)
	memory := func(mb uint32) bool { return mb >= noryxv1.MinMemoryMB && mb <= noryxv1.MaxMemoryMB }
	switch {
	case typ == noryxv1.ServerType_SERVER_TYPE_UNSPECIFIED || typ.Slug() != n.Type:
		return httpapi.Errorf(http.StatusBadRequest, "Choose the software of new servers.")
	case !memory(n.MemoryMB) || !memory(n.ProxyMemoryMB):
		return httpapi.Errorf(http.StatusBadRequest, "Give new servers %d to %d MB of memory.", noryxv1.MinMemoryMB, noryxv1.MaxMemoryMB)
	case !noryxv1.ValidJava(n.Java):
		return httpapi.Errorf(http.StatusBadRequest, "Choose Java 8, 11, 17, 21 or 25 for new game servers, or the newest.")
	case n.StopTimeout == 0 || !noryxv1.ValidStopTimeout(n.StopTimeout):
		return httpapi.Errorf(http.StatusBadRequest, "Give new servers %.0f seconds to %.0f minutes to stop.", noryxv1.MinStopTimeout.Seconds(), noryxv1.MaxStopTimeout.Minutes())
	case !noryxv1.ValidTimeZone(n.TimeZone):
		return httpapi.Errorf(http.StatusBadRequest, "Choose a time zone for new servers such as Europe/Berlin, or none for UTC.")
	case !templateID.MatchString(n.Template):
		return httpapi.Errorf(http.StatusBadRequest, "Choose a template for new servers, or none.")
	}
	return nil
}

// creates needs the permission to create servers somewhere.
func creates(_ *http.Request, g access.Grants) (access.Permission, bool) {
	return access.ServersCreate, g.Somewhere(access.ServersCreate, "")
}

// newServers tells the panel what to create servers with, also those who may create servers
// but not see the settings of the master.
func (h *Handler) newServers(w http.ResponseWriter, _ *http.Request) {
	httpapi.WriteJSON(w, http.StatusOK, h.conf.NewServers())
}
