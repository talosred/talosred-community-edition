package store_test

import (
	"testing"
	"time"

	"github.com/talosred/ce/store"
)

func TestBroadcasterPublishReceives(t *testing.T) {
	b := store.NewBroadcaster()
	ch, cancel := b.Subscribe()
	defer cancel()

	if b.Len() != 1 {
		t.Fatalf("Len: got %d want 1", b.Len())
	}

	want := &store.RequestLog{ID: "r1"}
	b.Publish(want)

	select {
	case got := <-ch:
		if got.ID != "r1" {
			t.Errorf("got %q want r1", got.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for published event")
	}
}

func TestBroadcasterFanOut(t *testing.T) {
	b := store.NewBroadcaster()
	ch1, c1 := b.Subscribe()
	ch2, c2 := b.Subscribe()
	defer c1()
	defer c2()

	if b.Len() != 2 {
		t.Fatalf("Len: got %d want 2", b.Len())
	}

	b.Publish(&store.RequestLog{ID: "x"})

	for i, ch := range []<-chan *store.RequestLog{ch1, ch2} {
		select {
		case got := <-ch:
			if got.ID != "x" {
				t.Errorf("subscriber %d: got %q want x", i, got.ID)
			}
		case <-time.After(time.Second):
			t.Fatalf("subscriber %d timed out", i)
		}
	}
}

func TestBroadcasterCancelUnsubscribes(t *testing.T) {
	b := store.NewBroadcaster()
	_, cancel := b.Subscribe()

	if b.Len() != 1 {
		t.Fatalf("Len before cancel: got %d want 1", b.Len())
	}
	cancel()
	if b.Len() != 0 {
		t.Errorf("Len after cancel: got %d want 0", b.Len())
	}
}

func TestBroadcasterDropsOnFullBuffer(t *testing.T) {
	b := store.NewBroadcaster()
	// subscribe but never drain — buffer is 16
	_, cancel := b.Subscribe()
	defer cancel()

	// Publishing far more than the buffer must not block or panic.
	done := make(chan struct{})
	go func() {
		for range 1000 {
			b.Publish(&store.RequestLog{ID: "flood"})
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Publish blocked on a full subscriber buffer")
	}
}

func TestBroadcasterPublishNoSubscribers(t *testing.T) {
	b := store.NewBroadcaster()
	// must be a no-op, not a panic
	b.Publish(&store.RequestLog{ID: "nobody"})
	if b.Len() != 0 {
		t.Errorf("Len: got %d want 0", b.Len())
	}
}

func TestStoreInsertPublishes(t *testing.T) {
	db := setupDB(t)
	b := store.NewBroadcaster()
	s := store.New(db, b)

	ch, cancel := b.Subscribe()
	defer cancel()

	if err := s.Insert(&store.RequestLog{ID: "ins-1", TS: time.Now(), Provider: "openai", Model: "gpt-4o"}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	select {
	case got := <-ch:
		if got.ID != "ins-1" {
			t.Errorf("broadcast got %q want ins-1", got.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("Insert did not publish to broadcaster")
	}
}
