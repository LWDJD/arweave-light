package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arweave-light/peers"
	"github.com/arweave-light/types"
)

// mockArweaveServer creates an httptest server that mimics an Arweave HTTP API.
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
			// /block/height/{h}
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
	var h types.Hash
	// Pad or truncate to 32 bytes
	bytes := []byte(s)
	for len(bytes) < 32 {
		bytes = append(bytes, 0)
	}
	copy(h[:], bytes[:32])
	return h
}

func TestMultiClientConsensus(t *testing.T) {
	// Setup: create 5 mock servers
	// 3 return correct block, 2 return wrong block
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

	// Setup peer store
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

	// Check scores: correct peers should have +1, wrong peers should have -2
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
	// Setup: 3 servers, all return different hashes
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

func TestMultiClientPeerScoringAndKick(t *testing.T) {
	ctx := context.Background()

	// 3 good servers, 1 bad
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

	// Run multiple rounds — bad peer should get kicked
	for round := 0; round < 6; round++ {
		_, err := mc.GetBlockByHeight(ctx, uint64(100+round))
		if err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
		if p := ps.Get(s4.URL); p != nil {
		t.Logf("Round %d: bad peer score=%d", round, p.Score)
	} else {
		t.Logf("Round %d: bad peer already kicked", round)
	}
	}

	// Bad peer should be kicked by now (6 * -2 = -12 < KickThreshold -10)
	if p := ps.Get(s4.URL); p != nil {
		t.Errorf("bad peer should have been kicked, score=%d", p.Score)
	}
	t.Logf("Bad peer kicked: %v", ps.Get(s4.URL) == nil)
}

func TestMultiClientTimeoutPenalty(t *testing.T) {
	ctx := context.Background()

	// 2 good, 1 that always fails
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

	// Failing peer should have ScoreTimeout penalty
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
	// Should have merged peer lists from both servers (each returns 2)
	if len(urls) < 2 {
		t.Fatalf("expected at least 2 unique URLs, got %d: %v", len(urls), urls)
	}
	t.Logf("Merged peer URLs: %v", urls)
}

func TestSaveLoadPeersPersistence(t *testing.T) {
	tmpDir := t.TempDir()
	peersPath := tmpDir + "/peers.json"

	// Create store, add peers, save
	ps := peers.NewStore(peersPath)
	ps.Add("https://persist1.example.com")
	ps.Add("https://persist2.example.com")
	ps.RecordSuccess("https://persist1.example.com")
	ps.RecordMismatch("https://persist2.example.com")
	ps.Save()

	// Load into new store
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
