package plugin

import (
	"cmp"
	"context"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/master/access"
	"github.com/QwikByte/noryx/internal/master/httpapi"
	"github.com/QwikByte/noryx/internal/master/modrinth"
	"github.com/QwikByte/noryx/internal/master/operation"
)

// gatherTimeout is how long a node may take to list the plugins of its servers.
const gatherTimeout = 15 * time.Second

// Everywhere is what the servers a user may see have installed, by project.
type Everywhere struct {
	Projects []InstalledProject `json:"projects"`
	// Unreachable are the nodes, or single servers, whose plugins couldn't be listed.
	Unreachable []Unreachable `json:"unreachable"`
	// CatalogueError tells why some files couldn't be looked up on Modrinth or Hangar.
	CatalogueError string `json:"catalogueError,omitempty"`
}

// InstalledProject is a project of Modrinth or Hangar with its files on servers.
type InstalledProject struct {
	Project Project       `json:"project"`
	Servers []InstalledOn `json:"servers"`
}

// InstalledOn is a file of a project on a server, as the server's listing describes it.
type InstalledOn struct {
	Ref
	Plugin
}

// Unreachable is a node, or a server of it, whose plugins couldn't be listed, and why.
type Unreachable struct {
	NodeID   string `json:"nodeId"`
	NodeName string `json:"nodeName"`
	ServerID string `json:"serverId,omitempty"`
	Error    string `json:"error"`
}

// listing is the plugin files of a server.
type listing struct {
	ref Ref
	srv *noryxv1.Server
	res *noryxv1.ListPluginsResponse
}

// Everywhere gathers the plugins and mods of the servers that grants let see, recognised by
// their hash like those of a single server: all nodes at once, up to operation.PerNode servers
// of a node at a time and each node within gatherTimeout. Servers of the same software and
// Minecraft version are looked up together.
func (s *Service) Everywhere(ctx context.Context, grants access.Grants) (Everywhere, error) {
	nodes, err := s.nodes.List(ctx)
	if err != nil {
		return Everywhere{}, err
	}
	all, err := s.pins.all(ctx, nil)
	if err != nil {
		return Everywhere{}, err
	}
	channels := inUse(slices.Collect(maps.Values(all))...)
	out := Everywhere{Projects: []InstalledProject{}, Unreachable: []Unreachable{}}
	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		listings []listing
	)
	for _, n := range nodes {
		if n.EnrolledAt == nil || !grants.Somewhere(access.ServersView, n.ID) {
			continue
		}
		wg.Go(func() {
			found, errs := s.gather(ctx, n.ID, grants)
			mu.Lock()
			defer mu.Unlock()
			listings = append(listings, found...)
			for serverID, err := range errs {
				out.Unreachable = append(out.Unreachable, Unreachable{NodeID: n.ID, NodeName: n.Name, ServerID: serverID, Error: httpapi.Message(err)})
			}
		})
	}
	wg.Wait()

	// Servers of the same target share the lookups of their files.
	groups := map[string][]listing{}
	targets := map[string]target{}
	for _, l := range listings {
		t, err := s.target(ctx, l.srv.GetType(), l.srv.GetVersion())
		if err != nil {
			out.CatalogueError = cmp.Or(out.CatalogueError, httpapi.Message(err))
			continue
		}
		key := strings.Join(t.loaders, ",") + "|" + t.gameVersion
		groups[key], targets[key] = append(groups[key], l), t
	}
	byProject := map[string]*InstalledProject{}
	for key, group := range groups {
		var files []*noryxv1.PluginFile
		for _, l := range group {
			files = append(files, l.res.GetPlugins()...)
		}
		slices.SortFunc(files, func(a, b *noryxv1.PluginFile) int { return strings.Compare(a.GetSha512(), b.GetSha512()) })
		files = slices.CompactFunc(files, func(a, b *noryxv1.PluginFile) bool { return a.GetSha512() == b.GetSha512() })
		if len(files) == 0 {
			continue
		}
		c, err := s.identify(ctx, targets[key], files, channels)
		if err != nil {
			out.CatalogueError = cmp.Or(out.CatalogueError, httpapi.Message(err))
			continue
		}
		for _, l := range group {
			for _, p := range c.describe(l.res.GetFolder(), l.res.GetPlugins(), all[l.ref]) {
				if p.Project == nil {
					continue
				}
				entry := byProject[p.Project.ID]
				if entry == nil {
					entry = &InstalledProject{Project: *p.Project}
					byProject[p.Project.ID] = entry
				}
				p.Project = nil
				entry.Servers = append(entry.Servers, InstalledOn{Ref: l.ref, Plugin: p})
			}
		}
	}
	for _, p := range byProject {
		out.Projects = append(out.Projects, *p)
	}
	slices.SortFunc(out.Projects, func(a, b InstalledProject) int {
		return cmp.Or(strings.Compare(strings.ToLower(a.Project.Title), strings.ToLower(b.Project.Title)), strings.Compare(a.Project.ID, b.Project.ID))
	})
	return out, nil
}

// gather lists the plugin files of the servers of a node that grants let see, and the error of
// each server whose files couldn't be listed; the node's own error has no server ID.
func (s *Service) gather(ctx context.Context, nodeID string, grants access.Grants) ([]listing, map[string]error) {
	ctx, cancel := context.WithTimeout(ctx, gatherTimeout)
	defer cancel()
	conn, err := s.nodes.Conn(ctx, nodeID)
	var res *noryxv1.ListServersResponse
	if err == nil {
		res, err = noryxv1.NewServerServiceClient(conn).ListServers(ctx, &noryxv1.ListServersRequest{})
	}
	if err != nil {
		return nil, map[string]error{"": err}
	}
	servers := slices.DeleteFunc(res.GetServers(), func(srv *noryxv1.Server) bool {
		return !grants.On(access.ServersView, nodeID, srv.GetId()) || len(modrinth.Loaders(srv.GetType())) == 0
	})
	found := make([]listing, len(servers))
	errs := make([]error, len(servers))
	slots := make(chan struct{}, operation.PerNode)
	var wg sync.WaitGroup
	client := noryxv1.NewPluginServiceClient(conn)
	for i, srv := range servers {
		wg.Go(func() {
			slots <- struct{}{}
			defer func() { <-slots }()
			found[i] = listing{ref: Ref{nodeID, srv.GetId()}, srv: srv}
			found[i].res, errs[i] = client.ListPlugins(ctx, &noryxv1.ListPluginsRequest{ServerId: srv.GetId(), IncludeDisabled: true})
		})
	}
	wg.Wait()
	failed := map[string]error{}
	for i, err := range errs {
		if err != nil {
			failed[servers[i].GetId()] = err
		}
	}
	return slices.DeleteFunc(found, func(l listing) bool { return failed[l.ref.ServerID] != nil }), failed
}
