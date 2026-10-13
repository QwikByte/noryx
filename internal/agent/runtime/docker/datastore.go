package docker

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"iter"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/agent/datadir"
	"github.com/QwikByte/noryx/internal/agent/progress"
	"github.com/QwikByte/noryx/internal/agent/runtime"
	"github.com/QwikByte/noryx/internal/logging"
)

// A datastore runs in the container noryx-db-<id>, which only its own internal network of
// the same name reaches, which the servers of its network on the node join. Its files are in
// <location>/datastores/<id>: the data of each major version in data-<version>, owned by the
// image's user, and the superuser's password in superuser, which the image reads when it
// initializes new data.
const (
	labelDatastore     = "io.noryx.datastore"
	datastorePrefix    = "noryx-db-"
	datastoresFolder   = "datastores"
	superuserFile      = "superuser"
	superuserMount     = "/run/noryx/superuser"
	datastoreUID       = 999 // the user of both images
	datastoreUser      = "999:999"
	datastoreStop      = 120 // seconds; the postgres image warns that Docker's 10 are too few
	datastorePids      = 512
	datastoreReadyWait = 5 * time.Minute
	sizeInterval       = time.Minute
)

// engine describes how an engine runs in its official image.
type engine struct {
	image    string
	versions []string // major versions, oldest first
	port     int
	// data returns where a version keeps its data in the container.
	data   func(version string) string
	env    []string
	health []string
	// command returns the command of its server with the parameters as options.
	command func(connections, bufferMB uint32) []string
}

var engines = map[noryxv1.DatastoreEngine]engine{
	noryxv1.DatastoreEngine_DATASTORE_ENGINE_MARIADB: {
		image: "docker.io/library/mariadb", versions: []string{"11.8", "12.3"}, port: 3306,
		data: func(string) string { return "/var/lib/mysql" },
		// Root may only sign in on the container itself, and the system tables follow upgrades
		// of the image.
		env:    []string{"MARIADB_ROOT_PASSWORD_FILE=" + superuserMount, "MARIADB_ROOT_HOST=localhost", "MARIADB_AUTO_UPGRADE=1"},
		health: []string{"CMD", "healthcheck.sh", "--connect", "--innodb_initialized"},
		command: func(connections, bufferMB uint32) []string {
			return []string{"mariadbd", fmt.Sprintf("--max-connections=%d", connections), fmt.Sprintf("--innodb-buffer-pool-size=%dM", bufferMB)}
		},
	},
	noryxv1.DatastoreEngine_DATASTORE_ENGINE_POSTGRES: {
		image: "docker.io/library/postgres", versions: []string{"17", "18"}, port: 5432,
		// From 18 on, the image keeps the data of each major version in a folder of its own
		// under /var/lib/postgresql, which is its volume.
		data: func(version string) string {
			if major, _ := strconv.Atoi(version); major >= 18 {
				return "/var/lib/postgresql"
			}
			return "/var/lib/postgresql/data"
		},
		env: []string{"POSTGRES_PASSWORD_FILE=" + superuserMount},
		// Over TCP: the server that initializes new data listens on the Unix socket only, and
		// stops right after.
		health: []string{"CMD", "pg_isready", "-h", "127.0.0.1", "-U", "postgres"},
		command: func(connections, bufferMB uint32) []string {
			return []string{"postgres", "-c", fmt.Sprintf("max_connections=%d", connections), "-c", fmt.Sprintf("shared_buffers=%dMB", bufferMB)}
		},
	},
}

func (d *Docker) DatastoreVersions() map[noryxv1.DatastoreEngine][]string {
	versions := map[noryxv1.DatastoreEngine][]string{}
	for e, eng := range engines {
		versions[e] = slices.Clone(eng.versions)
	}
	return versions
}

func datastoreName(id string) string { return datastorePrefix + id }

// pullOrLocal pulls an image, or takes the one the node has if the registry can't be
// reached, e.g. as it limits the pulls of the node.
func (d *Docker) pullOrLocal(ctx context.Context, ref string) error {
	err := d.pull(ctx, ref)
	if err != nil && ctx.Err() == nil {
		if _, inspectErr := d.cli.ImageInspect(ctx, ref); inspectErr == nil {
			slog.WarnContext(ctx, "Use the image the node has, as it can't be pulled", logging.Databases, "image", ref, "err", err)
			return nil
		}
	}
	return err
}

// datastoreDir returns the folder of the files of a datastore.
func (d *Docker) datastoreDir(spec runtime.DatastoreSpec) (string, error) {
	location, err := d.storage.Path(spec.Storage)
	return filepath.Join(location, datastoresFolder, spec.ID), err
}

func dataFolder(version string) string { return "data-" + version }

func (d *Docker) ListDatastores(ctx context.Context) ([]runtime.Datastore, error) {
	res, err := d.cli.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: make(client.Filters).Add("label", labelDatastore)})
	if err != nil {
		return nil, err
	}
	var list []runtime.Datastore
	for _, c := range res.Items {
		spec, ok := datastoreSpecOf(c.Labels)
		// The name check skips the old container while a datastore is being recreated.
		if !ok || !slices.Contains(c.Names, "/"+datastoreName(spec.ID)) {
			continue
		}
		ds := runtime.Datastore{DatastoreSpec: spec, State: datastoreState(c.State, c.Health)}
		if dir, err := d.datastoreDir(spec); err == nil {
			ds.Size = d.dataSize(filepath.Join(dir, dataFolder(spec.Version)))
		}
		list = append(list, ds)
	}
	slices.SortFunc(list, func(a, b runtime.Datastore) int { return strings.Compare(a.ID, b.ID) })
	return list, nil
}

// sized is the size of a folder and when it was measured.
type sized struct {
	bytes int64
	at    time.Time
}

// dataSize returns the size of a folder of data, measured at most every sizeInterval, as
// measuring reads the details of all its files and the datastores are listed often.
func (d *Docker) dataSize(folder string) int64 {
	if s, ok := d.sizes.Load(folder); ok && time.Since(s.(sized).at) < sizeInterval {
		return s.(sized).bytes
	}
	size := datadir.Size(os.DirFS(filepath.Dir(folder)), filepath.Base(folder))
	d.sizes.Store(folder, sized{size, time.Now()})
	return size
}

func datastoreSpecOf(labels map[string]string) (runtime.DatastoreSpec, bool) {
	var spec runtime.DatastoreSpec
	return spec, json.Unmarshal([]byte(labels[labelDatastore]), &spec) == nil && runtime.ValidID(spec.ID)
}

func datastoreState(state container.ContainerState, health *container.HealthSummary) noryxv1.DatastoreState {
	switch {
	case state != container.StateRunning:
		return noryxv1.DatastoreState_DATASTORE_STATE_STOPPED
	case health == nil || health.Status == container.Starting:
		return noryxv1.DatastoreState_DATASTORE_STATE_STARTING
	case health.Status == container.Unhealthy:
		return noryxv1.DatastoreState_DATASTORE_STATE_UNHEALTHY
	}
	return noryxv1.DatastoreState_DATASTORE_STATE_RUNNING
}

// inspectDatastore returns the container of a datastore and its spec.
func (d *Docker) inspectDatastore(ctx context.Context, id string) (container.InspectResponse, runtime.DatastoreSpec, error) {
	res, err := d.cli.ContainerInspect(ctx, datastoreName(id), client.ContainerInspectOptions{})
	if err != nil {
		return container.InspectResponse{}, runtime.DatastoreSpec{}, notFound(err)
	}
	spec, ok := datastoreSpecOf(res.Container.Config.Labels)
	if !ok || spec.ID != id {
		return container.InspectResponse{}, runtime.DatastoreSpec{}, runtime.ErrNotFound
	}
	return res.Container, spec, nil
}

func (d *Docker) CreateDatastore(ctx context.Context, spec runtime.DatastoreSpec) (err error) {
	eng, ok := engines[spec.Engine]
	if !ok || !slices.Contains(eng.versions, spec.Version) {
		return fmt.Errorf("%s %s isn't supported", spec.Engine, spec.Version)
	}
	dir, err := d.datastoreDir(spec)
	if err != nil {
		return err
	}
	if err := d.pullOrLocal(ctx, eng.image+":"+spec.Version); err != nil {
		return err
	}
	progress.Step(ctx, "container", 0)
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return err
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, os.RemoveAll(dir))
		}
	}()
	if err := newData(dir, spec.Version); err != nil {
		return err
	}
	password := strings.ToLower(rand.Text() + rand.Text())
	if err := os.WriteFile(filepath.Join(dir, superuserFile), []byte(password), 0o400); err != nil {
		return err
	}
	if err := os.Lchown(filepath.Join(dir, superuserFile), datastoreUID, datastoreUID); err != nil && !errors.Is(err, fs.ErrPermission) {
		return err
	}
	return d.createDatastoreContainer(ctx, spec, dir)
}

// newData creates the empty data folder of a version, owned by the image's user.
func newData(dir, version string) error {
	path := filepath.Join(dir, dataFolder(version))
	if err := os.Mkdir(path, 0o700); err != nil {
		return err
	}
	if err := os.Lchown(path, datastoreUID, datastoreUID); err != nil && !errors.Is(err, fs.ErrPermission) {
		return err // without root, e.g. in tests, the folder stays the agent's
	}
	return nil
}

func (d *Docker) createDatastoreContainer(ctx context.Context, spec runtime.DatastoreSpec, dir string) error {
	opts, err := datastoreOptions(spec, dir)
	if err != nil {
		return err
	}
	tune(&opts, spec)
	for name := range opts.NetworkingConfig.EndpointsConfig {
		if err := d.ensureNetwork(ctx, name); err != nil {
			return err
		}
	}
	opts.HostConfig.LogConfig = d.logConfig()
	d.adapt(&opts)
	_, err = d.cli.ContainerCreate(ctx, opts)
	return err
}

// portNetwork is the network in which a datastore publishes its port for other nodes, as
// internal networks can't publish ports. No other container joins it.
func portNetwork(id string) string { return datastoreName(id) + "-port" }

// internalNetwork reports whether a network of the agent is the internal network of a
// datastore, which has no route to the internet.
func internalNetwork(name string) bool {
	return strings.HasPrefix(name, datastorePrefix) && !strings.HasSuffix(name, "-port")
}

// datastoreOptions describes the container of a datastore with its files in dir: it runs as
// the image's user without capabilities, in its internal network, which the servers that
// use it on the node join, and, for other nodes, in its port network with its port published
// at the node's address in the private network of the nodes.
func datastoreOptions(spec runtime.DatastoreSpec, dir string) (client.ContainerCreateOptions, error) {
	eng := engines[spec.Engine]
	specJSON, err := json.Marshal(spec)
	if err != nil {
		return client.ContainerCreateOptions{}, err
	}
	name := datastoreName(spec.ID)
	networks := map[string]*network.EndpointSettings{name: {}}
	port := network.MustParsePort(fmt.Sprintf("%d/tcp", eng.port))
	var published network.PortMap
	if addr, err := netip.ParseAddr(spec.Overlay); err == nil && spec.Port != 0 {
		networks[portNetwork(spec.ID)] = &network.EndpointSettings{}
		published = network.PortMap{port: {{HostIP: addr, HostPort: strconv.Itoa(int(spec.Port))}}}
	}
	return client.ContainerCreateOptions{
		Name: name,
		Config: &container.Config{
			Image:        eng.image + ":" + spec.Version,
			User:         datastoreUser,
			Env:          eng.env,
			Labels:       map[string]string{labelDatastore: string(specJSON)},
			ExposedPorts: network.PortSet{port: {}},
			Healthcheck: &container.HealthConfig{
				Test: eng.health, Interval: 10 * time.Second, Timeout: 5 * time.Second, Retries: 3,
				StartPeriod: 5 * time.Minute, StartInterval: 2 * time.Second,
			},
			StopTimeout: new(datastoreStop),
		},
		NetworkingConfig: &network.NetworkingConfig{EndpointsConfig: networks},
		HostConfig: &container.HostConfig{
			Binds: []string{
				filepath.Join(dir, dataFolder(spec.Version)) + ":" + eng.data(spec.Version),
				filepath.Join(dir, superuserFile) + ":" + superuserMount + ":ro",
			},
			PortBindings:  published,
			RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyUnlessStopped},
			SecurityOpt:   []string{"no-new-privileges:true"},
			CapDrop:       []string{"ALL"},
			Resources: container.Resources{
				Memory:    int64(spec.MemoryMB) << 20,
				NanoCPUs:  int64(spec.CPUMillis) * 1e6,
				PidsLimit: new(int64(datastorePids)),
			},
		},
	}, nil
}

// tune runs the engine of a datastore with its parameters, or their defaults, as options that
// it builds from the numbers, and gives it room for them: a process or thread for each
// connection besides those of the engine itself, and for PostgreSQL a quarter of the memory in
// /dev/shm, which parallel queries share, instead of Docker's 64 MB. Its shared_buffers are
// anonymous shared memory, which doesn't need room there; both count against the memory limit.
func tune(opts *client.ContainerCreateOptions, spec runtime.DatastoreSpec) {
	connections := cmp.Or(spec.MaxConnections, spec.Engine.DefaultConnections())
	opts.Config.Cmd = engines[spec.Engine].command(connections, cmp.Or(spec.BufferMB, spec.Engine.DefaultBufferMB(spec.MemoryMB)))
	opts.HostConfig.PidsLimit = new(int64(datastorePids + connections))
	if spec.Engine == noryxv1.DatastoreEngine_DATASTORE_ENGINE_POSTGRES {
		opts.HostConfig.ShmSize = int64(max(spec.MemoryMB/4, 64)) << 20
	}
}

func (d *Docker) StartDatastore(ctx context.Context, id string) error {
	c, _, err := d.inspectDatastore(ctx, id)
	if err != nil {
		return err
	}
	if err := d.checkNetworks(ctx, c); err != nil {
		return err
	}
	_, err = d.cli.ContainerStart(ctx, datastoreName(id), client.ContainerStartOptions{})
	return notFound(err)
}

func (d *Docker) StopDatastore(ctx context.Context, id string) error {
	if _, _, err := d.inspectDatastore(ctx, id); err != nil {
		return err
	}
	_, err := d.cli.ContainerStop(ctx, datastoreName(id), client.ContainerStopOptions{Timeout: new(datastoreStop)})
	return notFound(err)
}

func (d *Docker) UpdateDatastore(ctx context.Context, spec runtime.DatastoreSpec, pull bool) error {
	c, current, err := d.inspectDatastore(ctx, spec.ID)
	if err != nil {
		return err
	}
	spec.Engine, spec.Storage = current.Engine, current.Storage
	dir, err := d.datastoreDir(current)
	if err != nil {
		return err
	}
	if spec.Version != current.Version {
		return d.upgrade(ctx, c, current, spec, dir)
	}
	if pull {
		if err := d.pull(ctx, engines[spec.Engine].image+":"+spec.Version); err != nil {
			return err
		}
	}
	if current.Previous != "" && spec.Previous == "" {
		if err := os.RemoveAll(filepath.Join(dir, dataFolder(current.Previous))); err != nil {
			return err
		}
	}
	spec.Previous = current.Previous
	if spec.Previous != "" && !exists(filepath.Join(dir, dataFolder(spec.Previous))) {
		spec.Previous = ""
	}
	progress.Step(ctx, "container", 0)
	return d.recreateDatastore(ctx, spec, dir, c.State.Running)
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// recreateDatastore replaces the container of a datastore, see replace.
func (d *Docker) recreateDatastore(ctx context.Context, spec runtime.DatastoreSpec, dir string, running bool) error {
	name := datastoreName(spec.ID)
	err := d.replace(ctx, name, running,
		func(ctx context.Context) error {
			_, err := d.cli.ContainerStop(ctx, name, client.ContainerStopOptions{Timeout: new(datastoreStop)})
			return err
		},
		func(ctx context.Context) error { return d.createDatastoreContainer(ctx, spec, dir) },
		func(ctx context.Context) error {
			_, err := d.cli.ContainerStart(ctx, name, client.ContainerStartOptions{})
			return err
		})
	if err != nil {
		return err
	}
	if spec.Port == 0 {
		if err := d.removeNetwork(ctx, portNetwork(spec.ID)); err != nil { // unused now, only clutter
			slog.WarnContext(ctx, "Can't remove the port network of a datastore", logging.Databases, "datastore", spec.ID, "err", err)
		}
	}
	return nil
}

func (d *Docker) RemoveDatastore(ctx context.Context, id string) error {
	_, spec, err := d.inspectDatastore(ctx, id)
	if err != nil {
		return err
	}
	dir, err := d.datastoreDir(spec)
	if err != nil {
		return err
	}
	// An old container that a replacement left would come back once the agent starts again.
	if err := d.reconcile(ctx, datastoreName(id)); err != nil {
		return err
	}
	if err := d.forceRemove(ctx, datastoreName(id)); err != nil {
		return notFound(err)
	}
	for _, name := range []string{datastoreName(id), portNetwork(id)} {
		if err := d.removeNetwork(ctx, name); err != nil {
			return err
		}
	}
	return os.RemoveAll(dir)
}

// removeNetwork takes the containers off a network of the agent and removes it.
func (d *Docker) removeNetwork(ctx context.Context, name string) error {
	res, err := d.cli.NetworkInspect(ctx, name, client.NetworkInspectOptions{})
	if cerrdefs.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for containerID := range res.Network.Containers {
		if _, err := d.cli.NetworkDisconnect(ctx, name, client.NetworkDisconnectOptions{Container: containerID, Force: true}); err != nil && !cerrdefs.IsNotFound(err) {
			return err
		}
	}
	_, err = d.cli.NetworkRemove(ctx, name, client.NetworkRemoveOptions{})
	if cerrdefs.IsNotFound(err) {
		return nil
	}
	return err
}

func (d *Docker) DatastoreLogs(ctx context.Context, id string, tail int, after time.Time) iter.Seq2[runtime.LogLine, error] {
	return d.logs(ctx, datastoreName(id), tail, after)
}

// waitReady waits until a datastore passes its health check.
func (d *Docker) waitReady(ctx context.Context, id string) error {
	ctx, cancel := context.WithTimeout(ctx, datastoreReadyWait)
	defer cancel()
	for {
		c, _, err := d.inspectDatastore(ctx, id)
		switch {
		case err != nil:
			return err
		case !c.State.Running:
			return fmt.Errorf("%w: it stopped, its log tells why", runtime.ErrDatastoreNotRunning)
		case c.State.Health != nil && c.State.Health.Status == container.Healthy:
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w: it isn't ready after %s", runtime.ErrDatastoreNotRunning, datastoreReadyWait)
		case <-time.After(time.Second):
		}
	}
}

// ready returns the spec of a datastore that runs and passes its health check.
func (d *Docker) ready(ctx context.Context, id string) (runtime.DatastoreSpec, string, error) {
	c, spec, err := d.inspectDatastore(ctx, id)
	if err != nil {
		return spec, "", err
	}
	if !c.State.Running || c.State.Health == nil || c.State.Health.Status != container.Healthy {
		return spec, "", runtime.ErrDatastoreNotRunning
	}
	dir, err := d.datastoreDir(spec)
	return spec, dir, err
}
