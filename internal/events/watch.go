package events

import (
	"context"
	"time"

	eventsv1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/events/v1"
)

// Watch tails the outbox from its current head and calls fn for every event
// committed from now on by any replica of the service, until ctx is
// cancelled. Errors are retried. It is used to keep per-process caches
// coherent across replicas: a replica's own writes invalidate its cache
// directly, and Watch delivers every other replica's writes within one poll
// interval.
func (o *Outbox) Watch(ctx context.Context, opts ServerOptions, fn func(*eventsv1.Event)) error {
	after := int64(-1)
	for {
		err := o.watchOnce(ctx, &after, opts, fn)
		if ctx.Err() != nil {
			return nil //nolint:nilerr // shutting down; err is only the cancellation
		}
		o.log.Warn("outbox watch failed; retrying", "stream", o.stream, "error", err)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(time.Second):
		}
	}
}

// watchOnce positions at the head on first use, then tails from *after,
// advancing it so a retry resumes without missing events.
func (o *Outbox) watchOnce(ctx context.Context, after *int64, opts ServerOptions, fn func(*eventsv1.Event)) error {
	if *after < 0 {
		head, err := o.Head(ctx)
		if err != nil {
			return err
		}
		*after = head
	}
	return o.Tail(ctx, *after, false, opts, func(ev *eventsv1.Event) error {
		fn(ev)
		*after = ev.Seq
		return nil
	}, nil)
}
