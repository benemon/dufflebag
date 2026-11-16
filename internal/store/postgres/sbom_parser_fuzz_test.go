package postgres

import (
	"testing"

	"github.com/klauspost/compress/zstd"
)

const (
	fuzzSPDXSeed = `{"spdxVersion":"SPDX-2.3","SPDXID":"SPDXRef-DOCUMENT",
		"packages":[{"name":"openssl","SPDXID":"SPDXRef-A","versionInfo":"3.0.11",
		"externalRefs":[{"referenceType":"purl","referenceLocator":"pkg:rpm/openssl@3.0.11"}]}],
		"relationships":[{"spdxElementId":"SPDXRef-DOCUMENT","relatedSpdxElement":"SPDXRef-A","relationshipType":"DESCRIBES"}]}`
	fuzzCycloneDXSeed = `{"bomFormat":"CycloneDX","specVersion":"1.6","components":[
		{"bom-ref":"a","name":"parent","version":"1","licenses":[{"license":{"id":"MIT"}}],
		"components":[{"bom-ref":"b","name":"child","version":"2","purl":"pkg:npm/child@2"}]}]}`
)

// SBOMs arrive from clients, so the decoder must reject any bytes with an
// error and never expand past the cap.
func FuzzDecompressSbom(f *testing.F) {
	encoder, err := zstd.NewWriter(nil)
	if err != nil {
		f.Fatalf("zstd encoder: %v", err)
	}
	f.Add(encoder.EncodeAll([]byte(fuzzSPDXSeed), nil))
	f.Add(encoder.EncodeAll([]byte(fuzzCycloneDXSeed), nil))
	f.Add([]byte("not-zstd"))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, compressed []byte) {
		decompressed, err := decompressSbom(compressed)
		if err == nil && len(decompressed) > maxDecompressedSbomBytes {
			t.Fatalf("decompressed %d bytes, cap is %d", len(decompressed), maxDecompressedSbomBytes)
		}
	})
}

func FuzzParseSbom(f *testing.F) {
	encoder, err := zstd.NewWriter(nil)
	if err != nil {
		f.Fatalf("zstd encoder: %v", err)
	}
	f.Add([]byte(fuzzSPDXSeed), false)
	f.Add([]byte(fuzzCycloneDXSeed), true)
	f.Add([]byte(`{"components":[{"components":[{"components":null}]}]}`), true)
	f.Add([]byte(`{"packages":[{}],"relationships":[{}]}`), false)
	f.Fuzz(func(t *testing.T, document []byte, cycloneDX bool) {
		format := "SPDX"
		if cycloneDX {
			format = "CYCLONEDX"
		}
		_, _ = parseSbom(encoder.EncodeAll(document, nil), format)
	})
}
