package webhook

import (
	"net/netip"
	"testing"
)

func TestSignatureIndependentFixture(t *testing.T) {
	// Fixture independently computed with:
	// python3 -c 'import hmac,hashlib; print(hmac.new(b"secret",b"{\"ok\":true}",hashlib.sha512).hexdigest())'
	const want = "sha512=ebaaedc0ee1bba33d6b35bdc16cde6f350232027278da8ec5124a1a2e7d55c07a4a2be89f1c84cb059fecb793ff0c2b9b3c3beb95299f8401d1718e3683d91d2"
	if got := Signature("secret", []byte(`{"ok":true}`)); got != want {
		t.Fatalf("Signature() = %q, want %q", got, want)
	}
}

func TestSubscriptionFiltering(t *testing.T) {
	if !Subscribed(nil, OperationBucketCreated) || !Subscribed([]string{}, OperationBucketCreated) {
		t.Fatal("empty subscription did not select all operations")
	}
	if !Subscribed([]string{OperationBucketCreated}, OperationBucketCreated) {
		t.Fatal("matching subscription was refused")
	}
	if Subscribed([]string{OperationBucketDeleted}, OperationBucketCreated) {
		t.Fatal("non-matching subscription was selected")
	}
}

func TestSSRFAddressClassification(t *testing.T) {
	tests := map[string]bool{
		"169.254.169.254": true,
		"127.0.0.1":       true,
		"::1":             true,
		"10.1.2.3":        true,
		"172.16.2.3":      true,
		"172.31.255.254":  true,
		"192.168.2.3":     true,
		"8.8.8.8":         false,
	}
	for raw, want := range tests {
		if got := RefusedAddress(netip.MustParseAddr(raw)); got != want {
			t.Errorf("RefusedAddress(%s) = %v, want %v", raw, got, want)
		}
	}
}
