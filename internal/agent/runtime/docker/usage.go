package docker

import (
	"cmp"
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"github.com/QwikByte/noryx/internal/agent/runtime"
	"github.com/QwikByte/noryx/internal/logging"
)

func (d *Docker) Usage(ctx context.Context, id string) (runtime.Usage, error) {
	c, _, err := d.inspect(ctx, id)
	if err != nil {
		return runtime.Usage{}, err
	}
	if !c.State.Running {
		return runtime.Usage{}, runtime.ErrNotRunning
	}
	return d.usage(ctx, containerName(id))
}

func (d *Docker) DatastoreUsage(ctx context.Context, id string) (runtime.DatastoreUsage, error) {
	c, _, err := d.inspectDatastore(ctx, id)
	if err != nil {
		return runtime.DatastoreUsage{}, err
	}
	if !c.State.Running {
		return runtime.DatastoreUsage{}, runtime.ErrDatastoreNotRunning
	}
	u, err := d.usage(ctx, datastoreName(id))
	if err != nil {
		return runtime.DatastoreUsage{}, err
	}
	res := runtime.DatastoreUsage{Usage: u}
	if c.State.Health != nil && c.State.Health.Status == container.Healthy {
		n, err := d.connections(ctx, id)
		if err != nil {
			slog.DebugContext(ctx, "Can't count the connections of a datastore", logging.Databases, "datastore", id, "err", err)
		} else {
			res.Connections = &n
		}
	}
	return res, nil
}

// usage measures what a running container uses.
func (d *Docker) usage(ctx context.Context, name string) (runtime.Usage, error) {
	// A single sample returns right away; the caller computes rates from consecutive ones.
	res, err := d.cli.ContainerStats(ctx, name, client.ContainerStatsOptions{})
	if err != nil {
		return runtime.Usage{}, notFound(err)
	}
	defer res.Body.Close()
	var stats container.StatsResponse
	if err := json.NewDecoder(res.Body).Decode(&stats); err != nil {
		return runtime.Usage{}, err
	}
	mem := stats.MemoryStats
	// Like docker stats, without the page cache, which the kernel frees when it needs memory.
	cache := cmp.Or(mem.Stats["inactive_file"], mem.Stats["total_inactive_file"])
	u := runtime.Usage{
		CPUTime:     time.Duration(stats.CPUStats.CPUUsage.TotalUsage), //nolint:gosec // nanoseconds of CPU time fit easily
		MemoryBytes: mem.Usage - min(cache, mem.Usage),
		MemoryLimit: mem.Limit,
	}
	for _, n := range stats.Networks {
		u.NetRxBytes += n.RxBytes
		u.NetTxBytes += n.TxBytes
	}
	return u, nil
}
