package clients

import (
	"context"
	"errors"
	"testing"
	"time"
)

// A server that refuses every dial never reads as connected, not even
// for the moment between attempts.
func TestRefusedDialsNeverReadConnected(t *testing.T) {
	var tracker connectionTracker
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	attempts := make(chan struct{}, 10)
	go runWithBackoff(ctx, "test", "http://192.0.2.1", &tracker, func(context.Context) error {
		attempts <- struct{}{}
		return errors.New("connection refused")
	})
	deadline := time.After(1500 * time.Millisecond)
	for seen := 0; ; {
		select {
		case <-attempts:
			seen++
		case <-deadline:
			if seen < 2 {
				t.Fatalf("%d attempts; want retries", seen)
			}
			return
		default:
			if tracker.Connection().Connected {
				t.Fatal("read connected while every dial was refused")
			}
			time.Sleep(time.Millisecond)
		}
	}
}

// Connected from the moment the socket is open until it drops.
func TestConnectedOnlyWhileUp(t *testing.T) {
	var tracker connectionTracker
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	open, drop := make(chan struct{}), make(chan struct{})
	go runWithBackoff(ctx, "test", "http://192.0.2.1", &tracker, func(ctx context.Context) error {
		tracker.connected()
		select {
		case open <- struct{}{}:
		case <-ctx.Done():
			return nil
		}
		select {
		case <-drop:
		case <-ctx.Done():
		}
		return errors.New("closed")
	})
	<-open
	if !tracker.Connection().Connected {
		t.Fatal("not connected with the socket open")
	}
	drop <- struct{}{}
	deadline := time.Now().Add(time.Second)
	for tracker.Connection().Connected {
		if time.Now().After(deadline) {
			t.Fatal("still connected after the drop")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestDroppedReportsHowLongItWasUp(t *testing.T) {
	var tracker connectionTracker
	if lasted := tracker.dropped(); lasted != 0 {
		t.Errorf("never up, yet lasted %v", lasted)
	}
	tracker.connected()
	time.Sleep(20 * time.Millisecond)
	if lasted := tracker.dropped(); lasted < 20*time.Millisecond {
		t.Errorf("lasted %v", lasted)
	}
}
