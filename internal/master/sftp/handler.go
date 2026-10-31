package sftp

import (
	"net"
	"net/http"
	"strconv"

	"github.com/QwikByte/noryx/internal/master/access"
	"github.com/QwikByte/noryx/internal/master/httpapi"
)

// Config tells where SFTP listens, empty while it's off, and the fingerprint of its host key.
type Config interface {
	SFTP() (addr, hostKey string)
}

// Handler tells the panel how to reach the files of a server over SFTP.
type Handler struct {
	conf Config
	tree *tree
}

func NewHandler(conf Config, nodes Nodes) *Handler { return &Handler{conf: conf, tree: newTree(nodes)} }

func (h *Handler) Register(mux access.Mux) {
	mux.Handle("GET /api/nodes/{node}/servers/{id}/sftp", access.OnServer(access.FilesRead), h.get)
}

// connection is how to connect to the folder of a server; empty while SFTP is off.
type connection struct {
	Port    int    `json:"port,omitempty"`
	HostKey string `json:"hostKey,omitempty"`
	Path    string `json:"path,omitempty"`
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	addr, hostKey := h.conf.SFTP()
	if addr == "" {
		httpapi.WriteJSON(w, http.StatusOK, connection{})
		return
	}
	path, err := h.tree.path(r.Context(), r.PathValue("node"), r.PathValue("id"))
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	_, port, _ := net.SplitHostPort(addr)
	n, _ := strconv.Atoi(port)
	httpapi.WriteJSON(w, http.StatusOK, connection{Port: n, HostKey: hostKey, Path: path})
}
