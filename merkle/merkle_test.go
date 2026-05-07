package merkle

import (
	"encoding/base64"
	"testing"

	"github.com/arweave-light/types"
)

func b64urlDecode(s string) []byte {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}

func b64urlEncode(b []byte) string {
	return base64.RawURLEncoding.EncodeToString(b)
}

// TestComputeArweaveTxRoot_Block1911424 verifies tx_root computation against
// real Arweave block 1911424 which has 2 transactions.
func TestComputeArweaveTxRoot_Block1911424(t *testing.T) {
	// Real data from Arweave block 1911424 (fetched from ar-io.dev)
	// TX 1: rMuKKjhiovjCorQWeTqM4tylofKuubr8Vfc57YBlc_Q
	//   data_size: 749484
	//   data_root: txLWtksec7hkZn0CeAfWruXcqeK6IXrQO9amwrndYyg
	// TX 2: 8fqRjQslBYFE49owKSVZHu14viu9xTFK0pOmxftDNso
	//   data_size: 5689
	//   data_root: gLQ7KLDuWSnDDdCMH9mWRuTReGkHl1tnmPcpefQtIws
	// Expected tx_root: qtUUKZqn9r95HJK6VbQU89CZbUlbheLbIg1bTRlJptM

	leaves := []TxLeaf{
		{
			TxID:     b64urlDecode("rMuKKjhiovjCorQWeTqM4tylofKuubr8Vfc57YBlc_Q"),
			DataRoot: b64urlDecode("txLWtksec7hkZn0CeAfWruXcqeK6IXrQO9amwrndYyg"),
			DataSize: 749484,
		},
		{
			TxID:     b64urlDecode("8fqRjQslBYFE49owKSVZHu14viu9xTFK0pOmxftDNso"),
			DataRoot: b64urlDecode("gLQ7KLDuWSnDDdCMH9mWRuTReGkHl1tnmPcpefQtIws"),
			DataSize: 5689,
		},
	}

	computed, err := ComputeArweaveTxRoot(leaves)
	if err != nil {
		t.Fatalf("ComputeArweaveTxRoot: %v", err)
	}

	expected := "qtUUKZqn9r95HJK6VbQU89CZbUlbheLbIg1bTRlJptM"
	if computed.Base64() != expected {
		t.Errorf("tx_root mismatch:\n  got:      %s\n  expected: %s",
			computed.Base64(), expected)
		t.Logf("got raw hex:      %x", computed[:32])
		expectedRaw := b64urlDecode(expected)
		t.Logf("expected raw hex: %x", expectedRaw)
	}

	// Also test round-trip through types.Hash
	var expectedHash types.Hash
	if err := expectedHash.UnmarshalJSON([]byte(`"` + expected + `"`)); err != nil {
		t.Fatalf("unmarshal expected: %v", err)
	}
	if computed != expectedHash {
		t.Errorf("types.Hash comparison failed")
	}
}

// TestComputeArweaveTxRoot_Empty verifies empty tx list -> empty root.
func TestComputeArweaveTxRoot_Empty(t *testing.T) {
	computed, err := ComputeArweaveTxRoot(nil)
	if err != nil {
		t.Fatalf("ComputeArweaveTxRoot(nil): %v", err)
	}
	if computed != types.EmptyHash() {
		t.Errorf("empty tx list: expected EmptyHash, got %s", computed.Base64())
	}

	computed, err = ComputeArweaveTxRoot([]TxLeaf{})
	if err != nil {
		t.Fatalf("ComputeArweaveTxRoot([]): %v", err)
	}
	if computed != types.EmptyHash() {
		t.Errorf("empty tx list: expected EmptyHash, got %s", computed.Base64())
	}
}

// TestComputeArweaveTxRoot_Single verifies single tx behavior with and
// without padding (post-fork-2.5).
func TestComputeArweaveTxRoot_Single(t *testing.T) {
	// Case 1: data_size = 0 (no padding, offset = 0)
	leaves := []TxLeaf{
		{
			TxID:     b64urlDecode("rMuKKjhiovjCorQWeTqM4tylofKuubr8Vfc57YBlc_Q"),
			DataRoot: b64urlDecode("txLWtksec7hkZn0CeAfWruXcqeK6IXrQO9amwrndYyg"),
			DataSize: 0,
		},
	}
	computed, err := ComputeArweaveTxRoot(leaves)
	if err != nil {
		t.Fatalf("ComputeArweaveTxRoot: %v", err)
	}
	leafID := arweaveLeafHash(leaves[0].DataRoot, 0)
	leafHash := types.HashFromBytes(leafID)
	if computed != leafHash {
		t.Errorf("single tx (data_size=0): root should equal leaf hash\n  root: %s\n  leaf: %s",
			computed.Base64(), leafHash.Base64())
	}

	// Case 2: data_size exactly DATA_CHUNK_SIZE (no padding)
	leaves2 := []TxLeaf{
		{
			TxID:     b64urlDecode("8fqRjQslBYFE49owKSVZHu14viu9xTFK0pOmxftDNso"),
			DataRoot: b64urlDecode("gLQ7KLDuWSnDDdCMH9mWRuTReGkHl1tnmPcpefQtIws"),
			DataSize: dataChunkSize,
		},
	}
	computed2, err := ComputeArweaveTxRoot(leaves2)
	if err != nil {
		t.Fatalf("ComputeArweaveTxRoot: %v", err)
	}
	leafID2 := arweaveLeafHash(leaves2[0].DataRoot, dataChunkSize)
	leafHash2 := types.HashFromBytes(leafID2)
	if computed2 != leafHash2 {
		t.Errorf("single tx (data_size=256KiB): root should equal leaf hash\n  root: %s\n  leaf: %s",
			computed2.Base64(), leafHash2.Base64())
	}

	// Case 3: data_size = 749484 (needs padding, root != leaf hash)
	leaves3 := []TxLeaf{
		{
			TxID:     b64urlDecode("rMuKKjhiovjCorQWeTqM4tylofKuubr8Vfc57YBlc_Q"),
			DataRoot: b64urlDecode("txLWtksec7hkZn0CeAfWruXcqeK6IXrQO9amwrndYyg"),
			DataSize: 749484,
		},
	}
	computed3, err := ComputeArweaveTxRoot(leaves3)
	if err != nil {
		t.Fatalf("ComputeArweaveTxRoot: %v", err)
	}
	if computed3 == types.EmptyHash() {
		t.Error("single tx with padding: root should not be empty")
	}
	t.Logf("single tx with padding (749484 bytes) root: %s", computed3.Base64())
}

// TestValidateTxRoot_Structural tests the basic structural validation.
func TestValidateTxRoot_Structural(t *testing.T) {
	// Empty txs + empty root = valid
	ok, err := ValidateTxRoot(nil, types.EmptyHash())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Error("empty txs + empty root should be valid")
	}

	// Empty txs + non-empty root = invalid
	nonEmptyRoot := types.HashFromBytes([]byte("test"))
	ok, err = ValidateTxRoot(nil, nonEmptyRoot)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Error("empty txs + non-empty root should be invalid")
	}

	// Non-empty txs + empty root = invalid
	txIDs := []types.Hash{types.HashFromBytes([]byte("tx1"))}
	ok, err = ValidateTxRoot(txIDs, types.EmptyHash())
	if err == nil {
		t.Error("expected error for non-empty txs + empty root")
	}

	// Non-empty txs + non-empty root = valid (structural check only)
	ok, err = ValidateTxRoot(txIDs, nonEmptyRoot)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Error("non-empty txs + non-empty root should pass structural check")
	}
}
