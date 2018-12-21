package mfs

import (
	"context"
	"sync"
	"time"

	cid "github.com/ipfs/go-cid"
)

// PubFunc is the user-defined function that determines exactly what
// logic entails "publishing" a `Cid` value.
type PubFunc func(context.Context, cid.Cid) error

// Republisher manages when to publish a given entry.
type Republisher struct {
	TimeoutLong         time.Duration
	TimeoutShort        time.Duration
	valueHasBeenUpdated chan struct{}
	pubfunc             PubFunc

	valueLock          sync.Mutex
	valueToPublish     *cid.Cid

	ctx    context.Context
	cancel func()
}

// NewRepublisher creates a new Republisher object to republish the given root
// using the given short and long time intervals.
func NewRepublisher(ctx context.Context, pf PubFunc, tshort, tlong time.Duration) *Republisher {
	ctx, cancel := context.WithCancel(ctx)
	return &Republisher{
		TimeoutShort:        tshort,
		TimeoutLong:         tlong,
		valueHasBeenUpdated: make(chan struct{}, 1),
		pubfunc:             pf,
		ctx:                 ctx,
		cancel:              cancel,
	}
}

func (rp *Republisher) Close() error {
	err := rp.PublishNow()
	rp.cancel()
	return err
}

// Update the `valueToPublish` and signal it in the `valueHasBeenUpdated`
// channel. Multiple consecutive updates may extend the time period before
// the next publish occurs in order to more efficiently batch updates.
func (rp *Republisher) Update(c cid.Cid) {
	rp.valueLock.Lock()
	rp.valueToPublish = &c
	rp.valueLock.Unlock()

	select {
	case rp.valueHasBeenUpdated <- struct{}{}:
	default:
	}
}

// Run contains the core logic of the `Republisher`. It calls the user-defined
// `pubfunc` function whenever the `Cid` value is updated. The complexity comes
// from the fact that `pubfunc` may be slow so we need to batch updates.
// Algorithm:
//   1. When we receive the first update after publishing, we set a `longer` timer.
//   2. When we receive any update, we reset the `quick` timer.
//   3. If either the `quick` timeout or the `longer` timeout elapses,
//      we call `publish` with the latest updated value.
//
// The `longer` timer ensures that we delay publishing by at most
// `TimeoutLong`. The `quick` timer allows us to publish sooner if
// it looks like there are no more updates coming down the pipe.
func (rp *Republisher) Run() {
	for {
		select {
		case <-rp.ctx.Done():
			return
		case <-rp.valueHasBeenUpdated:
			// Fast timeout, a `publish` will be issued if there are
			// no more updates before it expires (restarted every time
			// the `valueHasBeenUpdated` is signaled).
			quick := time.After(rp.TimeoutShort)
			// Long timeout that guarantees a `publish` after it expires
			// even if the value keeps being updated (and `quick` is
			// restarted).
			longer := time.After(rp.TimeoutLong)

		wait:
			select {
			case <-rp.ctx.Done():
				return
			case <-rp.valueHasBeenUpdated:
				// The `valueToPublish` has been updated *again* since
				// the last time we checked and we still haven't published
				// it, restart the `quick` timer allowing for some more
				// time to see if the `valueToPublish` changes again.
				quick = time.After(rp.TimeoutShort)
				goto wait

			case <-quick:
			case <-longer:
			}

			err := rp.PublishNow()
			if err != nil {
				log.Errorf("republishRoot error: %s", err)
			}
		}
	}
}

// Wrapper function around the user-defined `pubfunc`. It publishes
// the (last) `valueToPublish` set and registers it in `lastValuePublished`.
// TODO: Allow passing a value to `PublishNow` which supersedes the
// internal `valueToPublish`.
func (rp *Republisher) PublishNow() error {

	rp.valueLock.Lock()
	extractedValue := rp.valueToPublish
	rp.valueLock.Unlock()

	if extractedValue == nil {
		return nil
		// If this value is `nil` it means we've done a publish
		// since the last `Update`.
	}

	err := rp.pubfunc(rp.ctx, *extractedValue);
	if err != nil {
		return err
	}

	rp.valueLock.Lock()
	rp.valueToPublish = nil
	rp.valueLock.Unlock()

	return nil
}
