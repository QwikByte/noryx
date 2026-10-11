package logs

import (
	"cmp"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/QwikByte/noryx/internal/logging"
	"github.com/QwikByte/noryx/internal/master/access"
	"github.com/QwikByte/noryx/internal/master/auth"
)

const maxErrorBody = 4 << 10

// action is how the requests of a route appear in the log.
type action struct {
	category slog.Attr
	message  string
}

const (
	routeServer  = "/api/nodes/{node}/servers/{id}"
	routeFiles   = routeServer + "/files"
	routeBackups = routeServer + "/backups"
)

// actions are the routes whose requests are logged: all that change something, and downloads.
var actions = map[string]action{
	"POST /api/groups":                                         {logging.Users, "Create group"},
	"PUT /api/groups/{id}":                                     {logging.Users, "Change group"},
	"DELETE /api/groups/{id}":                                  {logging.Users, "Delete group"},
	"POST /api/users":                                          {logging.Users, "Invite user"},
	"PUT /api/users/{id}":                                      {logging.Users, "Change user"},
	"DELETE /api/users/{id}":                                   {logging.Users, "Delete user"},
	"POST /api/users/{id}/setup-link":                          {logging.Users, "Create setup link"},
	"DELETE /api/users/{id}/mfa":                               {logging.Users, "Turn off two-factor authentication"},
	"PUT /api/settings":                                        {logging.Settings, "Change settings"},
	"POST /api/master/restart":                                 {logging.System, "Restart master"},
	"POST /api/update/check":                                   {logging.System, "Check for updates"},
	"POST /api/update/master":                                  {logging.System, "Update master"},
	"POST /api/update/agents":                                  {logging.Nodes, "Update agents"},
	"POST /api/update/agents/{node}":                           {logging.Nodes, "Update agent"},
	"POST /api/terminal":                                       {logging.Terminal, "Run terminal command"},
	"POST /api/operations/{id}/cancel":                         {logging.System, "Cancel operation"}, // noted in the operation's category
	"GET /api/logs/export":                                     {logging.System, "Export log"},
	"POST /api/nodes":                                          {logging.Nodes, "Add node"},
	"PUT /api/nodes/{id}":                                      {logging.Nodes, "Change node"},
	"DELETE /api/nodes/{id}":                                   {logging.Nodes, "Remove node"},
	"POST /api/nodes/{id}/join-token":                          {logging.Nodes, "Create join token"},
	"POST /api/nodes/{id}/certificate":                         {logging.Nodes, "Renew node certificate"},
	"PUT /api/overlay":                                         {logging.Nodes, "Change private network"},
	"POST /api/nodes/{id}/overlay":                             {logging.Nodes, "Add node to private network"},
	"PUT /api/nodes/{id}/overlay":                              {logging.Nodes, "Change endpoint in private network"},
	"DELETE /api/nodes/{id}/overlay":                           {logging.Nodes, "Remove node from private network"},
	"POST /api/nodes/{id}/overlay/rotate":                      {logging.Nodes, "Rotate key in private network"},
	"POST /api/nodes/{id}/overlay/peers/{peer}/test":           {logging.Nodes, "Test connections in private network"},
	"PUT /api/nodes/{node}/usage/thresholds":                   {logging.Nodes, "Change usage warnings of node"},
	"POST /api/nodes/{node}/servers":                           {logging.Servers, "Create server"},
	"POST /api/nodes/{node}/servers/import":                    {logging.Servers, "Create server from archive"},
	"POST /api/servers/actions":                                {logging.Servers, "Run action on servers"},
	"POST /api/servers/tags":                                   {logging.Servers, "Change server tags"},
	"PUT " + routeServer:                                       {logging.Servers, "Change server settings"},
	"POST " + routeServer + "/update-image":                    {logging.Servers, "Update server image"},
	"PUT " + routeServer + "/notes":                            {logging.Servers, "Change server notes"},
	"PUT " + routeServer + "/usage/thresholds":                 {logging.Servers, "Change usage warnings of server"},
	"DELETE " + routeServer:                                    {logging.Servers, "Delete server"},
	"POST " + routeServer + "/start":                           {logging.Servers, "Start server"},
	"POST " + routeServer + "/stop":                            {logging.Servers, "Stop server"},
	"POST " + routeServer + "/restart":                         {logging.Servers, "Restart server"},
	"POST " + routeServer + "/duplicate":                       {logging.Servers, "Duplicate server"},
	"POST " + routeServer + "/move":                            {logging.Servers, "Move server"},
	"POST " + routeServer + "/command":                         {logging.Console, "Send console command"},
	"PUT " + routeServer + "/properties":                       {logging.Files, "Change server.properties"},
	"PUT " + routeServer + "/proxy":                            {logging.Files, "Change proxy settings"},
	"GET " + routeFiles + "/content":                           {logging.Files, "Download file"},
	"GET " + routeFiles + "/archive":                           {logging.Files, "Download folder"},
	"PUT " + routeFiles + "/content":                           {logging.Files, "Upload file"},
	"POST " + routeFiles + "/directories":                      {logging.Files, "Create folder"},
	"POST " + routeFiles + "/move":                             {logging.Files, "Move file"},
	"POST " + routeFiles + "/copy":                             {logging.Files, "Copy file"},
	"POST " + routeFiles + "/extract":                          {logging.Files, "Extract archive"},
	"DELETE " + routeFiles:                                     {logging.Files, "Delete file"},
	"POST /api/plugins/install":                                {logging.Plugins, "Install plugins"},
	"PUT " + routeServer + "/plugins/{file}":                   {logging.Plugins, "Upload plugin"},
	"DELETE " + routeServer + "/plugins/{file}":                {logging.Plugins, "Remove plugin"},
	"POST " + routeServer + "/modpack":                         {logging.Plugins, "Update modpack"},
	"POST /api/plugins/update":                                 {logging.Plugins, "Update plugins"},
	"POST /api/plugins/remove":                                 {logging.Plugins, "Remove plugins"},
	"POST " + routeServer + "/plugins/{file}/enable":           {logging.Plugins, "Turn plugin on"},
	"POST " + routeServer + "/plugins/{file}/disable":          {logging.Plugins, "Turn plugin off"},
	"PUT " + routeServer + "/plugins/pins/{project}":           {logging.Plugins, "Keep plugin version"},
	"DELETE " + routeServer + "/plugins/pins/{project}":        {logging.Plugins, "Stop keeping plugin version"},
	"POST " + routeBackups:                                     {logging.Backups, "Back up server"},
	"POST " + routeBackups + "/{backup}/restore":               {logging.Backups, "Restore backup"},
	"POST " + routeBackups + "/{backup}/restore-into":          {logging.Backups, "Restore backup into another server"},
	"PATCH " + routeBackups + "/{backup}":                      {logging.Backups, "Change backup"},
	"DELETE " + routeBackups + "/{backup}":                     {logging.Backups, "Delete backup"},
	"GET " + routeBackups + "/{backup}/download":               {logging.Backups, "Download backup"},
	"POST " + routeBackups + "/upload":                         {logging.Backups, "Upload backup"},
	"POST /api/backup-jobs":                                    {logging.Backups, "Create backup job"},
	"PUT /api/backup-jobs/{id}":                                {logging.Backups, "Change backup job"},
	"DELETE /api/backup-jobs/{id}":                             {logging.Backups, "Delete backup job"},
	"POST /api/backup-jobs/{id}/run":                           {logging.Backups, "Run backup job"},
	"POST /api/backup-storages":                                {logging.Backups, "Add storage for copies of backups"},
	"PUT /api/backup-storages/{id}":                            {logging.Backups, "Change storage for copies of backups"},
	"DELETE /api/backup-storages/{id}":                         {logging.Backups, "Delete storage for copies of backups"},
	"POST " + routeServer + "/copies/{copy}/restore":           {logging.Backups, "Restore copy of backup"},
	"DELETE " + routeServer + "/copies/{copy}":                 {logging.Backups, "Delete copy of backup"},
	"POST /api/backup-copies/{copy}/restore":                   {logging.Backups, "Restore copy of backup"},
	"DELETE /api/backup-copies/{copy}":                         {logging.Backups, "Delete copy of backup"},
	"POST /api/policies":                                       {logging.Policies, "Create schedule"},
	"PUT /api/policies/{id}":                                   {logging.Policies, "Change schedule"},
	"DELETE /api/policies/{id}":                                {logging.Policies, "Delete schedule"},
	"POST /api/policies/{id}/run":                              {logging.Policies, "Run schedule"},
	"POST /api/workflows":                                      {logging.Workflows, "Create workflow"},
	"PUT /api/workflows/{id}":                                  {logging.Workflows, "Change workflow"},
	"DELETE /api/workflows/{id}":                               {logging.Workflows, "Delete workflow"},
	"POST /api/workflows/{id}/run":                             {logging.Workflows, "Run workflow"},
	"POST /api/workflows/{id}/runs/{run}/cancel":               {logging.Workflows, "Cancel workflow run"},
	"POST /api/workflows/{id}/hook":                            {logging.Workflows, "Create workflow URL"},
	"DELETE /api/workflows/{id}/hook":                          {logging.Workflows, "Delete workflow URL"},
	"POST /api/networks":                                       {logging.Networks, "Create network"},
	"PUT /api/networks/{id}":                                   {logging.Networks, "Change network"},
	"DELETE /api/networks/{id}":                                {logging.Networks, "Delete network"},
	"POST /api/networks/{id}/proxy":                            {logging.Networks, "Change network proxy"},
	"POST /api/networks/{id}/apply":                            {logging.Networks, "Apply network"},
	"POST /api/networks/{id}/start":                            {logging.Networks, "Start network"},
	"POST /api/networks/{id}/stop":                             {logging.Networks, "Stop network"},
	"POST /api/networks/{id}/restart":                          {logging.Networks, "Restart network"},
	"POST /api/networks/{id}/broadcast":                        {logging.Networks, "Send message to network"},
	"POST /api/networks/{id}/rolling-restart":                  {logging.Networks, "Restart network server by server"},
	"POST /api/networks/{id}/maintenance":                      {logging.Networks, "Change network maintenance"},
	"POST /api/networks/{id}/maintenance/abort":                {logging.Networks, "Abort network maintenance timer"},
	"POST /api/networks/{id}/maintenance/players":              {logging.Networks, "Change who may join during maintenance"},
	"POST /api/networks/{id}/players/send":                     {logging.Players, "Send player to server"},
	"POST /api/networks/{id}/players/move":                     {logging.Players, "Send players to another server"},
	"POST /api/players/actions":                                {logging.Players, "Change player"},
	"POST /api/players/message":                                {logging.Players, "Send message to players"},
	"DELETE /api/players/seen":                                 {logging.Players, "Delete players seen"},
	"POST /api/templates":                                      {logging.Templates, "Create template"},
	"POST /api/templates/import":                               {logging.Templates, "Import template"},
	"GET /api/templates/{id}/export":                           {logging.Templates, "Export template"},
	"PUT /api/templates/{id}":                                  {logging.Templates, "Change template"},
	"DELETE /api/templates/{id}":                               {logging.Templates, "Delete template"},
	"POST /api/networks/{id}/datastores":                       {logging.Databases, "Create datastore"},
	"PATCH /api/datastores/{id}":                               {logging.Databases, "Change datastore"},
	"POST /api/datastores/{id}/start":                          {logging.Databases, "Start datastore"},
	"POST /api/datastores/{id}/stop":                           {logging.Databases, "Stop datastore"},
	"DELETE /api/datastores/{id}":                              {logging.Databases, "Delete datastore"},
	"POST /api/datastores/{id}/databases":                      {logging.Databases, "Create database"},
	"DELETE /api/datastores/{id}/databases/{name}":             {logging.Databases, "Drop database"},
	"POST /api/datastores/{id}/databases/{name}/rotate":        {logging.Databases, "Rotate database password"},
	"GET /api/datastores/{id}/databases/{name}/password":       {logging.Databases, "Show database password"},
	"GET /api/datastores/{id}/databases/{name}/tables/{table}": {logging.Databases, "Browse database table"},
	"POST /api/datastores/{id}/backups":                        {logging.Databases, "Back up datastore"},
	"POST /api/datastores/{id}/backups/upload":                 {logging.Databases, "Upload datastore backup"},
	"POST /api/datastores/{id}/backups/{backup}/restore":       {logging.Databases, "Restore datastore backup"},
	"DELETE /api/datastores/{id}/backups/{backup}":             {logging.Databases, "Delete datastore backup"},
	"GET /api/datastores/{id}/backups/{backup}/download":       {logging.Databases, "Download datastore backup"},
	"POST /api/filesets":                                       {logging.Files, "Create file set"},
	"PUT /api/filesets/{id}":                                   {logging.Files, "Change file set"},
	"DELETE /api/filesets/{id}":                                {logging.Files, "Delete file set"},
	"PUT /api/filesets/{id}/secrets/{name}":                    {logging.Files, "Set secret of file set"},
	"DELETE /api/filesets/{id}/secrets/{name}":                 {logging.Files, "Delete secret of file set"},
	"POST /api/filesets/{id}/preview":                          {logging.Files, "Preview file set"},
	"POST /api/filesets/{id}/apply":                            {logging.Files, "Apply file set"},
	"POST /api/notifications/channels":                         {logging.Notifications, "Create notification channel"},
	"PUT /api/notifications/channels/{id}":                     {logging.Notifications, "Change notification channel"},
	"DELETE /api/notifications/channels/{id}":                  {logging.Notifications, "Delete notification channel"},
	"POST /api/notifications/channels/{id}/test":               {logging.Notifications, "Send test notification"},
	"POST /api/notifications/rules":                            {logging.Notifications, "Create notification rule"},
	"PUT /api/notifications/rules/{id}":                        {logging.Notifications, "Change notification rule"},
	"DELETE /api/notifications/rules/{id}":                     {logging.Notifications, "Delete notification rule"},
}

var wildcard = regexp.MustCompile(`\{(\w+)\}`)

// Audit returns the wrapper that logs the requests of the actions: who made them, from
// where, about which node and server, and how they ended. Handlers can add details with
// logging.Note. It panics for routes that change something but have no action, so none is
// left out.
func (s *Store) Audit() access.Wrapper {
	names := s.names
	return func(pattern string, h http.HandlerFunc) http.HandlerFunc {
		a, ok := actions[pattern]
		if method, _, _ := strings.Cut(pattern, " "); !ok && method != http.MethodGet {
			panic("logs: the route " + pattern + " has no action")
		}
		if !ok {
			return h
		}
		return func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			attrs := append(target(r, pattern, names), a.category, slog.String("ip", auth.ClientIP(r)))
			if user, ok := auth.UserFrom(r.Context()); ok {
				attrs = append(attrs, user.LogAttrs()...)
			}
			ctx, notes := logging.WithNotes(r.Context())
			rec := &recorder{ResponseWriter: w}
			h(rec, r.WithContext(ctx))

			// Other failed requests, e.g. with an invalid input or a port in use, are no warning:
			// the panel tells the user who made them why, and nobody else needs to know.
			status, level, message := cmp.Or(rec.status, http.StatusOK), slog.LevelInfo, a.message
			switch {
			case status == http.StatusUnauthorized || status == http.StatusForbidden:
				level, message = slog.LevelWarn, message+" denied"
			case status >= http.StatusInternalServerError:
				level, message = slog.LevelError, message+" failed"
			case status >= http.StatusBadRequest:
				message += " failed"
			}
			attrs = append(attrs, slog.Int("status", status), slog.Duration("duration", time.Since(start).Round(time.Millisecond)))
			if status >= http.StatusBadRequest {
				var body struct {
					Error string `json:"error"`
				}
				_ = json.Unmarshal(rec.body, &body)
				attrs = append(attrs, slog.String("err", body.Error))
			}
			slog.LogAttrs(context.WithoutCancel(ctx), level, message, append(attrs, notes()...)...)
		}
	}
}

// target returns the node and the server a request is about with their names, which are
// looked up first, as the request may delete them, and its other path values, e.g. the
// ID of a group, and the path of a file.
func target(r *http.Request, pattern string, names *Names) []slog.Attr {
	var attrs []slog.Attr
	nodeID, serverID := r.PathValue("node"), ""
	for _, m := range wildcard.FindAllStringSubmatch(pattern, -1) {
		name, value := m[1], r.PathValue(m[1])
		switch {
		case name == "node":
		case name == "id" && nodeID != "":
			serverID = value
		case name == "id" && strings.Contains(pattern, " /api/nodes/{id}"):
			nodeID = value
		case name == logging.KeyServer: // a server of a network, whose node the route doesn't name
			attrs = append(attrs, slog.String("server_id", value))
		default:
			attrs = append(attrs, slog.String(name, value))
		}
	}
	if path := r.URL.Query().Get("path"); path != "" {
		attrs = append(attrs, slog.String("path", path))
	}
	if nodeID != "" {
		attrs = append(attrs, slog.String(logging.KeyNode, nodeID), slog.String(logging.KeyNodeName, names.Node(r.Context(), nodeID)))
	}
	if serverID != "" {
		attrs = append(attrs, slog.String(logging.KeyServer, serverID), slog.String(logging.KeyServerName, names.Server(r.Context(), nodeID, serverID)))
	}
	return attrs
}

// recorder keeps the status of a response and the start of an error's body.
type recorder struct {
	http.ResponseWriter
	status int
	body   []byte
}

func (r *recorder) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
	r.ResponseWriter.WriteHeader(status)
}

func (r *recorder) Write(p []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	if r.status >= http.StatusBadRequest {
		r.body = append(r.body, p[:min(len(p), maxErrorBody-len(r.body))]...)
	}
	return r.ResponseWriter.Write(p)
}

// Unwrap lets http.ResponseController reach the writer, e.g. to flush a stream.
func (r *recorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }
