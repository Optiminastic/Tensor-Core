package secretbox

import "testing"

func TestSealOpenRoundTrip(t *testing.T) {
	box, err := New("a-development-key")
	if err != nil {
		t.Fatal(err)
	}
	token := "shpat_secret_access_token"
	sealed, err := box.Seal(token)
	if err != nil {
		t.Fatal(err)
	}
	if sealed == token {
		t.Fatal("sealed value should not equal the plaintext")
	}
	got, err := box.Open(sealed)
	if err != nil {
		t.Fatal(err)
	}
	if got != token {
		t.Errorf("open = %q, want %q", got, token)
	}
}

func TestOpenWithWrongKeyFails(t *testing.T) {
	a, _ := New("key-one")
	b, _ := New("key-two")
	sealed, _ := a.Seal("secret")
	if _, err := b.Open(sealed); err == nil {
		t.Error("opening with the wrong key must fail")
	}
}

func TestEmptyKeyRejected(t *testing.T) {
	if _, err := New(""); err == nil {
		t.Error("an empty key must be rejected")
	}
}

func TestNonceIsRandom(t *testing.T) {
	box, _ := New("k")
	a, _ := box.Seal("same")
	b, _ := box.Seal("same")
	if a == b {
		t.Error("two seals of the same plaintext should differ (random nonce)")
	}
}
