package docker

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/client"

	"github.com/QwikByte/noryx/internal/logging"
)

// oldSuffix names the container that replace keeps until its replacement runs.
const oldSuffix = "-old"

// replace replaces the container name by the one that create makes, e.g. with other settings of
// its server or datastore, whose data on the host stays. The old container is stopped if it runs,
// renamed to name-old and kept until the new one exists and, if the old one ran, runs; any
// failure until then removes the new one and brings the old one back as it was. As a half-done
// replacement would leave the server stopped or without its container, it goes on when ctx ends,
// e.g. with the timeout of the master's call, and reconcile finishes one that an agent that
// stopped meanwhile left.
func (d *Docker) replace(ctx context.Context, name string, running bool, stop, create, start func(context.Context) error) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	ctx = context.WithoutCancel(ctx)
	if err := d.reconcile(ctx, name); err != nil {
		return err
	}
	if running {
		if err := stop(ctx); err != nil {
			return err
		}
	}
	old := name + oldSuffix
	if _, err := d.cli.ContainerRename(ctx, name, client.ContainerRenameOptions{NewName: old}); err != nil {
		return err
	}
	defer func() {
		if err == nil {
			return
		}
		removeErr := d.forceRemove(ctx, name)
		if cerrdefs.IsNotFound(removeErr) {
			removeErr = nil
		}
		_, renameErr := d.cli.ContainerRename(ctx, old, client.ContainerRenameOptions{NewName: name})
		if renameErr == nil && running {
			renameErr = start(ctx)
		}
		err = errors.Join(err, removeErr, renameErr)
	}()
	if err := create(ctx); err != nil {
		return err
	}
	if running {
		if err := start(ctx); err != nil {
			return err
		}
	}
	// The replacement is done; reconcile removes an old container that stays.
	if _, err := d.cli.ContainerRemove(ctx, old, client.ContainerRemoveOptions{}); err != nil {
		slog.Warn("Can't remove the container that another one replaced", category(name), "container", old, "err", err)
	}
	return nil
}

// reconcile finishes the replacement of the container name that an agent left when it stopped:
// the old container gets its name back if no new one was made, and goes if one was.
func (d *Docker) reconcile(ctx context.Context, name string) error {
	old := name + oldSuffix
	if _, err := d.cli.ContainerInspect(ctx, old, client.ContainerInspectOptions{}); err != nil {
		if cerrdefs.IsNotFound(err) {
			return nil
		}
		return err
	}
	_, err := d.cli.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
	switch {
	case cerrdefs.IsNotFound(err):
		slog.Warn("Bring back a container whose replacement the agent didn't finish", category(name), "container", name)
		_, err = d.cli.ContainerRename(ctx, old, client.ContainerRenameOptions{NewName: name})
	case err == nil:
		slog.Warn("Remove a container that another one replaced", category(name), "container", old)
		err = d.forceRemove(ctx, old)
	}
	return err
}

// reconcileAll reconciles every container of the agent whose replacement didn't finish.
func (d *Docker) reconcileAll(ctx context.Context) error {
	res, err := d.cli.ContainerList(ctx, client.ContainerListOptions{All: true})
	if err != nil {
		return err
	}
	for _, c := range res.Items {
		for _, n := range c.Names {
			if name, ok := strings.CutSuffix(strings.TrimPrefix(n, "/"), oldSuffix); ok && strings.HasPrefix(name, "noryx-") {
				err = errors.Join(err, d.reconcile(ctx, name))
			}
		}
	}
	return err
}

// category is the category of the log of a container: datastores or servers.
func category(name string) slog.Attr {
	if strings.HasPrefix(name, datastorePrefix) {
		return logging.Databases
	}
	return logging.Servers
}
