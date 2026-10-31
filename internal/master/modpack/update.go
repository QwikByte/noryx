package modpack

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/master/backup"
	"github.com/QwikByte/noryx/internal/master/httpapi"
	"github.com/QwikByte/noryx/internal/master/modrinth"
	"github.com/QwikByte/noryx/internal/master/operation"
)

// updateTimeout covers backing up a server with large worlds, downloading the pack and
// creating the server's container again.
const updateTimeout = 3 * time.Hour

var errOlderAgent = httpapi.Errorf(http.StatusNotImplemented, "Update the agent of the node to update the modpacks of its servers.")

// defaultBackup is what an update backs up unless the request chooses: the worlds, plugins
// and mods and the configuration, as the panel suggests, in the default storage location.
var defaultBackup = backup.JobSettings{Selection: backup.Selection{Worlds: true, Plugins: true, Config: true}}

// Change is what an update of the modpack of a server did.
type Change struct {
	// Number is the version of the pack the server has now.
	Number string `json:"number"`
	// Backup is the label of the backup made before; empty if the update made none.
	Backup  string   `json:"backup"`
	Written []string `json:"written"`
	Removed []string `json:"removed"`
	// Kept are files the new version changes or no longer has, which stay as they are, as the
	// administrator changed them since the pack wrote them, or the pack didn't write them.
	Kept []string `json:"kept"`
	// Warning tells that the server didn't start again.
	Warning string `json:"warning,omitempty"`
}

// update is a server about to move to another version of its pack.
type update struct {
	nodeID  string
	server  *noryxv1.Server
	from    installed
	version string // the ID of the version to install
	// backup is what of the server is backed up before, and where; nil for no backup.
	backup  *backup.JobSettings
	release func()
}

// prepare checks that a server can move to a version of its pack, before the update
// downloads it, and keeps others from updating the server meanwhile, until release.
func (s *Service) prepare(ctx context.Context, nodeID, serverID, version string, b *backup.JobSettings) (update, error) {
	from, ok, err := s.installed(ctx, nodeID, serverID)
	switch {
	case err != nil:
		return update{}, err
	case !ok:
		return update{}, httpapi.Errorf(http.StatusNotFound, "The server wasn't created from a modpack, or before Noryx remembered modpacks.")
	case !modrinth.ValidProjectID(version):
		return update{}, httpapi.Errorf(http.StatusBadRequest, "Choose a version of the modpack.")
	case version == from.version:
		return update{}, httpapi.Errorf(http.StatusConflict, "The server has this version of the modpack already.")
	}
	conn, err := s.nodes.Conn(ctx, nodeID)
	var res *noryxv1.ListServersResponse
	if err == nil {
		res, err = noryxv1.NewServerServiceClient(conn).ListServers(ctx, &noryxv1.ListServersRequest{})
	}
	if err != nil {
		return update{}, err
	}
	i := slices.IndexFunc(res.GetServers(), func(srv *noryxv1.Server) bool { return srv.GetId() == serverID })
	if i < 0 {
		return update{}, httpapi.Errorf(http.StatusNotFound, "Server not found.")
	}
	key := nodeID + "/" + serverID
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.updating[key] {
		return update{}, httpapi.Errorf(http.StatusConflict, "The modpack of the server is being updated already.")
	}
	s.updating[key] = true
	release := func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(s.updating, key)
	}
	return update{nodeID: nodeID, server: res.GetServers()[i], from: from, version: version, backup: b, release: release}, nil
}

// steps are those an update plans; the agent adds those of creating the container again.
func (u update) steps() []string {
	stopped := u.server.GetState() == noryxv1.ServerState_SERVER_STATE_STOPPED
	return slices.DeleteFunc([]string{"modpack", "stop", "archive", "mods"}, func(step string) bool {
		return step == "stop" && stopped || step == "archive" && u.backup == nil
	})
}

// run updates the modpack of a server as an operation. It downloads the new version and
// plans what changes, which can be cancelled. Then it stops the server, backs it up unless
// asked not to, and writes and removes files; if the pack changes its Minecraft or loader
// version, the server gets the new one. A server that ran starts again.
func (s *Service) run(ctx context.Context, u update) (*Change, error) {
	srv := u.server
	conn, err := s.nodes.Conn(ctx, u.nodeID)
	var next *Pack
	if err == nil {
		next, err = s.Resolve(ctx, u.from.project, u.version)
	}
	if err == nil && next.Type != srv.GetType() {
		err = httpapi.Errorf(http.StatusConflict, "This version of the modpack needs another mod loader than the server runs.")
	}
	var p plan
	if err == nil {
		p, err = s.plan(ctx, conn, srv.GetId(), u.from.files, next)
	}
	var sel *noryxv1.BackupSelection
	if err == nil && u.backup != nil {
		sel, err = selection(u.backup.Selection, topLevel(u.from.files, next.files))
	}
	if err == nil {
		err = operation.Keep(ctx)
	}
	if err != nil {
		return nil, err
	}

	servers, files := noryxv1.NewServerServiceClient(conn), noryxv1.NewFileServiceClient(conn)
	running := srv.GetState() != noryxv1.ServerState_SERVER_STATE_STOPPED
	if running {
		operation.Step(ctx, "stop")
		if _, err := servers.StopServer(ctx, &noryxv1.StopServerRequest{Id: srv.GetId()}); err != nil {
			return nil, err
		}
	}
	change := &Change{Number: next.Number, Written: []string{}, Removed: p.remove, Kept: p.kept}
	for _, f := range p.write {
		change.Written = append(change.Written, f.path)
	}
	if sel != nil {
		change.Backup = label(next.Number)
		operation.Step(ctx, "archive")
		if err := backUp(ctx, conn, srv.GetId(), change.Backup, sel, u.backup.Location); err != nil {
			if running { // nothing changed yet
				if _, startErr := servers.StartServer(ctx, &noryxv1.StartServerRequest{Id: srv.GetId()}); startErr != nil {
					err = errors.Join(err, startErr) // only then, as a join of err alone hides the agent's message
				}
			}
			return nil, err
		}
	}
	operation.Step(ctx, "mods")
	err = s.apply(ctx, files, srv.GetId(), p)
	if err == nil && (u.from.minecraft != next.GameVersion || u.from.loader != next.LoaderVersion) {
		err = changeVersion(ctx, conn, srv, u.from, next)
	}
	if err == nil {
		err = s.remember(ctx, u.nodeID, srv.GetId(), next, p.files)
	}
	if err != nil {
		restore := "."
		if change.Backup != "" {
			restore = fmt.Sprintf("; the backup %q has it as it was.", change.Backup)
		}
		return nil, httpapi.Errorf(http.StatusBadGateway, "%s The server stays stopped, as it may be updated in part%s",
			strings.TrimSuffix(httpapi.Message(err), ".")+".", restore)
	}
	if running {
		operation.Step(ctx, "start")
		if _, err := servers.StartServer(ctx, &noryxv1.StartServerRequest{Id: srv.GetId()}); err != nil {
			change.Warning = "The server didn't start again: " + httpapi.Message(err)
		}
	}
	return change, nil
}

// label is the label of the backup before an update, of up to 64 characters like all labels.
func label(number string) string {
	l := []rune("Before modpack " + number)
	return string(l[:min(len(l), 64)])
}

// topLevel returns the files and folders at the top of a server's data that hold the files
// of two versions of a pack, so that its backup has all files an update changes.
func topLevel(old map[string]written, next []file) []string {
	var top []string
	for p := range old {
		top = append(top, strings.Split(p, "/")[0])
	}
	for _, f := range next {
		top = append(top, strings.Split(f.path, "/")[0])
	}
	slices.Sort(top)
	return slices.Compact(top)
}

// selection is what of a server an update backs up: what was chosen, plus the files and
// folders with the files of the pack, so that the backup has all files the update changes.
func selection(s backup.Selection, pack []string) (*noryxv1.BackupSelection, error) {
	paths := s.Paths
	if !s.Everything { // which has them
		paths = append(slices.Clone(s.Paths), pack...)
		slices.Sort(paths)
		paths = slices.Compact(paths)
	}
	if len(paths) > noryxv1.MaxBackupPaths {
		return nil, httpapi.Errorf(http.StatusBadRequest, "Back up everything, or fewer files and folders: with those of the modpack, they are more than %d.", noryxv1.MaxBackupPaths)
	}
	return &noryxv1.BackupSelection{Everything: s.Everything, Worlds: s.Worlds, Plugins: s.Plugins, Config: s.Config, Paths: paths, Exclude: s.Exclude}, nil
}

// backUp backs a server up as a backup made by hand, in a storage location of its node.
func backUp(ctx context.Context, conn grpc.ClientConnInterface, serverID, label string, sel *noryxv1.BackupSelection, location string) error {
	ctx, stop := operation.Agent(ctx, conn)
	defer stop()
	_, err := noryxv1.NewBackupServiceClient(conn).CreateBackup(ctx, &noryxv1.CreateBackupRequest{
		ServerId: serverID, Label: label, Selection: sel, Location: location,
	})
	return err
}

// changeVersion gives a server the Minecraft and loader version of the next version of its
// pack where it changes them, which creates its container again. Versions set by hand stay
// where the pack keeps its own.
func changeVersion(ctx context.Context, conn grpc.ClientConnInterface, srv *noryxv1.Server, from installed, next *Pack) error {
	version, loader := srv.GetVersion(), srv.GetLoaderVersion()
	if from.minecraft != next.GameVersion {
		version = next.GameVersion
	}
	if from.loader != next.LoaderVersion {
		loader = next.LoaderVersion
	}
	ctx, stop := operation.Agent(ctx, conn)
	defer stop()
	_, err := noryxv1.NewServerServiceClient(conn).UpdateServer(ctx, &noryxv1.UpdateServerRequest{
		Id: srv.GetId(), Name: srv.GetName(), Version: version, MemoryMb: srv.GetMemoryMb(), Port: srv.GetPort(),
		Java: srv.GetJava(), RestartPolicy: srv.GetRestartPolicy(), AikarFlags: srv.GetAikarFlags(), JvmOptions: srv.GetJvmOptions(),
		CpuMillis: srv.GetCpuMillis(), LoaderVersion: loader, StopTimeoutSeconds: srv.GetStopTimeoutSeconds(), TimeZone: srv.GetTimeZone(),
		ExtraPorts:         srv.GetExtraPorts(),
		HealthStartMinutes: srv.GetHealthStartMinutes(), CrashLimit: srv.GetCrashLimit(), StableAfterMinutes: srv.GetStableAfterMinutes(),
		UnhealthyRestartMinutes: srv.GetUnhealthyRestartMinutes(), ProcessLimit: srv.GetProcessLimit(), InitMemoryMb: srv.GetInitMemoryMb(),
		MaxPlayers: srv.GetMaxPlayers(),
	})
	return err
}

// apply writes and removes the files of a plan.
func (s *Service) apply(ctx context.Context, c noryxv1.FileServiceClient, serverID string, p plan) error {
	if err := s.write(ctx, c, serverID, p.write); err != nil {
		return err
	}
	for _, path := range p.remove {
		_, err := c.DeleteFile(ctx, &noryxv1.DeleteFileRequest{ServerId: serverID, Path: path})
		if status.Code(err) != codes.NotFound && err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	}
	return nil
}

// plan compares the files a server got from its pack with those of the next version and
// with what the server has now, which the agent hashes. Mods of the server that the pack
// didn't write are identified on Modrinth.
func (s *Service) plan(ctx context.Context, conn grpc.ClientConnInterface, serverID string, old map[string]written, next *Pack) (plan, error) {
	known := map[string]bool{}
	for path := range old {
		known[path] = true
	}
	for _, f := range next.files {
		known[f.path] = true
	}
	current, err := hashes(ctx, noryxv1.NewFileServiceClient(conn), serverID, slices.Sorted(maps.Keys(known)))
	if err != nil {
		return plan{}, err
	}
	res, err := noryxv1.NewPluginServiceClient(conn).ListPlugins(ctx, &noryxv1.ListPluginsRequest{ServerId: serverID})
	if err != nil {
		return plan{}, err
	}
	others := map[string][]string{} // paths of mods by their hash
	for _, f := range res.GetPlugins() {
		path := res.GetFolder() + "/" + f.GetFileName()
		if _, ok := known[path]; !ok && f.GetSha512() != "" {
			others[f.GetSha512()] = append(others[f.GetSha512()], path)
		}
	}
	byHand := map[string]string{}
	if len(others) > 0 {
		versions, err := s.modrinth.VersionsByHash(ctx, slices.Sorted(maps.Keys(others)))
		if err != nil {
			return plan{}, err
		}
		for hash, v := range versions {
			for _, path := range others[hash] {
				byHand[path] = v.ProjectID
			}
		}
	}
	return diff(old, next.files, current, byHand), nil
}

// hashes returns the SHA-512 hashes of files of a server by their paths, "" for paths that
// aren't regular files; missing files are left out.
func hashes(ctx context.Context, c noryxv1.FileServiceClient, serverID string, paths []string) (map[string]string, error) {
	all := map[string]string{}
	for chunk := range slices.Chunk(paths, noryxv1.MaxHashPaths) {
		res, err := c.HashFiles(ctx, &noryxv1.HashFilesRequest{ServerId: serverID, Paths: chunk})
		if status.Code(err) == codes.Unimplemented {
			return nil, errOlderAgent
		}
		if err != nil {
			return nil, err
		}
		maps.Copy(all, res.GetSha512())
	}
	return all, nil
}

// plan is what an update changes on a server, and the files of the pack it has afterwards.
type plan struct {
	write        []file
	remove, kept []string
	files        map[string]written
}

// diff plans an update from the files a server got from its pack (old), those of the next
// version, the hashes of what the server has now by path ("" for no regular file; gone ones
// are left out), and the projects of the mods it has besides. What the next version doesn't
// change stays as it is. Mods follow the pack, and replace other versions of their projects
// installed by hand, but a project whose mods the administrator removed stays removed. Other
// files are only written or removed if the server has them as the pack wrote them, or
// doesn't have them; otherwise they are kept.
func diff(old map[string]written, next []file, current, byHand map[string]string) plan {
	packed, present := map[string]bool{}, map[string]bool{} // projects the pack had and the server has mods of
	for path, o := range old {
		_, exists := current[path]
		packed[o.Project], present[o.Project] = true, present[o.Project] || exists
	}
	for _, f := range next {
		_, exists := current[f.path]
		present[f.project] = present[f.project] || exists
	}
	for _, project := range byHand {
		present[project] = true
	}

	p := plan{remove: []string{}, kept: []string{}, files: map[string]written{}}
	changed := map[string]bool{} // projects whose mods the next version changes
	for _, f := range next {
		o, had := old[f.path]
		c, exists := current[f.path]
		p.files[f.path] = written{f.sha512, f.project}
		if had && o.SHA512 == f.sha512 {
			continue
		}
		changed[f.project] = true
		switch {
		case exists && c == f.sha512: // the server has it already
		case f.project != "" && packed[f.project] && !present[f.project]: // the administrator removed it
			p.kept = append(p.kept, f.path)
		case !exists || c != "" && (f.project != "" || had && c == o.SHA512):
			p.write = append(p.write, f)
		default:
			p.kept = append(p.kept, f.path)
		}
	}
	for path, o := range old {
		if _, ok := p.files[path]; ok {
			continue
		}
		changed[o.Project] = true
		c, exists := current[path]
		switch {
		case !exists:
		case c != "" && (o.Project != "" || c == o.SHA512):
			p.remove = append(p.remove, path)
		default:
			p.kept = append(p.kept, path)
		}
	}
	delete(changed, "")
	for path, project := range byHand {
		if changed[project] {
			p.remove = append(p.remove, path)
		}
	}
	slices.SortFunc(p.write, func(a, b file) int { return cmp.Compare(a.path, b.path) })
	slices.Sort(p.remove)
	slices.Sort(p.kept)
	return p
}
