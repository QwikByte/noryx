package network

import (
	"context"
	"net/http"
	"slices"
	"time"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/master/httpapi"
	"github.com/QwikByte/noryx/internal/master/operation"
	"github.com/QwikByte/noryx/internal/master/plugin"
)

// maintenanceProject is the Maintenance plugin by kennytv on Modrinth, which runs on
// Velocity and BungeeCord.
const maintenanceProject = "VCAqN1ln"

const (
	// pluginTimeout is how long a proxy may take to load the plugin after it started.
	pluginTimeout = time.Minute
	// maintenanceWait is how long the agent waits for the plugin to save a change.
	maintenanceWait = 15 * time.Second
)

// Maintenance is the maintenance of a network, as the Maintenance plugin of its proxy keeps it.
type Maintenance struct {
	// Installed tells that the proxy loaded the plugin.
	Installed bool `json:"installed"`
	Enabled   bool `json:"enabled"`
	// Players may join during maintenance.
	Players []MaintenancePlayer `json:"players"`
	// Servers are the game servers of the network in maintenance on their own, by name.
	Servers []string `json:"servers"`
	// EndsAt is when the maintenance of the whole network ends, if the plugin keeps its end
	// timer over restarts. It keeps its other timers to itself.
	EndsAt       *time.Time `json:"endsAt,omitempty"`
	ProxyRunning bool       `json:"proxyRunning"`
}

type MaintenancePlayer struct {
	Name string `json:"name"`
	UUID string `json:"uuid"`
}

// MaintenanceChange turns maintenance on or off, for the whole network or one of its game
// servers, now or after a delay. Maintenance that starts may end after a duration.
type MaintenanceChange struct {
	Enabled bool `json:"enabled"`
	// Server is the name of a game server of the network; empty for the whole network.
	Server string `json:"server"`
	// Delay is the minutes until maintenance starts or ends; 0 for now.
	Delay uint32 `json:"delay"`
	// Duration is the minutes that maintenance lasts which starts; 0 until it is ended.
	Duration uint32 `json:"duration"`
}

// requests returns the commands of the plugin for a change, which they check: maintenance
// that starts now and ends later is turned on and gets a timer to end.
func (c MaintenanceChange) requests() ([]*noryxv1.ChangeMaintenanceRequest, error) {
	req := func(change noryxv1.MaintenanceChange, minutes, duration uint32) *noryxv1.ChangeMaintenanceRequest {
		return &noryxv1.ChangeMaintenanceRequest{Change: change, Server: c.Server, Minutes: minutes, DurationMinutes: duration}
	}
	var reqs []*noryxv1.ChangeMaintenanceRequest
	switch {
	case !c.Enabled && c.Delay == 0:
		reqs = append(reqs, req(noryxv1.MaintenanceChange_MAINTENANCE_CHANGE_OFF, 0, c.Duration))
	case !c.Enabled:
		reqs = append(reqs, req(noryxv1.MaintenanceChange_MAINTENANCE_CHANGE_END_TIMER, c.Delay, c.Duration))
	case c.Delay > 0 && c.Duration > 0:
		reqs = append(reqs, req(noryxv1.MaintenanceChange_MAINTENANCE_CHANGE_SCHEDULE, c.Delay, c.Duration))
	case c.Delay > 0:
		reqs = append(reqs, req(noryxv1.MaintenanceChange_MAINTENANCE_CHANGE_START_TIMER, c.Delay, 0))
	default:
		reqs = append(reqs, req(noryxv1.MaintenanceChange_MAINTENANCE_CHANGE_ON, 0, 0))
		if c.Duration > 0 {
			reqs = append(reqs, req(noryxv1.MaintenanceChange_MAINTENANCE_CHANGE_END_TIMER, c.Duration, 0))
		}
	}
	for _, r := range reqs {
		if problem := r.Problem(); problem != "" {
			return nil, httpapi.Errorf(http.StatusBadRequest, "%s", problem)
		}
	}
	return reqs, nil
}

// Maintenance tells whether a network is in maintenance.
func (s *Service) Maintenance(ctx context.Context, n Network) (Maintenance, error) {
	proxy, err := s.server(ctx, n.Proxy)
	if err != nil {
		return Maintenance{}, err
	}
	var res *noryxv1.GetMaintenanceResponse
	err = s.proxy(ctx, n, func(ctx context.Context, c noryxv1.ProxyServiceClient) (err error) {
		res, err = c.GetMaintenance(ctx, &noryxv1.GetMaintenanceRequest{ServerId: n.Proxy.ServerID})
		return err
	})
	return maintenanceOf(n, res.GetMaintenance(), proxy.GetState() == noryxv1.ServerState_SERVER_STATE_RUNNING), err
}

// SetMaintenance changes the maintenance of a network or of one of its game servers. The
// first time maintenance starts, the plugin is installed on the proxy, which restarts to
// load it.
func (s *Service) SetMaintenance(ctx context.Context, n Network, c MaintenanceChange) (Maintenance, error) {
	ctx = context.WithoutCancel(ctx)
	reqs, err := c.requests()
	if err == nil {
		err = n.checkServer(c.Server)
	}
	if err != nil {
		return Maintenance{}, err
	}
	m, err := s.Maintenance(ctx, n)
	switch {
	case err != nil:
		return m, err
	case !m.ProxyRunning:
		return m, httpapi.Errorf(http.StatusConflict, "Start the proxy of the network first.")
	case !m.Installed && !c.Enabled:
		return m, nil
	case !m.Installed:
		operation.Step(ctx, "plugin")
		if err := s.mods.Ensure(ctx, plugin.Ref(n.Proxy), maintenanceProject); err != nil {
			return m, err
		}
		operation.Step(ctx, "proxy-restart")
		if err := s.restart(ctx, []Backend{{Ref: n.Proxy, Name: "The proxy"}}); err != nil {
			return m, err
		}
		// The plugin creates its files once the proxy loaded it.
		err := waitFor(ctx, pluginTimeout, func(ctx context.Context) (bool, error) {
			m, err := s.Maintenance(ctx, n)
			return m.Installed, err
		})
		if err != nil {
			return m, timedOut(err, "The proxy didn't load the Maintenance plugin. Its console tells why.")
		}
	}
	operation.Step(ctx, "maintenance")
	for _, req := range reqs {
		if m, err = s.changeMaintenance(ctx, n, req); err != nil {
			return m, err
		}
	}
	return m, nil
}

// AbortMaintenanceTimer aborts the timer of the plugin for a network or one of its game
// servers, if one runs.
func (s *Service) AbortMaintenanceTimer(ctx context.Context, n Network, server string) (Maintenance, error) {
	if err := n.checkServer(server); err != nil {
		return Maintenance{}, err
	}
	return s.changeMaintenance(ctx, n, &noryxv1.ChangeMaintenanceRequest{Change: noryxv1.MaintenanceChange_MAINTENANCE_CHANGE_ABORT_TIMER, Server: server})
}

// checkServer fails unless name is empty, for the whole network, or names a game server of it.
func (n *Network) checkServer(name string) error {
	if name != "" && !slices.ContainsFunc(n.Backends, func(b Backend) bool { return b.Name == name }) {
		return httpapi.Errorf(http.StatusBadRequest, "Choose a server of the network.")
	}
	return nil
}

// ChangeMaintenancePlayer adds or removes a player who may join during maintenance.
func (s *Service) ChangeMaintenancePlayer(ctx context.Context, n Network, add bool, player string) (Maintenance, error) {
	change := noryxv1.MaintenanceChange_MAINTENANCE_CHANGE_REMOVE
	if add {
		change = noryxv1.MaintenanceChange_MAINTENANCE_CHANGE_ADD
	}
	return s.changeMaintenance(ctx, n, &noryxv1.ChangeMaintenanceRequest{Change: change, Player: player})
}

func (s *Service) changeMaintenance(ctx context.Context, n Network, req *noryxv1.ChangeMaintenanceRequest) (Maintenance, error) {
	if problem := req.Problem(); problem != "" {
		return Maintenance{}, httpapi.Errorf(http.StatusBadRequest, "%s", problem)
	}
	req.ServerId = n.Proxy.ServerID
	var res *noryxv1.ChangeMaintenanceResponse
	err := s.proxy(ctx, n, func(ctx context.Context, c noryxv1.ProxyServiceClient) (err error) {
		res, err = c.ChangeMaintenance(ctx, req)
		return err
	})
	return maintenanceOf(n, res.GetMaintenance(), true), err
}

// proxy calls the ProxyService of the agent of a network's proxy, with time for the plugin to
// look up a player who never joined.
func (s *Service) proxy(ctx context.Context, n Network, call func(ctx context.Context, c noryxv1.ProxyServiceClient) error) error {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout+maintenanceWait)
	defer cancel()
	conn, err := s.nodes.Conn(ctx, n.Proxy.NodeID)
	if err != nil {
		return err
	}
	return call(ctx, noryxv1.NewProxyServiceClient(conn))
}

// maintenanceOf returns the maintenance the agent read, with the servers in maintenance that
// are game servers of the network.
func maintenanceOf(n Network, m *noryxv1.Maintenance, proxyRunning bool) Maintenance {
	out := Maintenance{
		Installed: m.GetInstalled(), Enabled: m.GetEnabled(), Players: []MaintenancePlayer{}, Servers: []string{},
		ProxyRunning: proxyRunning,
	}
	for _, p := range m.GetPlayers() {
		out.Players = append(out.Players, MaintenancePlayer{p.GetName(), p.GetUuid()})
	}
	for _, b := range n.Backends {
		if slices.Contains(m.GetServers(), b.Name) {
			out.Servers = append(out.Servers, b.Name)
		}
	}
	if m.GetEndsAt() > 0 {
		end := time.Unix(m.GetEndsAt(), 0)
		out.EndsAt = &end
	}
	return out
}
