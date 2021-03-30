package mfs

import (
	"context"
	"errors"
	"testing"
	"time"

	cid "github.com/ipfs/go-cid"
	ci "github.com/libp2p/go-libp2p-testing/ci"
)

func TestRepublisher(t *testing.T) {
	if ci.IsRunning() {
		t.Skip("dont run timing tests in CI")
	}

	pub := make(chan struct{})

	pf := func(ctx context.Context, c cid.Cid) error {
		select {
		case pub <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
		return nil
	}

	testCid1, _ := cid.Parse("QmeomffUNfmQy76CQGy9NdmqEnnHU9soCexBnGU3ezPHVH")
	testCid2, _ := cid.Parse("QmeomffUNfmQy76CQGy9NdmqEnnHU9soCexBnGU3ezPHVX")

	tshort := time.Millisecond * 50
	tlong := time.Second / 2

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	rp := NewRepublisher(ctx, pf, tshort, tlong, cid.Undef)

	rp.Update(testCid1)

	// should hit short timeout
	select {
	case <-time.After(tshort * 2):
		t.Fatal("publish didnt happen in time")
	case <-pub:
	}

	stopUpdates := make(chan struct{})
	go func() {
		for {
			rp.Update(testCid2)
			time.Sleep(time.Millisecond * 10)
			select {
			case <-stopUpdates:
				return
			default:
			}
		}
	}()

	select {
	case <-pub:
		t.Fatal("shouldnt have received publish yet!")
	case <-time.After((tlong * 9) / 10):
	}
	select {
	case <-pub:
	case <-time.After(tlong / 2):
		t.Fatal("waited too long for pub!")
	}

	close(stopUpdates)

	// Check that republishing update does not call pubfunc again
	rp.Update(testCid2)
	err := rp.WaitPub(ctx)
	if err != nil {
		t.Error("got error trying to republish update:", err)
	}
	select {
	case <-pub:
		t.Fatal("pub func called again with repeated update")
	case <-time.After(tlong / 2):
	}

	ctx3, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()

	// Check that waitpub times out when blocked pubfunc is called
	rp.Update(testCid1)
	err = rp.WaitPub(ctx3)
	if err != context.DeadlineExceeded {
		t.Errorf("expected %q, got %v", context.DeadlineExceeded, err)
	}

	// Continue clearing the pub channel until stopPub signaled.  Signal stopped
	// chan when publishing becomes blocked due to pub chan not being cleared.
	stopPub := make(chan struct{})
	go func() {
		for {
			select {
			case <-pub:
			case <-stopPub:
				return
			}
		}
	}()

	// Check that calling Close returns
	done := make(chan struct{})
	go func() {
		err = rp.Close(ctx)
		close(done)
	}()
	select {
	case <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Error("repub.Close did not finish")
	}
	close(stopPub)

	// Check that republisher is stopped
	select {
	case <-rp.stopped:
	default:
		t.Fatal("did not stop republisher after Close")
	}

	// Check that additional call to Close is OK after republisher stopped
	done = make(chan struct{})
	go func() {
		err = rp.Close(ctx)
		close(done)
	}()
	select {
	case <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Error("repub.Close did not finish")
	}
}

func TestPubRetry(t *testing.T) {
	pubFail := make(chan cid.Cid, 1)
	gate := make(chan struct{})
	pf := func(ctx context.Context, c cid.Cid) error {
		select {
		case <-gate:
			return nil
		default:
		}
		pubFail <- c
		return errors.New("some failure")
	}

	tshort := time.Millisecond * 50
	tlong := time.Millisecond * 500

	testCid1, _ := cid.Parse("QmeomffUNfmQy76CQGy9NdmqEnnHU9soCexBnGU3ezPHVH")
	testCid2, _ := cid.Parse("QmeomffUNfmQy76CQGy9NdmqEnnHU9soCexBnGU3ezPHVX")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	rp := NewRepublisher(ctx, pf, tshort, tlong, cid.Undef)

	rp.Update(testCid1)

	waitDone := make(chan struct{})
	go func() {
		rp.WaitPub(context.Background())
		waitDone <- struct{}{}
	}()

	go func() {
		rp.WaitPub(context.Background())
		waitDone <- struct{}{}
	}()

	c := <-pubFail
	if c != testCid1 {
		t.Fatal("expected", testCid1)
	}
	// Check that pubfunc is called with new value after failure
	rp.Update(testCid2)
	c = <-pubFail
	if c != testCid2 {
		t.Fatal("expected", testCid2)
	}
	// Check that next call to pubfunc, after repeated update, is same as before.
	rp.Update(testCid2)
	c = <-pubFail
	if c != testCid2 {
		t.Fatal("expected", testCid2)
	}

	// Make pubfunc succeed on next try
	close(gate)

	// Make sure that all waiters were answered
	timeout := time.After(time.Second)
	for x := 0; x < 2; x++ {
		select {
		case <-waitDone:
		case <-timeout:
			t.Fatal("lost waiter after publish failure")
		}
	}
}
