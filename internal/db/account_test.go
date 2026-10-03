package db

import (
	"errors"
	"testing"
)

func TestAccountIsCreatedOnce(t *testing.T) {
	d := newTestDB(t)

	if _, _, ok, err := d.GetAccount(); err != nil || ok {
		t.Fatalf("fresh database: ok=%v err=%v, want no account", ok, err)
	}
	if err := d.CreateAccount("jai", "hash-1"); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	user, hash, ok, err := d.GetAccount()
	if err != nil || !ok || user != "jai" || hash != "hash-1" {
		t.Fatalf("GetAccount = %q %q %v %v", user, hash, ok, err)
	}
	if err := d.CreateAccount("intruder", "hash-2"); !errors.Is(err, ErrAccountExists) {
		t.Fatalf("second CreateAccount err = %v, want ErrAccountExists", err)
	}
	if user, _, _, _ := d.GetAccount(); user != "jai" {
		t.Errorf("account was replaced by %q", user)
	}
}
