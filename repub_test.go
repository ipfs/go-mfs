package mfs

import (
	"context"
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

	rp := NewRepublisher(pf, tshort, tlong, cid.Undef)

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

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	// Check that republishing update does not call pubfunc again
	rp.Update(testCid2)
	err := rp.WaitPub(ctx)
	if err != nil {
		t.Error("got error trying to republish update:", err)
	}

	// Check that waitpub times out when blocked pubfunc is called
	rp.Update(testCid1)
	err = rp.WaitPub(ctx)
	if err != context.DeadlineExceeded {
		t.Errorf("expected %q, got %v", context.DeadlineExceeded, err)
	}

	// Check that calling Close returns
	done := make(chan struct{})
	go func() {
		rp.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Error("repub.Close did not finish")
	}
}
