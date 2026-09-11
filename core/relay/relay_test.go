package relay

// Rendezvous tests (builder-role-and-relay.md §3, §6): data integrity in both
// arrival orders, the bounded wait for a missing peer, busy-session
// protection, and teardown propagating errors to the live peer.

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

func transfer(t *testing.T, r *Relay, dep string, payload []byte, pushFirst bool) []byte {
	t.Helper()
	var wg sync.WaitGroup
	var got []byte
	var pushErr, pullErr error

	push := func() {
		defer wg.Done()
		w, err := r.Push(context.Background(), dep)
		if err != nil {
			pushErr = err
			return
		}
		if _, err := w.Write(payload); err != nil {
			pushErr = err
			return
		}
		pushErr = w.Close()
	}
	pull := func() {
		defer wg.Done()
		rd, err := r.Pull(context.Background(), dep)
		if err != nil {
			pullErr = err
			return
		}
		got, pullErr = io.ReadAll(rd)
		r.Drop(dep, nil)
	}

	wg.Add(2)
	if pushFirst {
		go push()
		time.Sleep(10 * time.Millisecond)
		go pull()
	} else {
		go pull()
		time.Sleep(10 * time.Millisecond)
		go push()
	}
	wg.Wait()
	if pushErr != nil || pullErr != nil {
		t.Fatalf("push err = %v, pull err = %v", pushErr, pullErr)
	}
	return got
}

func TestRelayCarriesBytesBothArrivalOrders(t *testing.T) {
	payload := make([]byte, 3<<20) // spans several 1 MiB chunks
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}
	r := New(time.Second)
	for _, pushFirst := range []bool{true, false} {
		got := transfer(t, r, "dep1", payload, pushFirst)
		if !bytes.Equal(got, payload) {
			t.Fatalf("pushFirst=%v: relayed %d bytes, corrupt or truncated", pushFirst, len(got))
		}
	}
}

func TestRelayTimesOutWithoutPeer(t *testing.T) {
	r := New(30 * time.Millisecond)
	if _, err := r.Push(context.Background(), "dep1"); !errors.Is(err, ErrPeerTimeout) {
		t.Fatalf("push err = %v, want ErrPeerTimeout", err)
	}
	// The timed-out session is gone: a fresh pair rendezvouses cleanly on a
	// retry (spec §6 — retry is a fresh session).
	r.wait = time.Second
	got := transfer(t, r, "dep1", []byte("retry"), true)
	if string(got) != "retry" {
		t.Fatalf("retry after timeout relayed %q", got)
	}
}

func TestRelaySecondPusherIsBusy(t *testing.T) {
	r := New(time.Second)
	done := make(chan struct{})
	go func() {
		defer close(done)
		w, err := r.Push(context.Background(), "dep1")
		if err != nil {
			t.Errorf("first push: %v", err)
			return
		}
		_, _ = w.Write([]byte("x"))
		_ = w.Close()
	}()
	// Give the first pusher time to claim the session (the claim happens
	// before its rendezvous wait), then a second pusher must bounce.
	time.Sleep(50 * time.Millisecond)
	if _, err := r.Push(context.Background(), "dep1"); !errors.Is(err, ErrBusy) {
		t.Fatalf("second push err = %v, want ErrBusy", err)
	}
	rd, err := r.Pull(context.Background(), "dep1")
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if got, _ := io.ReadAll(rd); string(got) != "x" {
		t.Fatalf("relayed %q, want x", got)
	}
	r.Drop("dep1", nil)
	<-done
}

// A mid-transfer Drop (either side dying) surfaces its cause to the live
// peer instead of a silent truncation.
func TestRelayDropPropagatesError(t *testing.T) {
	r := New(time.Second)
	boom := errors.New("pusher died")
	go func() {
		w, err := r.Push(context.Background(), "dep1")
		if err != nil {
			return
		}
		_, _ = w.Write([]byte("partial"))
		r.Drop("dep1", boom)
	}()
	rd, err := r.Pull(context.Background(), "dep1")
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if _, err := io.ReadAll(rd); !errors.Is(err, boom) {
		t.Fatalf("read err = %v, want the drop cause", err)
	}
}
