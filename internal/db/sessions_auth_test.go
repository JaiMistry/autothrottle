package db

import (
	"testing"
	"time"
)

func TestAuthSessionLifetime(t *testing.T) {
	d := newTestDB(t)
	now := time.Now()
	if err := d.CreateAuthSession("live", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := d.CreateAuthSession("lapsed", now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}

	if ok, err := d.AuthSessionValid("live", now); err != nil || !ok {
		t.Errorf("a live session reads %v, %v", ok, err)
	}
	if err := d.CreateAuthSession("edge", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if ok, _ := d.AuthSessionValid("edge", now.Add(time.Hour)); ok {
		t.Error("a session is still valid at its expiry")
	}
	if ok, _ := d.AuthSessionValid("unknown", now); ok {
		t.Error("an unknown token is valid")
	}

	// An expired row found on lookup is deleted there and then.
	if ok, _ := d.AuthSessionValid("lapsed", now); ok {
		t.Error("a lapsed session is valid")
	}
	if n, _ := d.PruneAuthSessions(now); n != 0 {
		t.Errorf("the lapsed row outlived its lookup: pruned %d", n)
	}

	if err := d.CreateAuthSession("old", now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if n, err := d.PruneAuthSessions(now); err != nil || n != 1 {
		t.Errorf("prune removed %d rows (%v), want just the lapsed one", n, err)
	}
	if ok, _ := d.AuthSessionValid("live", now); !ok {
		t.Error("prune removed a live session")
	}

	if err := d.DeleteAuthSession("live"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := d.AuthSessionValid("live", now); ok {
		t.Error("a deleted session is still valid")
	}
}
