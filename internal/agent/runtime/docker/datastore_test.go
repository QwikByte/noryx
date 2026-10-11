package docker

import (
	"bytes"
	"context"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/agent/runtime"
)

func TestDatastoreOptions(t *testing.T) {
	spec := runtime.DatastoreSpec{ID: "store", Engine: noryxv1.DatastoreEngine_DATASTORE_ENGINE_POSTGRES, Version: "18", MemoryMB: 512}
	opts, err := datastoreOptions(spec, "/srv/store")
	if err != nil {
		t.Fatal(err)
	}
	hc := opts.HostConfig
	if opts.Config.User != datastoreUser || !slices.Equal(hc.CapDrop, []string{"ALL"}) || len(hc.CapAdd) > 0 || hc.Privileged {
		t.Errorf("runs as %q with %v, %v", opts.Config.User, hc.CapDrop, hc.CapAdd)
	}
	if want := []string{"/srv/store/data-18:/var/lib/postgresql", "/srv/store/superuser:/run/noryx/superuser:ro"}; !slices.Equal(hc.Binds, want) {
		t.Errorf("mounts %v, want %v", hc.Binds, want)
	}
	if len(hc.PortBindings) > 0 || len(opts.NetworkingConfig.EndpointsConfig) != 1 || opts.NetworkingConfig.EndpointsConfig["noryx-db-store"] == nil {
		t.Errorf("unpublished, it publishes %v in %v", hc.PortBindings, opts.NetworkingConfig.EndpointsConfig)
	}

	spec.Port, spec.Overlay = 25600, "10.213.0.3"
	opts, _ = datastoreOptions(spec, "/srv/store")
	bindings := opts.HostConfig.PortBindings[network.MustParsePort("5432/tcp")]
	if len(opts.HostConfig.PortBindings) != 1 || len(bindings) != 1 || bindings[0].HostIP.String() != "10.213.0.3" || bindings[0].HostPort != "25600" {
		t.Errorf("publishes %v", opts.HostConfig.PortBindings)
	}
	if opts.NetworkingConfig.EndpointsConfig["noryx-db-store-port"] == nil {
		t.Errorf("isn't in its port network: %v", opts.NetworkingConfig.EndpointsConfig)
	}
	if !internalNetwork("noryx-db-store") || internalNetwork("noryx-db-store-port") || internalNetwork(sharedNetwork) {
		t.Error("internalNetwork tells the networks apart wrongly")
	}
}

// TestTune checks the options of the engines, by default with buffers that follow the memory,
// and the room their connections and PostgreSQL's parallel queries get.
func TestTune(t *testing.T) {
	mariadb, postgres := noryxv1.DatastoreEngine_DATASTORE_ENGINE_MARIADB, noryxv1.DatastoreEngine_DATASTORE_ENGINE_POSTGRES
	for _, c := range []struct {
		spec runtime.DatastoreSpec
		cmd  []string
		pids int64
		shm  int64
	}{
		{runtime.DatastoreSpec{Engine: mariadb, Version: "12.3", MemoryMB: 256}, []string{"mariadbd", "--max-connections=151", "--innodb-buffer-pool-size=128M"}, 663, 0},
		{runtime.DatastoreSpec{Engine: mariadb, Version: "12.3", MemoryMB: 2048}, []string{"mariadbd", "--max-connections=151", "--innodb-buffer-pool-size=1024M"}, 663, 0},
		{
			runtime.DatastoreSpec{Engine: mariadb, Version: "12.3", MemoryMB: 2048, MaxConnections: 800, BufferMB: 1536},
			[]string{"mariadbd", "--max-connections=800", "--innodb-buffer-pool-size=1536M"}, 1312, 0,
		},
		{runtime.DatastoreSpec{Engine: postgres, Version: "18", MemoryMB: 256}, []string{"postgres", "-c", "max_connections=100", "-c", "shared_buffers=128MB"}, 612, 64 << 20},
		{runtime.DatastoreSpec{Engine: postgres, Version: "18", MemoryMB: 2048}, []string{"postgres", "-c", "max_connections=100", "-c", "shared_buffers=512MB"}, 612, 512 << 20},
		{
			runtime.DatastoreSpec{Engine: postgres, Version: "17", MemoryMB: 1024, MaxConnections: 500, BufferMB: 32},
			[]string{"postgres", "-c", "max_connections=500", "-c", "shared_buffers=32MB"}, 1012, 256 << 20,
		},
	} {
		c.spec.ID = "store"
		opts, err := datastoreOptions(c.spec, "/srv/store")
		if err != nil {
			t.Fatal(err)
		}
		tune(&opts, c.spec)
		if hc := opts.HostConfig; !slices.Equal(opts.Config.Cmd, c.cmd) || *hc.PidsLimit != c.pids || hc.ShmSize != c.shm {
			t.Errorf("%+v: runs %q with %d processes and %d bytes of /dev/shm", c.spec, opts.Config.Cmd, *hc.PidsLimit, hc.ShmSize)
		}
	}
}

func TestEveryDatastoreEngineHasADialect(t *testing.T) {
	for e := range noryxv1.DatastoreEngine_name {
		engine := noryxv1.DatastoreEngine(e)
		if _, ok := engines[engine]; ok != (engine != noryxv1.DatastoreEngine_DATASTORE_ENGINE_UNSPECIFIED) {
			t.Errorf("%s: engine %v", engine, ok)
		}
		if _, ok := dialects[engine]; ok != (engine != noryxv1.DatastoreEngine_DATASTORE_ENGINE_UNSPECIFIED) {
			t.Errorf("%s: dialect %v", engine, ok)
		}
	}
}

// TestDatastoresLive runs datastores of both engines in Docker: databases, dumps, loads
// and an upgrade, after which the data and the passwords are there still, and a container
// that joins the internal network of a datastore reaches it by name. It needs Docker, root
// and the images, so it only runs with NORYX_DOCKER_TEST set.
func TestDatastoresLive(t *testing.T) {
	d := live(t)
	for _, engine := range []noryxv1.DatastoreEngine{noryxv1.DatastoreEngine_DATASTORE_ENGINE_MARIADB, noryxv1.DatastoreEngine_DATASTORE_ENGINE_POSTGRES} {
		t.Run(engine.Slug(), func(t *testing.T) { liveDatastore(t, d, engine) })
	}
}

func liveDatastore(t *testing.T, d *Docker, engine noryxv1.DatastoreEngine) {
	ctx := t.Context()
	versions := engines[engine].versions
	spec := runtime.DatastoreSpec{ID: runtime.NewID(), Engine: engine, Version: versions[0], MemoryMB: 512, MaxConnections: 120, BufferMB: 192}
	must(t, d.CreateDatastore(ctx, spec))
	t.Cleanup(func() { must(t, d.RemoveDatastore(context.WithoutCancel(ctx), spec.ID)) })
	must(t, d.StartDatastore(ctx, spec.ID))
	must(t, d.waitReady(ctx, spec.ID))
	tuned(t, d, spec.ID)

	const password = "abcdefghijklmnopqrstuvwxyz234567"
	must(t, d.EnsureDatabase(ctx, spec.ID, "shop", password))
	must(t, d.EnsureDatabase(ctx, spec.ID, "shop", password)) // again, as the master does
	asUser(t, d, spec.ID, password, "CREATE TABLE items (v INT); INSERT INTO items VALUES (42);")
	if names, err := d.Databases(ctx, spec.ID); err != nil || !slices.Equal(names, []string{"shop"}) {
		t.Fatalf("databases %v, %v", names, err)
	}
	if err := d.EnsureDatabase(ctx, spec.ID, "shop; DROP", password); err == nil {
		t.Error("ensured a database with an invalid name")
	}
	if err := d.DropDatabase(ctx, spec.ID, "mysql"); err == nil {
		t.Error("dropped a database of the engine")
	}
	// A user has rights on its own database only, also with _ in its name, which MariaDB's
	// grants read as any character.
	must(t, d.EnsureDatabase(ctx, spec.ID, "a_b", password))
	must(t, d.EnsureDatabase(ctx, spec.ID, "axb", password))
	if _, err := as(t, d, spec.ID, "a_b", "axb", password, "CREATE TABLE stolen (v INT);"); err == nil {
		t.Error("a_b created a table in axb")
	}
	must(t, d.DropDatabase(ctx, spec.ID, "a_b"))
	must(t, d.DropDatabase(ctx, spec.ID, "axb"))
	// Errors hold no secrets, as clients repeat what failed.
	err := d.exec(ctx, spec.ID, []string{"sh", "-c", "echo the password is s3cr3t >&2; exit 1"}, nil, nil, nil, "s3cr3t")
	if err == nil || strings.Contains(err.Error(), "s3cr3t") || !strings.Contains(err.Error(), "<hidden>") {
		t.Errorf("error = %v", err)
	}

	var dump bytes.Buffer
	must(t, d.Dump(ctx, spec.ID, "shop", &dump))
	asUser(t, d, spec.ID, password, "INSERT INTO items VALUES (7);")
	// A dump from elsewhere can't use the commands of the client itself, e.g. to run programs.
	for _, sql := range []string{"\\! touch /tmp/escaped\n", "SELECT 1; \\! touch /tmp/escaped\n", "system touch /tmp/escaped\n", "\\unrestrict x\n\\! touch /tmp/escaped\n"} {
		if err := d.Load(ctx, spec.ID, "shop", strings.NewReader(sql)); err == nil {
			t.Errorf("loaded %q", sql)
		}
	}
	if d.exec(ctx, spec.ID, []string{"test", "-e", "/tmp/escaped"}, nil, nil, nil) == nil {
		t.Error("a dump ran a program")
	}
	must(t, d.Load(ctx, spec.ID, "shop", &dump))
	if got := asUser(t, d, spec.ID, password, "SELECT SUM(v) FROM items;"); got != "42" {
		t.Errorf("after the load, the sum is %q", got)
	}

	server := joinedContainer(t, d, spec.ID)
	spec.Version = versions[1]
	must(t, d.UpdateDatastore(ctx, spec, false))
	list, err := d.ListDatastores(ctx)
	if err != nil || len(list) != 1 || list[0].Version != versions[1] || list[0].Previous != versions[0] {
		t.Fatalf("after the upgrade: %+v, %v", list, err)
	}
	must(t, d.waitReady(ctx, spec.ID))
	if got := asUser(t, d, spec.ID, password, "SELECT SUM(v) FROM items;"); got != "42" {
		t.Errorf("after the upgrade, the sum is %q", got)
	}
	tuned(t, d, spec.ID)
	reaches(t, d, server, spec.ID)
	if engine == noryxv1.DatastoreEngine_DATASTORE_ENGINE_POSTGRES {
		s, err := d.session(ctx, spec.ID)
		must(t, err)
		if got, err := s.query(ctx, "SELECT has_database_privilege('shop', 'postgres', 'CONNECT')"); err != nil || !slices.Equal(got, []string{"f"}) {
			t.Errorf("after the upgrade, shop may connect to postgres: %v, %v", got, err)
		}
	}

	spec.Previous = ""
	must(t, d.UpdateDatastore(ctx, spec, false))
	if _, err := os.Stat(d.mustDir(t, spec) + "/" + dataFolder(versions[0])); !os.IsNotExist(err) {
		t.Errorf("the data of the previous version stayed: %v", err)
	}
	must(t, d.waitReady(ctx, spec.ID))
	must(t, d.DropDatabase(ctx, spec.ID, "shop"))
	if names, _ := d.Databases(ctx, spec.ID); len(names) > 0 {
		t.Errorf("databases %v after dropping", names)
	}
}

// tuned checks that the engine of a datastore runs with 120 connections and 192 MiB of buffers.
func tuned(t *testing.T, d *Docker, id string) {
	s, err := d.session(t.Context(), id)
	must(t, err)
	settings := map[string]string{"SELECT @@max_connections;": "120", "SELECT @@innodb_buffer_pool_size DIV 1048576;": "192"}
	if s.engine == noryxv1.DatastoreEngine_DATASTORE_ENGINE_POSTGRES {
		settings = map[string]string{"SHOW max_connections;": "120", "SHOW shared_buffers;": "192MB"}
	}
	for query, want := range settings {
		if got, err := s.query(t.Context(), query); err != nil || !slices.Equal(got, []string{want}) {
			t.Errorf("%s = %v, %v; want %s", query, got, err, want)
		}
	}
}

func (d *Docker) mustDir(t *testing.T, spec runtime.DatastoreSpec) string {
	dir, err := d.datastoreDir(spec)
	must(t, err)
	return dir
}

// asUser runs SQL as the user of the database shop over TCP, so with its password, and
// returns what it prints.
func asUser(t *testing.T, d *Docker, id, password, sql string) string {
	t.Helper()
	out, err := as(t, d, id, "shop", "shop", password, sql)
	must(t, err)
	return out
}

// as runs SQL as a user on a database over TCP, so with its password.
func as(t *testing.T, d *Docker, id, user, database, password, sql string) (string, error) {
	c, spec, err := d.inspectDatastore(t.Context(), id)
	must(t, err)
	cmd, env := []string{"mariadb", "-N", "-B", "-h127.0.0.1", "-u" + user, database}, []string{"MYSQL_PWD=" + password}
	if spec.Engine == noryxv1.DatastoreEngine_DATASTORE_ENGINE_POSTGRES {
		// The image trusts connections from the container itself, but not from its address.
		ip := c.NetworkSettings.Networks[datastoreName(id)].IPAddress.String()
		cmd, env = []string{"psql", "-X", "-q", "-A", "-t", "-v", "ON_ERROR_STOP=1", "-h", ip, "-U", user, "-d", database}, []string{"PGPASSWORD=" + password}
	}
	var out bytes.Buffer
	err = d.exec(t.Context(), id, cmd, env, strings.NewReader(sql), &out)
	return strings.TrimSpace(out.String()), err
}

// joinedContainer creates a container in the shared network that joins the internal network
// of a datastore.
func joinedContainer(t *testing.T, d *Docker, id string) container.InspectResponse {
	ctx := t.Context()
	c, spec, err := d.inspectDatastore(ctx, id)
	must(t, err)
	must(t, d.ensureNetwork(ctx, sharedNetwork))
	created, err := d.cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config:     &container.Config{Image: c.Config.Image, User: datastoreUser, Entrypoint: []string{"sleep", "600"}},
		HostConfig: &container.HostConfig{NetworkMode: sharedNetwork},
		Name:       "noryx-test-" + spec.ID,
	})
	must(t, err)
	t.Cleanup(func() {
		_, _ = d.cli.ContainerRemove(context.WithoutCancel(ctx), created.ID, client.ContainerRemoveOptions{Force: true})
	})
	_, err = d.cli.ContainerStart(ctx, created.ID, client.ContainerStartOptions{})
	must(t, err)
	res, err := d.cli.ContainerInspect(ctx, created.ID, client.ContainerInspectOptions{})
	must(t, err)
	joined, err := d.attach(ctx, res.Container, []string{id})
	must(t, err)
	if nets := datastoreNetworks(joined); !slices.Equal(nets, []string{datastoreName(id)}) {
		t.Fatalf("joined %v", nets)
	}
	reaches(t, d, joined, id)
	return joined
}

// reaches checks that a container resolves the name of a datastore's container.
func reaches(t *testing.T, d *Docker, c container.InspectResponse, id string) {
	if err := d.run(t.Context(), c.ID, "", []string{"getent", "hosts", datastoreName(id)}, nil, nil, nil); err != nil {
		t.Errorf("%s doesn't resolve: %v", datastoreName(id), err)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// TestUpgradeRollsBackLive upgrades a datastore whose dump its user may not load, a view of
// the superuser, after which it runs the old version on the old data again.
func TestUpgradeRollsBackLive(t *testing.T) {
	d := live(t)
	ctx := t.Context()
	spec := runtime.DatastoreSpec{ID: runtime.NewID(), Engine: noryxv1.DatastoreEngine_DATASTORE_ENGINE_MARIADB, Version: "11.8", MemoryMB: 512}
	must(t, d.CreateDatastore(ctx, spec))
	t.Cleanup(func() { must(t, d.RemoveDatastore(context.WithoutCancel(ctx), spec.ID)) })
	must(t, d.StartDatastore(ctx, spec.ID))
	must(t, d.waitReady(ctx, spec.ID))
	const password = "abcdefghijklmnopqrstuvwxyz234567"
	must(t, d.EnsureDatabase(ctx, spec.ID, "shop", password))
	asUser(t, d, spec.ID, password, "CREATE TABLE items (v INT); INSERT INTO items VALUES (42);")
	s, err := d.session(ctx, spec.ID)
	must(t, err)
	_, err = s.query(ctx, "CREATE DEFINER = 'root'@'localhost' VIEW shop.all_items AS SELECT v FROM shop.items;")
	must(t, err)

	spec.Version = "12.3"
	if err := d.UpdateDatastore(ctx, spec, false); err == nil {
		t.Fatal("the upgrade loaded a view of the superuser as the user")
	}
	list, err := d.ListDatastores(ctx)
	must(t, err)
	if len(list) != 1 || list[0].Version != "11.8" || list[0].Previous != "" {
		t.Fatalf("after the failed upgrade: %+v", list)
	}
	if _, err := os.Stat(d.mustDir(t, list[0].DatastoreSpec) + "/" + dataFolder("12.3")); !os.IsNotExist(err) {
		t.Errorf("the new data stayed: %v", err)
	}
	must(t, d.waitReady(ctx, spec.ID))
	if got := asUser(t, d, spec.ID, password, "SELECT SUM(v) FROM items;"); got != "42" {
		t.Errorf("after the rollback, the sum is %q", got)
	}
}
