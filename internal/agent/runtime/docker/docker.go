// Package docker runs Minecraft servers as containers of Docker, or of Podman through its
// Docker-compatible API, based on the itzg/minecraft-server, itzg/mc-proxy and
// itzg/minecraft-bedrock-server images, which cover all common server types. Where Podman
// differs, podman.go makes up for it.
package docker

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/jsonstream"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/agent/datadir"
	"github.com/QwikByte/noryx/internal/agent/progress"
	"github.com/QwikByte/noryx/internal/agent/rcon"
	"github.com/QwikByte/noryx/internal/agent/runtime"
	"github.com/QwikByte/noryx/internal/agent/storage"
)

const (
	labelManaged = "io.noryx.managed"
	labelSpec    = "io.noryx.spec"
	// managed matches the containers of servers in filters, with the value, without which
	// Podman's filter of events matches nothing.
	managed = labelManaged + "=true"

	// Images are named with their registry, as Podman doesn't assume Docker Hub.
	serverImage  = "docker.io/itzg/minecraft-server"
	proxyImage   = "docker.io/itzg/mc-proxy"
	bedrockImage = "docker.io/itzg/minecraft-bedrock-server"

	maxLineBytes = 1 << 20
)

// image describes how a server type maps onto the itzg images.
type image struct {
	ref  string // without tag
	typ  string // value of the TYPE variable, empty for Bedrock servers
	port int    // TCP port inside the container
	data string // data directory inside the container
}

// asUser reports whether the image runs as imageUser from the start, without capabilities.
// Only the server image starts as root to hand its data to the server's user. The others read
// console commands from their standard input, as they have no RCON.
func (img image) asUser() bool { return img.ref == proxyImage || img.ref == bedrockImage }

// loaderVariables are the variables of the server image that select the version of a mod loader.
var loaderVariables = map[noryxv1.ServerType]string{
	noryxv1.ServerType_SERVER_TYPE_FABRIC:   "FABRIC_LOADER_VERSION",
	noryxv1.ServerType_SERVER_TYPE_QUILT:    "QUILT_LOADER_VERSION",
	noryxv1.ServerType_SERVER_TYPE_FORGE:    "FORGE_VERSION",
	noryxv1.ServerType_SERVER_TYPE_NEOFORGE: "NEOFORGE_VERSION",
}

// serverCapabilities are all the server image needs: it starts as root to hand the data
// directory to the server's user and switch to it.
var serverCapabilities = []string{"CHOWN", "SETUID", "SETGID"}

// The proxy image would download and configure as root in a directory it handed to its
// user before, which root without DAC_OVERRIDE can't write to. So proxies run as that
// user from the start, which owns their data, and need no capabilities. Bedrock servers run
// as the same user, which their image keeps rather than switching to the owner of the data.
const (
	imageUID  = 1000
	imageUser = "1000:1000"
)

var images = map[noryxv1.ServerType]image{
	noryxv1.ServerType_SERVER_TYPE_VANILLA:    {serverImage, "VANILLA", 25565, "/data"},
	noryxv1.ServerType_SERVER_TYPE_PAPER:      {serverImage, "PAPER", 25565, "/data"},
	noryxv1.ServerType_SERVER_TYPE_PURPUR:     {serverImage, "PURPUR", 25565, "/data"},
	noryxv1.ServerType_SERVER_TYPE_FOLIA:      {serverImage, "FOLIA", 25565, "/data"},
	noryxv1.ServerType_SERVER_TYPE_LEAF:       {serverImage, "LEAF", 25565, "/data"},
	noryxv1.ServerType_SERVER_TYPE_FABRIC:     {serverImage, "FABRIC", 25565, "/data"},
	noryxv1.ServerType_SERVER_TYPE_QUILT:      {serverImage, "QUILT", 25565, "/data"},
	noryxv1.ServerType_SERVER_TYPE_FORGE:      {serverImage, "FORGE", 25565, "/data"},
	noryxv1.ServerType_SERVER_TYPE_NEOFORGE:   {serverImage, "NEOFORGE", 25565, "/data"},
	noryxv1.ServerType_SERVER_TYPE_VELOCITY:   {proxyImage, "VELOCITY", 25565, "/server"},
	noryxv1.ServerType_SERVER_TYPE_BUNGEECORD: {proxyImage, "BUNGEECORD", 25577, "/server"},
	noryxv1.ServerType_SERVER_TYPE_WATERFALL:  {proxyImage, "WATERFALL", 25577, "/server"},
	noryxv1.ServerType_SERVER_TYPE_BEDROCK:    {bedrockImage, "", noryxv1.BedrockContainerPort, "/data"},
}

// Docker implements runtime.Runtime with Docker or Podman. Container labels are the only
// state: the agent itself stores nothing besides the server data directories.
type Docker struct {
	cli      *client.Client
	storage  *storage.Locations
	consoles *rcon.Consoles
	sizes    sync.Map // of the data of datastores by folder, as dataSize measured them
	podman   *podman  // nil for Docker
	// refused tells why the agent refuses the runtime that answered last; see check.
	refused atomic.Pointer[error]
	output  atomic.Pointer[outputLimit] // see LimitLogs
	// stops counts the stops asked for of each server, so that a restart of a server that hangs
	// doesn't start one that someone stopped meanwhile; see restartUnlessStopped.
	stops sync.Map
	// replacing holds the names of the containers being replaced; see replace.
	replacing sync.Map
	public    netip.Addr // see Options
}

// Options choose the runtime.
type Options struct {
	// Podman makes the agent run servers with Podman instead of Docker.
	Podman bool
	// Socket is the path of the runtime's Unix socket. Empty means Docker's of the DOCKER_HOST
	// variable or /var/run/docker.sock, or Podman's PodmanSocket.
	Socket string
	// PublicAddress is the IPv4 address at which players reach the node, which Bedrock servers
	// tell them, e.g. the public one of a node behind NAT. Invalid means the node's own on its
	// route to the internet.
	PublicAddress netip.Addr
}

// New returns the runtime; it connects with the first call, so it starts while the runtime
// doesn't run yet.
func New(locations *storage.Locations, opts Options) (*Docker, error) {
	d := &Docker{storage: locations, consoles: rcon.NewConsoles(), public: opts.PublicAddress}
	clientOpts := []client.Opt{client.FromEnv, client.WithResponseHook(d.check)}
	if opts.Podman {
		d.podman = &podman{}
		opts.Socket = cmp.Or(opts.Socket, PodmanSocket)
	}
	if opts.Socket != "" {
		clientOpts = append(clientOpts, client.WithHost("unix://"+opts.Socket))
	}
	var err error
	d.cli, err = client.New(clientOpts...)
	return d, err
}

func (d *Docker) Close() error { return d.cli.Close() }

func (d *Docker) Name() string {
	if d.podman != nil {
		return noryxv1.RuntimePodman
	}
	return noryxv1.RuntimeDocker
}

func (d *Docker) Info(ctx context.Context) (runtime.Info, error) {
	res, err := d.cli.Info(ctx, client.InfoOptions{})
	if err != nil {
		return runtime.Info{}, cmp.Or(d.refusal(), err)
	}
	i := res.Info
	//nolint:gosec // CPU count and memory size are never negative and fit easily
	return runtime.Info{Version: i.ServerVersion, OS: i.OperatingSystem, CPUs: uint32(i.NCPU), MemoryBytes: uint64(i.MemTotal)}, nil
}

func (d *Docker) List(ctx context.Context) ([]runtime.Server, error) {
	res, err := d.cli.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: make(client.Filters).Add("label", managed)})
	if err != nil {
		return nil, err
	}
	if err := d.withHealth(ctx, res.Items); err != nil {
		return nil, err
	}
	servers := make([]runtime.Server, 0, len(res.Items))
	for _, c := range res.Items {
		// The name check skips the old container while a server is being recreated.
		if spec, ok := specOf(c.Labels); ok && slices.Contains(c.Names, "/"+containerName(spec.ID)) {
			srv := runtime.Server{Spec: spec, State: state(c), Unhealthy: unhealthy(c)}
			if mayHaveCrashed(c) {
				d.addCrashes(ctx, c.ID, &srv)
			}
			servers = append(servers, srv)
		}
	}
	return servers, nil
}

// specOf reads the server spec stored in the labels of a container created by the agent.
func specOf(labels map[string]string) (runtime.Spec, bool) {
	var spec runtime.Spec
	return spec, json.Unmarshal([]byte(labels[labelSpec]), &spec) == nil
}

// state is the state of a server by its container. A server whose health check fails
// runs, but is unhealthy.
func state(c container.Summary) noryxv1.ServerState {
	switch {
	case c.State == container.StateRestarting: // after a crash, until Docker starts it again
		return noryxv1.ServerState_SERVER_STATE_CRASHING
	case c.State != container.StateRunning:
		return noryxv1.ServerState_SERVER_STATE_STOPPED
	case c.Health != nil && c.Health.Status == container.Starting:
		return noryxv1.ServerState_SERVER_STATE_STARTING
	default:
		return noryxv1.ServerState_SERVER_STATE_RUNNING
	}
}

// unhealthy reports whether a running server's health check fails, which the images of
// itzg run, e.g. as the server hangs.
func unhealthy(c container.Summary) bool {
	return c.State == container.StateRunning && c.Health != nil && c.Health.Status == container.Unhealthy
}

func (d *Docker) Create(ctx context.Context, spec runtime.Spec) error {
	if _, ok := images[spec.Type]; !ok {
		return fmt.Errorf("server type %s is not supported by the docker runtime", spec.Type)
	}
	path, err := d.dataPath(spec)
	if err != nil {
		return err
	}
	if err := d.pull(ctx, imageRef(spec)); err != nil {
		return err
	}
	progress.Step(ctx, "container", 0)
	if err := os.Mkdir(path, 0o750); err != nil {
		return err
	}
	if err := d.createContainer(ctx, spec, home(spec)); err != nil {
		return errors.Join(err, os.Remove(path)) // still empty
	}
	return nil
}

// dataPath returns the host directory with the data of a server.
func (d *Docker) dataPath(spec runtime.Spec) (string, error) {
	location, err := d.storage.Path(spec.Storage)
	return filepath.Join(location, spec.ID), err
}

// inspect returns a container created by the agent and the spec of its server.
func (d *Docker) inspect(ctx context.Context, id string) (container.InspectResponse, runtime.Spec, error) {
	res, err := d.cli.ContainerInspect(ctx, containerName(id), client.ContainerInspectOptions{})
	if err != nil {
		return container.InspectResponse{}, runtime.Spec{}, notFound(err)
	}
	spec, ok := specOf(res.Container.Config.Labels)
	if !ok || spec.ID != id {
		return container.InspectResponse{}, runtime.Spec{}, runtime.ErrNotFound
	}
	return res.Container, spec, nil
}

func (d *Docker) Data(ctx context.Context, id string) (*datadir.Dir, error) {
	_, spec, err := d.inspect(ctx, id)
	if err != nil {
		return nil, err
	}
	path, err := d.dataPath(spec)
	if err != nil {
		return nil, err
	}
	return datadir.Open(path)
}

// imageRef returns the image of a server. The tags of the server image select the
// Java version; latest has the newest.
func imageRef(spec runtime.Spec) string {
	img := images[spec.Type]
	if spec.Java != "" && img.ref == serverImage {
		return img.ref + ":java" + spec.Java
	}
	return img.ref + ":latest"
}

var restartPolicies = map[noryxv1.RestartPolicy]container.RestartPolicyMode{
	noryxv1.RestartPolicy_RESTART_POLICY_UNSPECIFIED: container.RestartPolicyUnlessStopped,
	noryxv1.RestartPolicy_RESTART_POLICY_ALWAYS:      container.RestartPolicyUnlessStopped,
	noryxv1.RestartPolicy_RESTART_POLICY_ON_CRASH:    container.RestartPolicyOnFailure,
	noryxv1.RestartPolicy_RESTART_POLICY_NEVER:       container.RestartPolicyDisabled,
}

// createContainer creates the container of a server whose image and data directory exist,
// in the network netName and the internal networks of datastores in also.
func (d *Docker) createContainer(ctx context.Context, spec runtime.Spec, netName string, also ...string) error {
	path, err := d.dataPath(spec)
	if err != nil {
		return err
	}
	if err := d.ensureNetwork(ctx, netName); err != nil {
		return err
	}
	opts, err := containerOptions(spec, path, netName)
	if err != nil {
		return err
	}
	if spec.Type.Bedrock() {
		opts.Config.Env = append(opts.Config.Env, "SERVER_UDP_PORTS="+udpPorts(spec, d.publicAddress()))
	}
	for _, name := range also {
		opts.NetworkingConfig.EndpointsConfig[name] = &network.EndpointSettings{}
	}
	opts.HostConfig.LogConfig = d.logConfig()
	d.adapt(&opts)
	_, err = d.cli.ContainerCreate(ctx, opts)
	return err
}

// javaEnv returns the variables of the server and proxy images for a server.
func javaEnv(spec runtime.Spec, img image) []string {
	env := []string{"TYPE=" + img.typ, "VERSION=" + spec.Version, fmt.Sprintf("MEMORY=%dM", spec.MemoryMB)}
	if !spec.Type.Proxy() { // as the operator accepted the EULA when creating the server
		env = append(env, "EULA=TRUE")
	}
	if spec.BehindProxy {
		env = append(env, "ONLINE_MODE=FALSE") // the proxy authenticates players
	}
	if spec.BedrockPlayers {
		env = append(env, "ENFORCE_SECURE_PROFILE=FALSE")
	}
	if spec.AikarFlags {
		env = append(env, "USE_AIKAR_FLAGS=TRUE")
	}
	if spec.InitMemoryMB != 0 { // MEMORY sets the largest heap then
		env = append(env, fmt.Sprintf("INIT_MEMORY=%dM", spec.InitMemoryMB))
	}
	if spec.LoaderVersion != "" {
		env = append(env, loaderVariables[spec.Type]+"="+spec.LoaderVersion)
	}
	if len(spec.JVMOptions) > 0 {
		env = append(env, "JVM_OPTS="+strings.Join(spec.JVMOptions, " "))
	}
	return env
}

// containerOptions describes the container of a server with the data at path, in the
// network netName.
func containerOptions(spec runtime.Spec, path, netName string) (client.ContainerCreateOptions, error) {
	img := images[spec.Type]
	specJSON, err := json.Marshal(spec)
	if err != nil {
		return client.ContainerCreateOptions{}, err
	}
	env := javaEnv(spec, img)
	if spec.Type.Bedrock() {
		// The image sets the properties of these variables on every start, and keeps its default
		// ports. Players join over NetherNet at the TCP port, the only way from Bedrock Dedicated
		// Server 1.26.60 on; the operator accepted the EULA when creating the server.
		env = []string{"VERSION=" + spec.Version, "EULA=TRUE", "TRANSPORT=nethernet", fmt.Sprintf("MAX_PLAYERS=%d", noryxv1.BedrockPlayers(spec.MaxPlayers))}
	}
	if spec.TimeZone != "" {
		env = append(env, "TZ="+spec.TimeZone)
	}
	port := network.MustParsePort(fmt.Sprintf("%d/tcp", img.port))
	exposed := network.PortSet{port: {}}
	published := network.PortMap{port: {{HostPort: strconv.Itoa(int(spec.Port))}}}
	if addr, err := netip.ParseAddr(spec.Overlay); err == nil {
		published[port][0].HostIP = addr // only the proxy's node reaches it, over the private network
	}
	if spec.BehindProxy && spec.ProxyOnNode {
		published = network.PortMap{} // only its proxy connects, over their network
	}
	if spec.BedrockPort != 0 && spec.Type.Proxy() {
		// Geyser listens at the same port inside the container, so that it tells it correctly.
		bedrock := network.MustParsePort(fmt.Sprintf("%d/udp", spec.BedrockPort))
		exposed[bedrock], published[bedrock] = struct{}{}, []network.PortBinding{{HostPort: strconv.Itoa(int(spec.BedrockPort))}}
	}
	for p := spec.Port; spec.Type.Bedrock() && p <= noryxv1.LastPort(spec.Type, spec.Port, spec.MaxPlayers); p++ {
		// Each player of a Bedrock server plays over a UDP port of their own, from the server's
		// port on, at the same port inside the container, which the server tells them.
		udp, err := network.ParsePort(fmt.Sprintf("%d/udp", p))
		if err != nil {
			return client.ContainerCreateOptions{}, err
		}
		exposed[udp], published[udp] = struct{}{}, []network.PortBinding{{HostPort: strconv.Itoa(int(p))}}
	}
	for _, p := range spec.ExtraPorts {
		// Plugins listen at the same port inside the container, which is public like the server's
		// own, also for a backend whose own port only its proxy reaches.
		extra, err := network.ParsePort(fmt.Sprintf("%d/%s", p.Port, p.Protocol))
		if err != nil {
			return client.ContainerCreateOptions{}, err
		}
		exposed[extra], published[extra] = struct{}{}, []network.PortBinding{{HostPort: strconv.Itoa(int(p.Port))}}
	}
	user, capAdd := "", serverCapabilities
	if img.asUser() {
		user, capAdd = imageUser, nil
	}
	return client.ContainerCreateOptions{
		Name: containerName(spec.ID),
		Config: &container.Config{
			Image:        imageRef(spec),
			User:         user,
			Env:          env,
			Labels:       map[string]string{labelManaged: "true", labelSpec: string(specJSON)},
			ExposedPorts: exposed,
			// Proxies and Bedrock servers read console commands from their standard input, as
			// they have no RCON.
			OpenStdin: img.asUser(),
			// Also when Docker stops the container by itself, e.g. as its daemon stops.
			StopTimeout: new(stopSeconds(spec)),
			Healthcheck: healthCheck(spec),
		},
		NetworkingConfig: &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{netName: {}}},
		HostConfig: &container.HostConfig{
			Binds:         []string{path + ":" + img.data},
			PortBindings:  published,
			RestartPolicy: container.RestartPolicy{Name: restartPolicies[spec.RestartPolicy]},
			SecurityOpt:   []string{"no-new-privileges:true"},
			CapDrop:       []string{"ALL"},
			CapAdd:        capAdd,
			Resources: container.Resources{
				Memory:    noryxv1.ContainerMemoryMB(spec.MemoryMB) << 20,
				NanoCPUs:  int64(spec.CPUMillis) * 1e6,
				PidsLimit: new(noryxv1.ProcessLimit(spec.ProcessLimit)),
			},
		},
	}, nil
}

func (d *Docker) Start(ctx context.Context, id string) error {
	if err := d.prepare(ctx, id); err != nil {
		return err
	}
	_, err := d.cli.ContainerStart(ctx, containerName(id), client.ContainerStartOptions{})
	return notFound(err)
}

// Stop gives the server the stop timeout its container carries to save its worlds.
func (d *Docker) Stop(ctx context.Context, id string) error {
	d.stopsOf(id).Add(1)
	return d.stop(ctx, id)
}

func (d *Docker) stop(ctx context.Context, id string) error {
	_, err := d.cli.ContainerStop(ctx, containerName(id), client.ContainerStopOptions{})
	return notFound(err)
}

// stopSeconds is the stop timeout of a server in seconds.
func stopSeconds(spec runtime.Spec) int {
	return int(noryxv1.StopTimeout(spec.StopTimeout) / time.Second)
}

func (d *Docker) Update(ctx context.Context, spec runtime.Spec) error {
	c, current, err := d.inspect(ctx, spec.ID)
	if err != nil {
		return err
	}
	spec.Type, spec.Storage, spec.BehindProxy, spec.ProxyOnNode = current.Type, current.Storage, current.BehindProxy, current.ProxyOnNode
	spec.BedrockPort, spec.BedrockPlayers, spec.Overlay = current.BedrockPort, current.BedrockPlayers, current.Overlay
	if imageRef(spec) != imageRef(current) {
		if err := d.pull(ctx, imageRef(spec)); err != nil {
			return err
		}
	}
	progress.Step(ctx, "container", 0)
	return d.recreate(ctx, c, spec, c.State.Running)
}

func (d *Docker) UpdateImage(ctx context.Context, id string) (bool, error) {
	c, spec, err := d.inspect(ctx, id)
	if err != nil {
		return false, err
	}
	if err := d.pull(ctx, imageRef(spec)); err != nil {
		return false, err
	}
	img, err := d.cli.ImageInspect(ctx, imageRef(spec))
	if err != nil || img.ID == c.Image {
		return false, err
	}
	progress.Step(ctx, "container", 0)
	if err := d.recreate(ctx, c, spec, c.State.Running); err != nil {
		return true, err
	}
	// The old image goes once no container uses it; Docker refuses to remove it before.
	_, _ = d.cli.ImageRemove(ctx, c.Image, client.ImageRemoveOptions{PruneChildren: true})
	return true, nil
}

// Restart stops a server gracefully and starts it again, prepared like any start.
func (d *Docker) Restart(ctx context.Context, id string) error {
	if err := d.Stop(ctx, id); err != nil {
		return err
	}
	return d.Start(ctx, id)
}

// restartUnlessStopped restarts a server like Restart, but leaves it stopped if someone asked
// for its stop meanwhile, e.g. an operator who stops a server that hangs.
func (d *Docker) restartUnlessStopped(ctx context.Context, id string) error {
	stops := d.stopsOf(id)
	asked := stops.Load()
	if err := d.stop(ctx, id); err != nil || stops.Load() != asked {
		return err
	}
	return d.Start(ctx, id)
}

// stopsOf counts the stops asked for of a server.
func (d *Docker) stopsOf(id string) *atomic.Int64 {
	n, _ := d.stops.LoadOrStore(id, new(atomic.Int64))
	return n.(*atomic.Int64)
}

// Remove deletes the container and all server data.
func (d *Docker) Remove(ctx context.Context, id string) error {
	_, spec, err := d.inspect(ctx, id)
	if err != nil {
		return err
	}
	if _, err := d.dataPath(spec); err != nil { // e.g. a removed storage location; nothing is deleted yet
		return err
	}
	if err := d.forceRemove(ctx, containerName(id)); err != nil {
		if cerrdefs.IsConflict(err) {
			return runtime.ErrNotFound // another request is removing it, which removes its data too
		}
		return notFound(err)
	}
	if spec.Type.Proxy() {
		if _, err := d.cli.NetworkRemove(ctx, proxyNetwork(id), client.NetworkRemoveOptions{}); err != nil && !cerrdefs.IsNotFound(err) {
			return err
		}
	}
	return d.removeData(spec)
}

// removeData deletes the data directory of a server.
func (d *Docker) removeData(spec runtime.Spec) error {
	location, err := d.storage.Path(spec.Storage)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(location)
	if err != nil {
		return err
	}
	return errors.Join(root.RemoveAll(spec.ID), root.Close())
}

func (d *Docker) Logs(ctx context.Context, id string, tail int, after time.Time) iter.Seq2[runtime.LogLine, error] {
	return d.logs(ctx, containerName(id), tail, after)
}

// logs yields the last tail lines of the output of a container written after the time after,
// if it isn't zero, then follows it until the container stops or ctx is cancelled.
func (d *Docker) logs(ctx context.Context, name string, tail int, after time.Time) iter.Seq2[runtime.LogLine, error] {
	return func(yield func(runtime.LogLine, error) bool) {
		opts := client.ContainerLogsOptions{ShowStdout: true, ShowStderr: true, Follow: true, Timestamps: true, Tail: strconv.Itoa(tail)}
		if !after.IsZero() {
			opts.Since = after.Format(time.RFC3339Nano) // includes the line written at that time
		}
		logs, err := d.cli.ContainerLogs(ctx, name, opts)
		if err != nil {
			yield(runtime.LogLine{}, notFound(err))
			return
		}
		defer logs.Close()
		// Docker multiplexes stdout and stderr into one stream; both end up in the console.
		r, w := io.Pipe()
		defer r.Close()
		go func() {
			_, err := stdcopy.StdCopy(w, w, logs)
			w.CloseWithError(err)
		}()
		stopped := false
		err = eachLine(r, func(text string) bool {
			line := logLine(text)
			stopped = (after.IsZero() || newer(line.Time, after)) && !yield(line, nil)
			return !stopped
		})
		if err != nil && !stopped && ctx.Err() == nil {
			yield(runtime.LogLine{}, err)
		}
	}
}

// newer reports whether a line written at t comes after the time after. Podman before 5.8
// tells the time of lines in whole seconds, so lines of the same second count as later: a
// console that resumes may show a line again, rather than miss one.
func newer(t, after time.Time) bool {
	if t.Nanosecond() == 0 {
		return !t.Before(after.Truncate(time.Second))
	}
	return t.After(after)
}

// eachLine calls fn with each line of r until it returns false, with lines cut after
// maxLineBytes: a server can write longer ones, which would otherwise end every stream of
// its log at the same line.
func eachLine(r io.Reader, fn func(string) bool) error {
	br := bufio.NewReaderSize(r, 64<<10)
	var line []byte
	for {
		chunk, more, err := br.ReadLine()
		if errors.Is(err, io.EOF) {
			return nil
		} else if err != nil {
			return err
		}
		line = append(line, chunk[:min(len(chunk), maxLineBytes-len(line))]...)
		if more {
			continue
		}
		if !fn(string(line)) {
			return nil
		}
		line = line[:0]
	}
}

// logLine splits off the time Docker writes in front of every line.
func logLine(text string) runtime.LogLine {
	stamp, rest, _ := strings.Cut(text, " ")
	t, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil {
		return runtime.LogLine{Text: text}
	}
	return runtime.LogLine{Time: t, Text: rest}
}

// pull pulls an image, and reports how much of its layers it downloaded.
func (d *Docker) pull(ctx context.Context, ref string) error {
	progress.Step(ctx, "image", 0)
	res, err := d.cli.ImagePull(ctx, ref, client.ImagePullOptions{})
	if err != nil {
		return fmt.Errorf("pull %s: %w", ref, err)
	}
	layers := map[string]*jsonstream.Progress{}
	for msg, err := range res.JSONMessages(ctx) {
		switch {
		case err != nil:
			return fmt.Errorf("pull %s: %w", ref, err)
		case msg.Error != nil:
			return fmt.Errorf("pull %s: %s", ref, msg.Error.Message)
		case msg.Status == "Downloading" && msg.Progress != nil:
			layers[msg.ID] = msg.Progress
		case msg.Status == "Download complete" && layers[msg.ID] != nil:
			layers[msg.ID].Current = layers[msg.ID].Total
		default:
			continue
		}
		var done, total int64
		for _, l := range layers {
			done, total = done+l.Current, total+l.Total
		}
		progress.Set(ctx, done, total)
	}
	return nil
}

func containerName(id string) string { return "noryx-" + id }

func notFound(err error) error {
	if cerrdefs.IsNotFound(err) {
		return runtime.ErrNotFound
	}
	return err
}
