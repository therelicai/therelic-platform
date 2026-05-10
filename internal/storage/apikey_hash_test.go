package storage

import (
	"strings"
	"testing"
)

func TestHashAPIKeyPlain(t *testing.T) {
	plaintext := "rk_dev_test_key_do_not_use_in_production"
	got := hashAPIKeyPlain(plaintext)
	want := "a3ae32a4d839195a3d546ebe78c79fd8bd6a673a8a22a8935cd938c0e0edc878"
	if got != want {
		t.Errorf("hashAPIKeyPlain mismatch — seed.sql relies on this value being stable\n got: %s\nwant: %s", got, want)
	}
}

func TestHashAPIKeyHMAC_DependsOnPepper(t *testing.T) {
	plaintext := "rk_test"
	h1 := hashAPIKeyHMAC(plaintext, []byte("pepper-a"))
	h2 := hashAPIKeyHMAC(plaintext, []byte("pepper-b"))
	if h1 == h2 {
		t.Fatalf("HMAC must change when pepper changes; got identical %s", h1)
	}
}

func TestHashAPIKeyHMAC_DependsOnPlaintext(t *testing.T) {
	pepper := []byte("pepper")
	h1 := hashAPIKeyHMAC("rk_aaa", pepper)
	h2 := hashAPIKeyHMAC("rk_bbb", pepper)
	if h1 == h2 {
		t.Fatalf("HMAC must change when plaintext changes; got identical %s", h1)
	}
}

func TestHashAPIKeyHMAC_LengthAndCharset(t *testing.T) {
	got := hashAPIKeyHMAC("rk_test", []byte("pepper"))
	if len(got) != 64 {
		t.Errorf("HMAC hex length: got %d, want 64", len(got))
	}
	if strings.ContainsAny(got, "ghijklmnopqrstuvwxyz") {
		t.Errorf("HMAC hex should be 0-9a-f, got %q", got)
	}
}

func TestHashAPIKeyHMAC_DiffersFromPlain(t *testing.T) {
	plaintext := "rk_test"
	plain := hashAPIKeyPlain(plaintext)
	hmacHash := hashAPIKeyHMAC(plaintext, []byte("pepper"))
	if plain == hmacHash {
		t.Fatalf("plain and HMAC outputs must differ; both %s", plain)
	}
}
