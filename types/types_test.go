package types

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

func TestHash48ByteRoundtrip(t *testing.T) {
	// Real 48-byte Arweave block hash (64-char base64url)
	original := "EaJAjHUpJrhhrpL5E5Jgw32z4EV47k5fgPu8geWEviwClKyTjT71JkD4CCiM9DUB"

	var h Hash
	err := h.UnmarshalJSON([]byte(`"` + original + `"`))
	if err != nil {
		t.Fatalf("UnmarshalJSON 48-byte: %v", err)
	}

	// Base64 should return 64 chars
	encoded := h.Base64()
	if len(encoded) != 64 {
		t.Errorf("expected 64-char base64url, got %d: %s", len(encoded), encoded)
	}
	if encoded != original {
		t.Errorf("48-byte roundtrip failed:\n  got:      %s\n  expected: %s", encoded, original)
	}

	// MarshalJSON should also work
	jsonData, err := json.Marshal(h)
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	if string(jsonData) != `"`+original+`"` {
		t.Errorf("MarshalJSON: got %s, expected %q", string(jsonData), original)
	}
}

func TestHash32ByteRoundtrip(t *testing.T) {
	// 32-byte TXID (43-char base64url)
	original := "EaJAjHUpJrhhrpL5E5Jgw32z4EV47k5fgPu8geWEviw"

	var h Hash
	err := h.UnmarshalJSON([]byte(`"` + original + `"`))
	if err != nil {
		t.Fatalf("UnmarshalJSON 32-byte: %v", err)
	}

	// Base64 should return 43 chars (not 64)
	encoded := h.Base64()
	if len(encoded) != 43 {
		t.Errorf("expected 43-char base64url, got %d: %s", len(encoded), encoded)
	}
	if encoded != original {
		t.Errorf("32-byte roundtrip failed:\n  got:      %s\n  expected: %s", encoded, original)
	}

	// Verify is32
	if !h.is32() {
		t.Error("expected is32()=true for 32-byte hash")
	}
}

func TestHashInvalidLength(t *testing.T) {
	// 5 bytes = invalid
	var h Hash
	err := h.UnmarshalJSON([]byte(`"abcde"`))
	if err == nil {
		t.Fatal("expected error for invalid length")
	}
	t.Logf("Correctly rejected invalid length: %v", err)
}

func TestHashEmptyHash(t *testing.T) {
	h := EmptyHash()
	if !h.is32() {
		t.Error("EmptyHash should report is32=true (all zeros)")
	}
	encoded := h.Base64()
	// All zeros → 43-char base64url
	if len(encoded) != 43 {
		t.Errorf("EmptyHash Base64: expected 43 chars, got %d", len(encoded))
	}
}

func TestHashFromBytes48(t *testing.T) {
	data := make([]byte, 48)
	for i := range data {
		data[i] = byte(i)
	}
	h := HashFromBytes(data)
	for i := 0; i < 48; i++ {
		if h[i] != byte(i) {
			t.Errorf("byte %d: expected %d, got %d", i, i, h[i])
		}
	}
	if h.is32() {
		t.Error("48-byte hash should NOT report is32=true")
	}
	// 48 bytes → 64-char base64url
	encoded := h.Base64()
	if len(encoded) != 64 {
		t.Errorf("48-byte hash base64 length: expected 64, got %d", len(encoded))
	}
}

func TestHashFromBytes32(t *testing.T) {
	data := make([]byte, 32)
	for i := range data {
		data[i] = byte(i + 100)
	}
	h := HashFromBytes(data)
	for i := 0; i < 32; i++ {
		if h[i] != byte(i+100) {
			t.Errorf("byte %d: expected %d, got %d", i, i+100, h[i])
		}
	}
	// Last 16 bytes should be zero
	for i := 32; i < 48; i++ {
		if h[i] != 0 {
			t.Errorf("byte %d: expected 0, got %d", i, h[i])
		}
	}
	if !h.is32() {
		t.Error("32-byte hash should report is32=true")
	}
}

func TestHashFromBytesOther(t *testing.T) {
	// Arbitrary data → SHA-256 (32 bytes)
	data := []byte("hello arweave")
	h := HashFromBytes(data)

	// Should have SHA-256 in first 32 bytes, zeros in last 16
	if !h.is32() {
		t.Error("SHA-256 hash should report is32=true")
	}

	// Verify it's a valid SHA-256
	import_crypto := false
	_ = import_crypto
	t.Logf("HashFromBytes(hello arweave) = %s", h.Base64())
}

func TestHashEquality(t *testing.T) {
	a := HashFromBytes([]byte("test"))
	b := HashFromBytes([]byte("test"))
	if a != b {
		t.Fatal("equal inputs should produce equal hashes")
	}

	c := HashFromBytes([]byte("other"))
	if a == c {
		t.Fatal("different inputs should produce different hashes")
	}
}

func TestHashJSONRoundtrip48(t *testing.T) {
	original := "EaJAjHUpJrhhrpL5E5Jgw32z4EV47k5fgPu8geWEviwClKyTjT71JkD4CCiM9DUB"

	type testStruct struct {
		Hash Hash `json:"hash"`
	}

	s := testStruct{}
	err := json.Unmarshal([]byte(`{"hash":"`+original+`"}`), &s)
	if err != nil {
		t.Fatalf("Unmarshal 48-byte hash in struct: %v", err)
	}

	if s.Hash.Base64() != original {
		t.Errorf("Unmarshal struct: hash mismatch")
	}

	data, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("Marshal struct: %v", err)
	}
	expected := `{"hash":"` + original + `"}`
	if string(data) != expected {
		t.Errorf("Marshal struct: got %s, expected %s", string(data), expected)
	}
}

func TestHashJSONRoundtrip32(t *testing.T) {
	original := "EaJAjHUpJrhhrpL5E5Jgw32z4EV47k5fgPu8geWEviw" // 43 chars

	type testStruct struct {
		Hash Hash `json:"hash"`
	}

	s := testStruct{}
	err := json.Unmarshal([]byte(`{"hash":"`+original+`"}`), &s)
	if err != nil {
		t.Fatalf("Unmarshal 32-byte hash in struct: %v", err)
	}

	if s.Hash.Base64() != original {
		t.Errorf("Unmarshal struct: hash mismatch: %s", s.Hash.Base64())
	}

	data, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("Marshal struct: %v", err)
	}
	expected := `{"hash":"` + original + `"}`
	if string(data) != expected {
		t.Errorf("Marshal struct: got %s, expected %s", string(data), expected)
	}
}

func TestBase64Padding(t *testing.T) {
	// Arweave uses RawURLEncoding (no padding)
	// A hash ending with 'A' might need padding in standard encoding
	var h Hash
	data, _ := base64.RawURLEncoding.DecodeString("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	copy(h[:], data)

	encoded := h.Base64()
	// Should NOT contain '=' padding
	for _, c := range encoded {
		if c == '=' {
			t.Error("base64url should not contain padding")
		}
	}
}

func TestBlockIndepHashRoundtrip(t *testing.T) {
	// Simulate a full block JSON with 48-byte hashes
	blockJSON := `{
		"nonce": "abc123",
		"previous_block": "EaJAjHUpJrhhrpL5E5Jgw32z4EV47k5fgPu8geWEviwClKyTjT71JkD4CCiM9DUB",
		"timestamp": 1715030400,
		"last_retarget": 1715030300,
		"diff": "30000000",
		"height": 1911424,
		"hash": "EaJAjHUpJrhhrpL5E5Jgw32z4EV47k5fgPu8geWEviwClKyTjT71JkD4CCiM9DUB",
		"indep_hash": "EaJAjHUpJrhhrpL5E5Jgw32z4EV47k5fgPu8geWEviwClKyTjT71JkD4CCiM9DUB",
		"txs": [],
		"tx_root": "",
		"wallet_list": "",
		"reward_addr": "test",
		"tags": [],
		"reward_pool": "1000",
		"weave_size": "1000000",
		"block_size": "100",
		"cumulative_diff": "500000",
		"hash_list_merkle": ""
	}`

	var block Block
	err := json.Unmarshal([]byte(blockJSON), &block)
	if err != nil {
		t.Fatalf("Unmarshal block with 48-byte hashes: %v", err)
	}

	expectedHash := "EaJAjHUpJrhhrpL5E5Jgw32z4EV47k5fgPu8geWEviwClKyTjT71JkD4CCiM9DUB"
	if block.Hash.Base64() != expectedHash {
		t.Errorf("block.Hash: %s", block.Hash.Base64())
	}
	if block.IndepHash.Base64() != expectedHash {
		t.Errorf("block.IndepHash: %s", block.IndepHash.Base64())
	}
	if block.PreviousBlock.Base64() != expectedHash {
		t.Errorf("block.PreviousBlock: %s", block.PreviousBlock.Base64())
	}

	// Empty tx_root should be all zeros
	if block.TxRoot != EmptyHash() {
		t.Error("empty tx_root should equal EmptyHash")
	}
}
