package store

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"testing"
)

func TestActivityPubIdentityIsKeptAcrossSwitchingOff(t *testing.T) {
	_, alice, _ := twoUsers(t)
	if _, err := alice.APAccount(alice.UID()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a new account has an identity: %v", err)
	}
	a, err := alice.APEnable(alice.UID(), "https://alice.example/")
	if err != nil || !a.Enabled || a.Actor != "https://alice.example/" {
		t.Fatalf("enable: %+v %v", a, err)
	}
	blk, _ := pem.Decode([]byte(a.PublicPEM))
	pub, err := x509.ParsePKIXPublicKey(blk.Bytes)
	if k, ok := pub.(*rsa.PublicKey); err != nil || !ok || k.N.BitLen() != 2048 {
		t.Fatalf("public key: %v %T", err, pub)
	}
	blk, _ = pem.Decode([]byte(a.PrivatePEM))
	if _, err := x509.ParsePKCS8PrivateKey(blk.Bytes); err != nil {
		t.Fatalf("private key: %v", err)
	}
	if err := alice.APDisable(alice.UID()); err != nil {
		t.Fatal(err)
	}
	// switching on again, even at another address, keeps the first address and the key
	b, err := alice.APEnable(alice.UID(), "https://elsewhere.example/")
	if err != nil || !b.Enabled || b.Actor != a.Actor || b.PublicPEM != a.PublicPEM || b.PrivatePEM != a.PrivatePEM {
		t.Fatalf("re-enable: %+v %v", b, err)
	}
}

func TestAnExportLeavesTheSigningKeyOut(t *testing.T) {
	_, alice, _ := twoUsers(t)
	a, _ := alice.APEnable(alice.UID(), "https://alice.example/")
	files := unzip(t, exportBytes(t, alice))
	for name, b := range files {
		if bytesContain(b, "BEGIN PRIVATE KEY") || bytesContain(b, a.PrivatePEM[:60]) {
			t.Errorf("%s holds the private key", name)
		}
	}
	got, err := alice.APAccount(alice.UID()) // the account itself still has it
	if err != nil || got.PrivatePEM != a.PrivatePEM {
		t.Errorf("the account lost its key: %v", err)
	}
}

func bytesContain(b []byte, s string) bool {
	return len(s) > 0 && string(b) != "" && indexOf(string(b), s) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
