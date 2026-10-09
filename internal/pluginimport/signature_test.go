package pluginimport

import (
	"os"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"
)

// testdata is verbatim from releases.hashicorp.com/packer-plugin-amazon/1.8.2/,
// captured 2026-10-09.
func TestTheRealHashiCorpSignatureVerifies(t *testing.T) {
	ring, err := HashiCorpKeyring()
	if err != nil {
		t.Fatal(err)
	}
	sums, _ := os.ReadFile("testdata/packer-plugin-amazon_1.8.2_SHA256SUMS")
	signature, _ := os.ReadFile("testdata/packer-plugin-amazon_1.8.2_SHA256SUMS.72D7468F.sig")
	if err := verifySignature(ring, sums, signature); err != nil {
		t.Fatalf("real amazon 1.8.2 signature: %v", err)
	}
	tampered := append([]byte(nil), sums...)
	tampered[0] ^= 1
	if err := verifySignature(ring, tampered, signature); err == nil {
		t.Fatal("a tampered SHA256SUMS verified")
	}
	stranger, err := openpgp.NewEntity("stranger", "", "stranger@example.com", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifySignature(openpgp.EntityList{stranger}, sums, signature); err == nil {
		t.Fatal("HashiCorp's signature verified against an unrelated key")
	}
}
