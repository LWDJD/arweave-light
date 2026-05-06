package client

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arweave-light/peers"
	"github.com/arweave-light/types"
)

// ===========================================================================
// Original tests (preserved)
// ===========================================================================

func mockArweaveServer(t *testing.T, height uint64, hash string, shouldFail *atomic.Bool) *httptest.Server {
	t.Helper()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if shouldFail != nil && shouldFail.Load() {
			http.Error(w, "internal error", 500)
			return
		}

		switch r.URL.Path {
		case "/info":
			resp := map[string]interface{}{
				"height":  height,
				"current": hash,
			}
			json.NewEncoder(w).Encode(resp)

		case "/peers":
			peers := []string{
				"https://peer-a.example.com",
				"https://peer-b.example.com",
			}
			json.NewEncoder(w).Encode(peers)

		default:
			var h uint64
			fmt.Sscanf(r.URL.Path, "/block/height/%d", &h)
			block := types.Block{
				Height: h,
				Hash:   hashToType(hash),
				Txs:    []types.Hash{},
			}
			json.NewEncoder(w).Encode(block)
		}
	})

	return httptest.NewServer(handler)
}

func hashToType(s string) types.Hash {
	return types.HashFromBytes([]byte(s))
}

func TestMultiClientConsensus(t *testing.T) {
	ctx := context.Background()

	var s1, s2, s3, s4, s5 *httptest.Server
	s1 = mockArweaveServer(t, 100, "correct-hash-xxxxxxxxxxxxxxxxxx", nil)
	s2 = mockArweaveServer(t, 100, "correct-hash-xxxxxxxxxxxxxxxxxx", nil)
	s3 = mockArweaveServer(t, 100, "correct-hash-xxxxxxxxxxxxxxxxxx", nil)
	s4 = mockArweaveServer(t, 100, "wrong-hash-yyyyyyyyyyyyyyyyyy", nil)
	s5 = mockArweaveServer(t, 100, "wrong-hash-yyyyyyyyyyyyyyyyyy", nil)
	defer s1.Close()
	defer s2.Close()
	defer s3.Close()
	defer s4.Close()
	defer s5.Close()

	tmpDir := t.TempDir()
	ps := peers.NewStore(tmpDir + "/peers.json")
	ps.Add(s1.URL)
	ps.Add(s2.URL)
	ps.Add(s3.URL)
	ps.Add(s4.URL)
	ps.Add(s5.URL)

	mc := NewMultiClient(ps, 3, 5*time.Second)

	cr, err := mc.GetBlockByHeight(ctx, 100)
	if err != nil {
		t.Fatalf("GetBlockByHeight: %v", err)
	}

	if !cr.Consensus {
		t.Fatalf("expected consensus, got agreed=%d total=%d", cr.Agreed, cr.Total)
	}
	if cr.Agreed < 3 {
		t.Fatalf("expected at least 3 agree, got %d", cr.Agreed)
	}
	t.Logf("Consensus: %d/%d agree, block hash=%s", cr.Agreed, cr.Total, cr.Block.Hash)

	p1 := ps.Get(s1.URL)
	p2 := ps.Get(s4.URL)
	if p1.Score <= 0 {
		t.Errorf("correct peer score should be positive: %d", p1.Score)
	}
	if p2.Score >= 0 {
		t.Errorf("wrong peer score should be negative: %d", p2.Score)
	}
	t.Logf("Correct peer score: %d, Wrong peer score: %d", p1.Score, p2.Score)
}

func TestMultiClientNoConsensus(t *testing.T) {
	ctx := context.Background()

	s1 := mockArweaveServer(t, 100, "hash-aaaa-xxxxxxxxxxxxxxxxxxxx", nil)
	s2 := mockArweaveServer(t, 100, "hash-bbbb-xxxxxxxxxxxxxxxxxxxx", nil)
	s3 := mockArweaveServer(t, 100, "hash-cccc-xxxxxxxxxxxxxxxxxxxx", nil)
	defer s1.Close()
	defer s2.Close()
	defer s3.Close()

	tmpDir := t.TempDir()
	ps := peers.NewStore(tmpDir + "/peers.json")
	ps.Add(s1.URL)
	ps.Add(s2.URL)
	ps.Add(s3.URL)

	mc := NewMultiClient(ps, 3, 5*time.Second)

	cr, err := mc.GetBlockByHeight(ctx, 100)
	if err == nil {
		t.Fatalf("expected error (no consensus), got result: %+v", cr)
	}
	t.Logf("Expected error: %v", err)
}

func TestMultiClientPeerScoringAndNotKicked(t *testing.T) {
	ctx := context.Background()

	s1 := mockArweaveServer(t, 100, "correct-hash", nil)
	s2 := mockArweaveServer(t, 100, "correct-hash", nil)
	s3 := mockArweaveServer(t, 100, "correct-hash", nil)
	s4 := mockArweaveServer(t, 100, "wrong-hash", nil)
	defer s1.Close()
	defer s2.Close()
	defer s3.Close()
	defer s4.Close()

	tmpDir := t.TempDir()
	ps := peers.NewStore(tmpDir + "/peers.json")
	ps.Add(s1.URL)
	ps.Add(s2.URL)
	ps.Add(s3.URL)
	ps.Add(s4.URL)

	mc := NewMultiClient(ps, 2, 5*time.Second)

	for round := 0; round < 6; round++ {
		_, err := mc.GetBlockByHeight(ctx, uint64(100+round))
		if err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
		if p := ps.Get(s4.URL); p != nil {
			t.Logf("Round %d: bad peer score=%d", round, p.Score)
		}
	}

	// Bad peer should still exist (never kicked, just low score)
	if p := ps.Get(s4.URL); p == nil {
		t.Error("bad peer should NOT be kicked — only score is lowered")
	} else if p.Score > -5 {
		t.Errorf("bad peer score should be negative after mismatches: %d", p.Score)
	}
	t.Logf("Bad peer retained with score=%d (not kicked)", ps.Get(s4.URL).Score)
}

func TestMultiClientTimeoutPenalty(t *testing.T) {
	ctx := context.Background()

	var failFlag atomic.Bool
	failFlag.Store(true)
	sGood1 := mockArweaveServer(t, 100, "correct-hash", nil)
	sGood2 := mockArweaveServer(t, 100, "correct-hash", nil)
	sFail := mockArweaveServer(t, 100, "correct-hash", &failFlag)
	defer sGood1.Close()
	defer sGood2.Close()
	defer sFail.Close()

	tmpDir := t.TempDir()
	ps := peers.NewStore(tmpDir + "/peers.json")
	ps.Add(sGood1.URL)
	ps.Add(sGood2.URL)
	ps.Add(sFail.URL)

	mc := NewMultiClient(ps, 2, 5*time.Second)

	cr, err := mc.GetBlockByHeight(ctx, 100)
	if err != nil {
		t.Fatalf("GetBlockByHeight: %v", err)
	}
	t.Logf("Consensus: %d/%d", cr.Agreed, cr.Total)

	failPeer := ps.Get(sFail.URL)
	if failPeer.Score >= 0 {
		t.Errorf("failing peer should have negative score: %d", failPeer.Score)
	}
	t.Logf("Fail peer score: %d", failPeer.Score)
}

func TestMultiClientGetPeersFromAll(t *testing.T) {
	ctx := context.Background()

	s1 := mockArweaveServer(t, 100, "correct-hash", nil)
	s2 := mockArweaveServer(t, 100, "correct-hash", nil)
	defer s1.Close()
	defer s2.Close()

	tmpDir := t.TempDir()
	ps := peers.NewStore(tmpDir + "/peers.json")
	ps.Add(s1.URL)
	ps.Add(s2.URL)

	mc := NewMultiClient(ps, 2, 5*time.Second)

	urls, err := mc.GetPeersFromAll(ctx)
	if err != nil {
		t.Fatalf("GetPeersFromAll: %v", err)
	}
	if len(urls) < 2 {
		t.Fatalf("expected at least 2 unique URLs, got %d: %v", len(urls), urls)
	}
	t.Logf("Merged peer URLs: %v", urls)
}

func TestSaveLoadPeersPersistence(t *testing.T) {
	tmpDir := t.TempDir()
	peersPath := tmpDir + "/peers.json"

	ps := peers.NewStore(peersPath)
	ps.Add("https://persist1.example.com")
	ps.Add("https://persist2.example.com")
	ps.RecordSuccess("https://persist1.example.com")
	ps.RecordMismatch("https://persist2.example.com")
	ps.Save()

	ps2 := peers.NewStore(peersPath)
	if err := ps2.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}

	if ps2.Len() != 2 {
		t.Fatalf("expected 2 peers, got %d", ps2.Len())
	}

	p1 := ps2.Get("https://persist1.example.com")
	p2 := ps2.Get("https://persist2.example.com")

	if p1 == nil || p2 == nil {
		t.Fatal("peers not loaded")
	}
	if p1.Score != 1 {
		t.Errorf("p1 score: expected 1, got %d", p1.Score)
	}
	if p2.Score != -2 {
		t.Errorf("p2 score: expected -2, got %d", p2.Score)
	}
	t.Logf("Persistence OK: p1=%+v p2=%+v", p1, p2)
}

// ===========================================================================
// Bug fix tests
// ===========================================================================

// realInfoResponse is the actual format returned by https://arweave.net/info
const realCurrentHash = "EaJAjHUpJrhhrpL5E5Jgw32z4EV47k5fgPu8geWEviwClKyTjT71JkD4CCiM9DUB"

func make48ByteHash(seed string) types.Hash {
	decoded, _ := base64.RawURLEncoding.DecodeString(realCurrentHash)
	var h types.Hash
	copy(h[:], decoded)
	for i, b := range []byte(seed) {
		h[i%types.HashSize] ^= b
	}
	return h
}

// ---------------------------------------------------------------------------
// Bug #1: /info endpoint returns 48-byte base64url "current" hash
// ---------------------------------------------------------------------------

func TestInfoEndpoint48ByteHash(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/info" {
			http.Error(w, "not found", 404)
			return
		}
		resp := map[string]interface{}{
			"network":            "arweave.N.1",
			"version":            5,
			"release":            91,
			"height":             1911425,
			"current":            realCurrentHash,
			"blocks":             1911425,
			"peers":              272,
			"queue_length":       0,
			"node_state_latency": 0,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewHTTPClient(server.URL, 5*time.Second)
	info, err := client.GetInfo(context.Background())
	if err != nil {
		t.Fatalf("GetInfo failed: %v", err)
	}

	if info.Height != 1911425 {
		t.Errorf("expected height 1911425, got %d", info.Height)
	}

	if info.CurrentHash.Base64() != realCurrentHash {
		t.Errorf("hash mismatch:\n  got:      %s\n  expected: %s",
			info.CurrentHash.Base64(), realCurrentHash)
	}

	decoded, err := base64.RawURLEncoding.DecodeString(realCurrentHash)
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded) != 48 {
		t.Errorf("decoded real hash is %d bytes, expected 48", len(decoded))
	}

	for i := 0; i < 48; i++ {
		if info.CurrentHash[i] != decoded[i] {
			t.Errorf("byte %d mismatch: got %d, expected %d", i, info.CurrentHash[i], decoded[i])
		}
	}

	t.Logf("Height: %d, Current: %s", info.Height, info.CurrentHash.Base64())
}

func TestInfoEndpoint32ByteTxID(t *testing.T) {
	txID32 := "EaJAjHUpJrhhrpL5E5Jgw32z4EV47k5fgPu8geWEviw" // 43-char

	// Just test the Hash type's ability to roundtrip 32-byte values
	var txID types.Hash
	err := txID.UnmarshalJSON([]byte(`"` + txID32 + `"`))
	if err != nil {
		t.Fatalf("Failed to parse 32-byte TXID: %v", err)
	}

	encoded := txID.Base64()
	if len(encoded) != 43 {
		t.Errorf("expected 43-char base64url for 32-byte value, got %d chars: %s", len(encoded), encoded)
	}
	if encoded != txID32 {
		t.Errorf("TXID roundtrip failed: %s != %s", encoded, txID32)
	}

	t.Logf("32-byte TXID roundtrip OK: %s", txID32)
}

// ---------------------------------------------------------------------------
// Bug #2: Peer URLs without scheme (bare IP:port)
// ---------------------------------------------------------------------------

func TestNormalizePeerURL(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"165.254.143.26:1984", "http://165.254.143.26:1984"},
		{"https://arweave.net", "https://arweave.net"},
		{"http://arweave.net:1984", "http://arweave.net:1984"},
		{"arweave.net", "http://arweave.net"},
		{"165.254.143.26:1984/", "http://165.254.143.26:1984"},
		{"https://arweave.net/", "https://arweave.net"},
		{"  arweave.net:1984  ", "http://arweave.net:1984"},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := NormalizePeerURL(tt.input)
			if got != tt.expected {
				t.Errorf("NormalizePeerURL(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestBareIPPeerURLWorks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/info":
			resp := map[string]interface{}{
				"height":  100,
				"current": realCurrentHash,
			}
			json.NewEncoder(w).Encode(resp)
		case "/peers":
			peers := []string{
				"165.254.143.26:1984",
				"207.154.245.101:1984",
				"https://arweave.net",
			}
			json.NewEncoder(w).Encode(peers)
		default:
			http.Error(w, "not found", 404)
		}
	}))
	defer server.Close()

	client := NewHTTPClient(server.URL, 5*time.Second)

	peers, err := client.GetPeers(context.Background())
	if err != nil {
		t.Fatalf("GetPeers failed: %v", err)
	}

	if len(peers) != 3 {
		t.Fatalf("expected 3 peers, got %d", len(peers))
	}

	t.Logf("Raw peers: %v", peers)

	for _, p := range peers {
		c := NewHTTPClient(p, 5*time.Second)
		if !strings.HasPrefix(c.baseURL, "http://") && !strings.HasPrefix(c.baseURL, "https://") {
			t.Errorf("Client baseURL for %q is %q — missing scheme", p, c.baseURL)
		}
		t.Logf("Peer %q → normalized: %s", p, c.baseURL)
	}
}

func TestPeerURLInMultiClient(t *testing.T) {
	s1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{"height": 100, "current": realCurrentHash})
	}))
	defer s1.Close()
	s2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{"height": 100, "current": realCurrentHash})
	}))
	defer s2.Close()

	bare1 := strings.TrimPrefix(s1.URL, "http://")
	bare2 := strings.TrimPrefix(s2.URL, "http://")

	tmpDir := t.TempDir()
	ps := peers.NewStore(tmpDir + "/peers.json")
	ps.Add(bare1)
	ps.Add(bare2)

	mc := NewMultiClient(ps, 1, 5*time.Second)

	info, err := mc.GetInfo(context.Background())
	if err != nil {
		t.Fatalf("GetInfo failed: %v", err)
	}
	t.Logf("Consensus info: height=%d hash=%s", info.Height, info.CurrentHash.Base64())
}

// ---------------------------------------------------------------------------
// Bug #3: --block should fetch from network, not just local DB
// ---------------------------------------------------------------------------

func TestFetchBlockFromNetwork(t *testing.T) {
	blockHeight := uint64(1911424)
	expectedHash := make48ByteHash("block1911424")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		urlPath := r.URL.Path
		if strings.HasPrefix(urlPath, "/block/height/") {
			var h uint64
			fmt.Sscanf(urlPath, "/block/height/%d", &h)
			block := types.Block{
				Nonce:          "test-nonce",
				PreviousBlock:  make48ByteHash("prev"),
				Timestamp:      1715030400,
				LastRetarget:   1715030300,
				Diff:           types.FlexString("30000000"),
				Height:         h,
				Hash:           expectedHash,
				IndepHash:      expectedHash,
				Txs:            []types.Hash{},
				TxRoot:         types.EmptyHash(),
				WalletList:     types.EmptyHash(),
				RewardAddr:     "reward-addr",
				RewardPool:     types.FlexString("1000"),
				WeaveSize:      types.FlexString("1000000"),
				BlockSize:      types.FlexString("100"),
				CumulativeDiff: types.FlexString("500000"),
				HashListMerkle: types.EmptyHash(),
			}
			json.NewEncoder(w).Encode(block)
			return
		}
		http.Error(w, "not found", 404)
	}))
	defer server.Close()

	client := NewHTTPClient(server.URL, 5*time.Second)
	block, err := client.GetBlockByHeight(context.Background(), blockHeight)
	if err != nil {
		t.Fatalf("GetBlockByHeight from network failed: %v", err)
	}

	if block.Height != blockHeight {
		t.Errorf("expected height %d, got %d", blockHeight, block.Height)
	}
	if block.Hash != expectedHash {
		t.Errorf("hash mismatch")
	}

	t.Logf("Block fetched from network: height=%d hash=%s", block.Height, block.Hash.Base64())
}

// ---------------------------------------------------------------------------
// Bug #4: MultiClient consensus hash decoding
// ---------------------------------------------------------------------------

func TestMultiClientGetInfoConsensusHashDecoding(t *testing.T) {
	realHash := realCurrentHash

	makeInfoServer := func() *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"height":  1911425,
				"current": realHash,
			})
		}))
	}

	s1 := makeInfoServer()
	s2 := makeInfoServer()
	s3 := makeInfoServer()
	defer s1.Close()
	defer s2.Close()
	defer s3.Close()

	tmpDir := t.TempDir()
	ps := peers.NewStore(tmpDir + "/peers.json")
	ps.Add(s1.URL)
	ps.Add(s2.URL)
	ps.Add(s3.URL)

	mc := NewMultiClient(ps, 2, 5*time.Second)
	info, err := mc.GetInfo(context.Background())
	if err != nil {
		t.Fatalf("GetInfo consensus failed: %v", err)
	}

	if info.Height != 1911425 {
		t.Errorf("height mismatch: %d", info.Height)
	}

	if info.CurrentHash.Base64() != realHash {
		t.Errorf("Consensus hash corrupted!\n  got:      %s\n  expected: %s",
			info.CurrentHash.Base64(), realHash)
	}

	decoded, _ := base64.RawURLEncoding.DecodeString(realHash)
	for i := 0; i < 48; i++ {
		if info.CurrentHash[i] != decoded[i] {
			t.Fatalf("Consensus hash byte %d: got %d, expected %d. The consensus code is using ASCII bytes instead of decoding!",
				i, info.CurrentHash[i], decoded[i])
		}
	}

	t.Logf("Consensus hash correctly decoded: %s", info.CurrentHash.Base64())
}

// ---------------------------------------------------------------------------
// End-to-end: all three bugs together
// ---------------------------------------------------------------------------

func TestEndToEndRealWorldScenario(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/info":
			json.NewEncoder(w).Encode(map[string]interface{}{
				"network":     "arweave.N.1",
				"version":     5,
				"release":     91,
				"height":      1911425,
				"current":     realCurrentHash,
				"blocks":      1911425,
				"peers":       272,
				"queue_length": 0,
				"node_state_latency": 0,
			})
		case r.URL.Path == "/peers":
			json.NewEncoder(w).Encode([]string{
				"165.254.143.26:1984",
				"207.154.245.101:1984",
			})
		case r.URL.Path == "/block/height/1911424":
			json.NewEncoder(w).Encode(types.Block{
				Height:    1911424,
				Hash:      make48ByteHash("block1911424"),
				IndepHash: make48ByteHash("indep1911424"),
			})
		case strings.HasPrefix(r.URL.Path, "/tx/"):
			txID := strings.TrimPrefix(r.URL.Path, "/tx/")
			if strings.HasSuffix(txID, "/data") {
				w.Write([]byte("transaction data content"))
			} else {
				var h types.Hash
				h.UnmarshalJSON([]byte(`"` + strings.TrimSuffix(txID, "/data") + `"`))
				json.NewEncoder(w).Encode(types.Transaction{
					ID:       h,
					DataSize: "23",
				})
			}
		default:
			http.Error(w, "not found", 404)
		}
	}))
	defer server.Close()

	client := NewHTTPClient(server.URL, 5*time.Second)

	// Test 1: /info with 48-byte hash
	info, err := client.GetInfo(context.Background())
	if err != nil {
		t.Fatalf("GetInfo failed: %v", err)
	}
	if info.CurrentHash.Base64() != realCurrentHash {
		t.Errorf("48-byte current hash not preserved")
	}
	t.Logf("PASS /info: height=%d current=%s", info.Height, info.CurrentHash.Base64())

	// Test 2: /peers returns bare IP:port
	peerList, err := client.GetPeers(context.Background())
	if err != nil {
		t.Fatalf("GetPeers failed: %v", err)
	}
	for _, p := range peerList {
		c := NewHTTPClient(p, 5*time.Second)
		if !strings.HasPrefix(c.baseURL, "http") {
			t.Errorf("Peer not normalized: %s → %s", p, c.baseURL)
		}
	}
	t.Logf("PASS /peers: %v normalized OK", peerList)

	// Test 3: /block/height/{h} fetches from network
	block, err := client.GetBlockByHeight(context.Background(), 1911424)
	if err != nil {
		t.Fatalf("GetBlockByHeight from network failed: %v", err)
	}
	if block.Height != 1911424 {
		t.Errorf("Block height mismatch: %d", block.Height)
	}
	t.Logf("PASS /block/height/1911424: height=%d hash=%s", block.Height, block.Hash.Base64())
}
