package node

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/QwikByte/noryx/internal/logging"
	"github.com/QwikByte/noryx/internal/master/access"
	"github.com/QwikByte/noryx/internal/master/httpapi"
	"github.com/QwikByte/noryx/internal/master/operation"
	"github.com/QwikByte/noryx/internal/master/tag"
)

const probeTimeout = 3 * time.Second

// Networks point proxies at the servers of other nodes, through the address of their node.
type Networks interface {
	// ReapplyNode configures the networks again that reach servers of the node from another node.
	ReapplyNode(ctx context.Context, nodeID string) error
	// CheckRemoveNode fails while servers of networks run on the node, with a conflict that
	// release confirms, or while it can't be removed even then.
	CheckRemoveNode(ctx context.Context, nodeID string, release bool) error
	// RemoveNode takes the node's servers out of their networks, if release confirms it, and
	// then removes the node with remove. It returns the servers of other nodes that left a
	// network.
	RemoveNode(ctx context.Context, nodeID string, release bool, remove func() error) ([]tag.Server, error)
}

// FileSets put shared files on servers. Servers that leave a network lose the files with
// secrets of its sets.
type FileSets interface {
	Left(ctx context.Context, servers []tag.Server)
}

// Overlay is the private network of the nodes, whose members reach each other at the
// addresses of their agents unless set otherwise.
type Overlay interface {
	// Reconcile configures its members again, e.g. without a node that was removed.
	Reconcile(ctx context.Context)
}

type Handler struct {
	svc      *Service
	networks Networks
	overlay  Overlay
	ops      *operation.Operations
	sets     FileSets
	// tidy removes the references to what was deleted, e.g. a backup job's copies to a node.
	tidy func(context.Context)
}

func NewHandler(svc *Service, networks Networks, overlay Overlay, ops *operation.Operations, sets FileSets, tidy func(context.Context)) *Handler {
	return &Handler{svc: svc, networks: networks, overlay: overlay, ops: ops, sets: sets, tidy: tidy}
}

// Register adds the routes. Users see the nodes on which they may see the node or servers,
// the details of a node only with the permission to see it.
func (h *Handler) Register(mux access.Mux) {
	mux.Handle("GET /api/nodes", access.SignedIn, h.list)
	mux.Handle("POST /api/nodes", access.Everywhere(access.NodesEnroll), h.create)
	mux.Handle("GET /api/nodes/{id}", access.SignedIn, h.get)
	mux.Handle("PUT /api/nodes/{id}", access.OnNode(access.NodesEdit, "id"), h.update)
	// Taking the servers of a node out of their networks needs the permission to manage them.
	releasing := func(r *http.Request, g access.Grants) (access.Permission, bool) {
		return access.NetworksManage, !releases(r) || g.Has(access.NetworksManage)
	}
	mux.Handle("DELETE /api/nodes/{id}", access.All(access.OnNode(access.NodesDelete, "id"), releasing), h.delete)
	mux.Handle("POST /api/nodes/{id}/join-token", access.Everywhere(access.NodesEnroll), h.joinToken)
	mux.Handle("POST /api/nodes/{id}/certificate", access.OnNode(access.NodesCertificates, "id"), h.renewCertificate)
}

type view struct {
	Node
	Status string `json:"status"` // pending, online or offline
	Info   *info  `json:"info,omitempty"`
	// Warning tells what didn't follow a change, e.g. the networks of a changed address.
	Warning string `json:"warning,omitempty"`
}

// info describes a node. Runtime is e.g. "docker 29.0.0", or "unavailable" while the agent
// can't reach its runtime, whose version is empty then.
type info struct {
	AgentVersion   string            `json:"agentVersion,omitempty"`
	Hostname       string            `json:"hostname,omitempty"`
	OS             string            `json:"os,omitempty"`
	CPUCount       uint32            `json:"cpuCount"`
	MemoryBytes    uint64            `json:"memoryBytes"`
	Runtime        string            `json:"runtime,omitempty"`
	RuntimeName    string            `json:"runtimeName"`
	RuntimeVersion string            `json:"runtimeVersion,omitempty"`
	Storage        []storageLocation `json:"storage"`
}

type storageLocation struct {
	Name       string `json:"name"`
	Path       string `json:"path,omitempty"`
	FreeBytes  uint64 `json:"freeBytes"`
	TotalBytes uint64 `json:"totalBytes"`
}

// conceal leaves out what only users who may see the node get: where the master reaches
// it, its system, agent and certificate, and where it keeps data. Using its servers doesn't
// need them; the name of the runtime tells how to protect their ports.
func (v *view) conceal() {
	v.Address, v.CertificateExpiresAt = "", nil
	if v.Info != nil {
		v.Info.AgentVersion, v.Info.Hostname, v.Info.OS, v.Info.Runtime, v.Info.RuntimeVersion = "", "", "", "", ""
		for i := range v.Info.Storage {
			v.Info.Storage[i].Path = ""
		}
	}
}

// probeFor probes a node for the user of ctx.
func (h *Handler) probeFor(ctx context.Context, n Node) view {
	v := h.probe(ctx, n)
	if !access.From(ctx).On(access.NodesView, n.ID, "") {
		v.conceal()
	}
	return v
}

// probe asks the agent for its machine info, which also tells whether it is reachable. The
// expiry of its certificate is the stored one, as an offline node has to be enrolled again
// once its certificate expired.
func (h *Handler) probe(ctx context.Context, n Node) view {
	v := view{Node: n, Status: "pending"}
	if n.EnrolledAt == nil {
		return v
	}
	v.Status = "offline"
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	res, _, err := h.svc.Status(ctx, n.ID)
	if err != nil {
		return v
	}
	v.Status = "online"
	name, version := res.RuntimeOf()
	v.Info = &info{res.GetAgentVersion(), res.GetHostname(), res.GetOs(), res.GetCpuCount(), res.GetMemoryBytes(), res.GetRuntime(), name, version, []storageLocation{}}
	for _, l := range res.GetStorage() {
		v.Info.Storage = append(v.Info.Storage, storageLocation{l.GetName(), l.GetPath(), l.GetFreeBytes(), l.GetTotalBytes()})
	}
	return v
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	nodes, err := h.svc.List(r.Context())
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	grants := access.From(r.Context())
	nodes = slices.DeleteFunc(nodes, func(n Node) bool { return !grants.SeesNode(n.ID) })
	views := make([]view, len(nodes))
	var wg sync.WaitGroup
	for i, n := range nodes {
		wg.Go(func() { views[i] = h.probeFor(r.Context(), n) })
	}
	wg.Wait()
	httpapi.WriteJSON(w, http.StatusOK, views)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	n, err := h.svc.Get(r.Context(), r.PathValue("id"))
	if err == nil && !access.From(r.Context()).SeesNode(n.ID) {
		err = access.Denied(access.NodesView)
	}
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, h.probeFor(r.Context(), n))
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name    string `json:"name"`
		Address string `json:"address"`
	}
	if err := httpapi.ReadJSON(w, r, &req); err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	n, token, err := h.svc.Create(r.Context(), req.Name, req.Address)
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	logging.Note(r.Context(), slog.String(logging.KeyNode, n.ID), slog.String(logging.KeyNodeName, n.Name))
	body := tokenJSON(token)
	body["node"] = view{Node: n, Status: "pending"}
	httpapi.WriteJSON(w, http.StatusCreated, body)
}

func tokenJSON(t JoinToken) map[string]any {
	return map[string]any{"joinToken": t.String(), "joinTokenExpiresAt": t.ExpiresAt, "installCommand": t.InstallCommand()}
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name    string `json:"name"`
		Address string `json:"address"`
		Settings
	}
	old, err := h.svc.Get(r.Context(), r.PathValue("id"))
	req.Agent = old.Agent // unless the request changes them; clients that don't know them leave them out
	if err == nil {
		err = httpapi.ReadJSON(w, r, &req)
	}
	var n Node
	if err == nil {
		n, err = h.svc.Update(r.Context(), Node{ID: old.ID, Name: req.Name, Address: req.Address, Settings: req.Settings})
	}
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	v := h.probeFor(r.Context(), n)
	// Proxies on other nodes reach the node's servers at the host of its address, and so
	// does the private network.
	if host(old.Address) != host(n.Address) {
		go h.overlay.Reconcile(context.WithoutCancel(r.Context()))
		if err := h.networks.ReapplyNode(r.Context(), n.ID); err != nil {
			v.Warning = err.Error()
		}
	}
	// An agent that doesn't get them now gets them once it is online again.
	if n.EnrolledAt != nil && n.Agent != old.Agent {
		ctx, cancel := context.WithTimeout(r.Context(), probeTimeout)
		defer cancel()
		if err := h.svc.Configure(ctx, n); err != nil {
			v.Warning = strings.TrimSpace(v.Warning + " " + configureWarning(err))
		}
	}
	httpapi.WriteJSON(w, http.StatusOK, v)
}

func host(address string) string {
	h, _, _ := net.SplitHostPort(address)
	return h
}

func (h *Handler) joinToken(w http.ResponseWriter, r *http.Request) {
	token, err := h.svc.NewJoinToken(r.Context(), r.PathValue("id"))
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, tokenJSON(token))
}

// releases tells whether a request to remove a node confirms that its servers leave their
// networks first.
func releases(r *http.Request) bool { return r.URL.Query().Get("release") == "true" }

// delete removes a node, once its servers left their networks, so that no server of another
// node keeps trusting a proxy of the node and no proxy keeps sending players to its servers.
// Unless the request confirms it, a node with servers of networks stays.
func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	id, release := r.PathValue("id"), releases(r)
	n, err := h.svc.Get(r.Context(), id)
	if err == nil {
		err = h.networks.CheckRemoveNode(r.Context(), id, release)
	}
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	logging.Note(r.Context(), slog.String(logging.KeyNodeName, n.Name), slog.Bool("release", release))
	h.ops.Run(w, r, operation.Spec{
		Kind: "node.delete", Subject: n.Name, NodeID: id, Status: http.StatusNoContent, Timeout: time.Hour, Category: logging.Nodes,
		Visible: func(g access.Grants) bool { return g.On(access.NodesView, id, "") },
	}, func(ctx context.Context) (any, error) {
		left, err := h.networks.RemoveNode(ctx, id, release, func() error { return h.svc.Delete(ctx, id) })
		if len(left) > 0 {
			h.sets.Left(ctx, left)
		}
		if err == nil {
			// The other members of the private network drop it right away.
			go h.overlay.Reconcile(context.WithoutCancel(ctx))
		}
		h.tidy(ctx)
		return nil, err
	})
}

func (h *Handler) renewCertificate(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), renewTimeout)
	defer cancel()
	cert, err := h.svc.RenewCertificate(ctx, r.PathValue("id"))
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]time.Time{"certificateExpiresAt": cert.NotAfter})
}
