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
	pubfunc PubFunc

	update           chan cid.Cid
	immediatePublish chan chan struct{}

	cancel  func()
	once    sync.Once
	stopped chan struct{}
}

// NewRepublisher creates a new Republisher object to republish the given root
// using the short and long time intervals.
func NewRepublisher(ctx context.Context, pf PubFunc, tshort, tlong time.Duration, lastPublished cid.Cid) *Republisher {
	ctx, cancel := context.WithCancel(ctx)
	rp := &Republisher{
		update:           make(chan cid.Cid, 1),
		pubfunc:          pf,
		immediatePublish: make(chan chan struct{}),
		cancel:           cancel,
		stopped:          make(chan struct{}),
	}

	go rp.run(ctx, tshort, tlong, lastPublished)

	return rp
}

// WaitPub waits for the current value to be published, or returns early if it
// already has.
func (rp *Republisher) WaitPub(ctx context.Context) error {
	wait := make(chan struct{})
	select {
	case rp.immediatePublish <- wait:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-wait:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close tells the republisher to stop and waits for it to stop.  If it is
// necessary to wait for any current values to be published, then call WaitPub
// prior to calling Close.
func (rp *Republisher) Close(ctx context.Context) error {
	rp.once.Do(func() {
		_ = rp.WaitPub(ctx)
		// Cancel the run goroutine
		rp.cancel()
	})
	// Wait for run goroutine to exit
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-rp.stopped:
	}
	return nil
}

// Update supplies a new cid value to publish. The value will be published
// after a delay but each consecutive call to Update may extend this delay up
// to long timeout.
func (rp *Republisher) Update(c cid.Cid) {
	select {
	case <-rp.update:
		select {
		case rp.update <- c:
		default:
			// Don't try again. If this case is hit, there is a concurrent
			// publish and it is safe to let that concurrent publish win.
		}
	case rp.update <- c:
	}
}

// run contains the core logic of the `Republisher`. It calls the user-defined
// `pubfunc` function whenever Update supplies a new cid value.  Since calling
// // the `pubfunc` may be slow, updates are batched
//
// Algorithm:
//   1. When receiving the first update after publishing, set a `longer` timer
//   2. When receiving any update, reset the `quick` timer
//   3. If either the `quick` timeout or the `longer` timeout elapses, call
//      `publish` with the latest updated value.
//
// The `longer` timer ensures that publishing is delayed by at most that
// duration. The `quick` timer allows publishing sooner if there are no more
// updates available.
//
// Note: If a publish fails, retry repeatedly every long timer interval.
func (rp *Republisher) run(ctx context.Context, timeoutShort, timeoutLong time.Duration, lastPublished cid.Cid) {
	defer close(rp.stopped)
	quick := time.NewTimer(0)
	if !quick.Stop() {
		<-quick.C
	}
	longer := time.NewTimer(0)
	if !longer.Stop() {
		<-longer.C
	}

	var toPublish cid.Cid
	var waiter chan struct{}

	for ctx.Err() == nil {
		select {
		case <-ctx.Done():
			return
		case newValue := <-rp.update:
			// Skip already published values.
			if lastPublished.Equals(newValue) {
				// Break to the end of the switch to cleanup any timers.
				toPublish = cid.Undef
				break
			}

			// If not already waiting to publish, reset the long timeout.
			if !toPublish.Defined() {
				longer.Reset(timeoutLong)
			}

			// Always reset the short timeout.
			quick.Reset(timeoutShort)

			// Finally, set the new value to publish.
			toPublish = newValue
			continue
		case waiter = <-rp.immediatePublish:
			// Make sure to grab the *latest* value to publish.
			select {
			case toPublish = <-rp.update:
			default:
			}

			// Avoid publishing duplicate values
			if lastPublished.Equals(toPublish) {
				toPublish = cid.Undef
			}
		case <-quick.C:
		case <-longer.C:
		}

		// Cleanup, publish, and close waiters.

		// 1. Stop any timers. Do not use the `if !t.Stop() { ... }`
		//    idiom as these timers may not be running.

		quick.Stop()
		select {
		case <-quick.C:
		default:
		}

		longer.Stop()
		select {
		case <-longer.C:
		default:
		}

		// 2. If there is a value to publish, publish it now.
		if toPublish.Defined() {
			err := rp.pubfunc(ctx, toPublish)
			if err != nil {
				// Keep retrying until publish succeeds, or run is stopped.
				// Publish newer values if available.
				longer.Reset(timeoutLong)
				continue
			}
			lastPublished = toPublish
			toPublish = cid.Undef
		}

		// 3. Trigger anything waiting in `WaitPub`.
		if waiter != nil {
			close(waiter)
			waiter = nil
		}
	}
}
