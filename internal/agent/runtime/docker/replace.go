package docker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/client"

	"github.com/QwikByte/noryx/internal/logging"
)

// oldSuffix names the container of a server or datastore while it is being replaced.
const oldSuffix = "-old"

// replacement replaces the container of a server or datastore to change its settings: stop
// stops the current one, create makes the new one under its name and start starts that.
type replacement struct {
	name    string
	running bool
	stop    func(context.Context) error
	create  func(context.Context) error
	start   func(context.Context) error
}

// replace replaces a container; the data on the host stays. The old container is kept as
// <name>-old until the new one exists and, if the old one ran, runs, so that a failure leaves
// it as it was: a half-made replacement is removed, and the old one renamed back and started.
// It finishes also when ctx ends, e.g. with the timeout of the master's call while a server
// takes its stop timeout, as the master wouldn't learn where it ended otherwise.
func (d *Docker) replace(ctx context.Context, r replacement) error {
	if !d.claim(r.name) {
		return fmt.Errorf("the container %s is being replaced already", r.name)
	}
	defer d.replacing.Delete(r.name)
	ctx = context.WithoutCancel(ctx)
	if err := d.reconcile(ctx, r.name); err != nil {
		return err
	}
	if r.running {
		if err := r.stop(ctx); err != nil {
			return notFound(err)
		}
	}
	old := r.name + oldSuffix
	if _, err := d.cli.ContainerRename(ctx, r.name, client.ContainerRenameOptions{NewName: old}); err != nil {
		if r.running {
			_, startErr := d.cli.ContainerStart(ctx, r.name, client.ContainerStartOptions{})
			return errors.Join(notFound(err), startErr)
		}
		return notFound(err)
	}
	err := r.create(ctx)
	if err == nil && r.running {
		err = r.start(ctx)
	}
	if err != nil {
		return errors.Join(err, d.restore(ctx, r.name, r.running))
	}
	if _, err := d.cli.ContainerRemove(ctx, old, client.ContainerRemoveOptions{}); err != nil {
		// The new container is in place; reconcile removes the old one later.
		slog.WarnContext(ctx, "Can't remove the replaced container", logging.Servers, "container", old, "err", err)
	}
	return nil
}

// restore removes a half-made replacement and brings the old container back, as it was.
func (d *Docker) restore(ctx context.Context, name string, running bool) error {
	if err := d.forceRemove(ctx, name); err != nil && !cerrdefs.IsNotFound(err) {
		return err
	}
	if _, err := d.cli.ContainerRename(ctx, name+oldSuffix, client.ContainerRenameOptions{NewName: name}); err != nil {
		return err
	}
	if running {
		_, err := d.cli.ContainerStart(ctx, name, client.ContainerStartOptions{})
		return err
	}
	return nil
}

// reconcile tidies up after a replacement that the agent or the runtime didn't finish, as
// they stopped amid it: an old container without the new one is renamed back, as the server or
// datastore would be missing otherwise, and one next to the new one is removed, as it would be
// in the way of the next replacement.
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
	case err == nil:
		slog.InfoContext(ctx, "Remove a container left by a replacement", logging.Servers, "container", old)
		if err := d.forceRemove(ctx, old); err != nil && !cerrdefs.IsNotFound(err) {
			return err
		}
		return nil
	case cerrdefs.IsNotFound(err):
		slog.InfoContext(ctx, "Bring back a container that a replacement left", logging.Servers, "container", name)
		_, err = d.cli.ContainerRename(ctx, old, client.ContainerRenameOptions{NewName: name})
	}
	return err
}

// reconcileAll reconciles every container that a replacement left, except those being
// replaced right now, e.g. when the agent starts or the runtime is back.
func (d *Docker) reconcileAll(ctx context.Context) error {
	for _, label := range []string{managed, labelDatastore} {
		res, err := d.cli.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: make(client.Filters).Add("label", label)})
		if err != nil {
			return err
		}
		for _, c := range res.Items {
			for _, n := range c.Names {
				name, ok := strings.CutSuffix(strings.TrimPrefix(n, "/"), oldSuffix)
				if !ok || !d.claim(name) {
					continue
				}
				if err := d.reconcile(ctx, name); err != nil {
					slog.WarnContext(ctx, "Can't tidy up after a replacement", logging.Servers, "container", name, "err", err)
				}
				d.replacing.Delete(name)
			}
		}
	}
	return nil
}

// claim marks a container as being replaced or reconciled, unless it already is.
func (d *Docker) claim(name string) bool {
	_, busy := d.replacing.LoadOrStore(name, struct{}{})
	return !busy
}
