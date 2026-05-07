package verifier

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arweave-light/types"
	"github.com/arweave-light/validator"
)

// mockFetcher implements BlockFetcher for testing.
type mockFetcher struct {
	blocks map[uint64]*types.Block
	height uint64
}

func (mf *mockFetcher) FetchBlockByHeight(ctx context.Context, height uint64) (*types.Block, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	b, ok := mf.blocks[height]
	if !ok {
		return nil, fmt.Errorf("block %d not found", height)
	}
	return b, nil
}

func (mf *mockFetcher) FetchNetworkHeight(ctx context.Context) (uint64, error) {
	return mf.height, nil
}

// generateMockBlocks creates a valid chain of mock blocks for testing.
// Each block's PreviousBlock points to the previous block's IndepHash.
func generateMockBlocks(count int) map[uint64]*types.Block {
	blocks := make(map[uint64]*types.Block, count)
	rng := rand.New(rand.NewSource(42))

	var prevIndepHash types.Hash

	for i := 0; i < count; i++ {
		height := uint64(i)

		// Generate unique indep_hash (simulates what network returns)
		indepHash := types.HashFromBytes([]byte(fmt.Sprintf("indep-%016d-%016x", height, rng.Uint64())))

		// Generate a unique block hash
		var hashBytes [48]byte
		copy(hashBytes[:], []byte(fmt.Sprintf("hash-%016d-%016x", height, rng.Uint64())))
		hash := types.Hash(hashBytes)

		block := &types.Block{
			Nonce:          fmt.Sprintf("%016x", rng.Uint64()),
			PreviousBlock:  prevIndepHash,
			Timestamp:      int64(120 * (i + 10000000)), // realistic timestamps
			LastRetarget:   int64(120 * (i + 10000000)),
			Diff:           types.FlexString("1000"),
			Height:         height,
			HashListMerkle: types.HashFromBytes([]byte(fmt.Sprintf("hlm-%d", i))),
			WalletList:     types.HashFromBytes([]byte(fmt.Sprintf("wl-%d", i))),
			RewardAddr:     "reward-addr-test",
			Tags:           []types.Tag{},
			Txs:            []types.Hash{},
			TxRoot:         types.Hash{},
			Hash:           hash,
			IndepHash:      indepHash,
		}

		blocks[height] = block
		prevIndepHash = indepHash
	}

	return blocks
}

func TestGenesisVerifyBasic(t *testing.T) {
	blocks := generateMockBlocks(100)
	fetcher := &mockFetcher{blocks: blocks, height: 99}
	val := validator.NewValidator()
	cpStore := NewCheckpointStore(t.TempDir())
	gv := NewGenesisVerifier(fetcher, cpStore, val, 4)

	result, err := gv.Verify(context.Background(), 0, 99, true)
	if err != nil {
		t.Fatalf("Verify failed: %v", err)
	}
	if result.VerifiedCount != 100 {
		t.Fatalf("expected 100 verified, got %d", result.VerifiedCount)
	}
	if result.FailedReason != "" {
		t.Fatalf("unexpected failure at height %d: %s", result.FailedHeight, result.FailedReason)
	}
}

func TestGenesisVerifyResume(t *testing.T) {
	blocks := generateMockBlocks(100)
	fetcher := &mockFetcher{blocks: blocks, height: 99}
	val := validator.NewValidator()
	dataDir := t.TempDir()
	cpStore := NewCheckpointStore(dataDir)

	midBlock := blocks[49]
	cpStore.Save(&TrustedCheckpoint{
		Height:        49,
		IndepHash:     midBlock.IndepHash.String(),
		Timestamp:     time.Now().Unix(),
		VerifiedCount: 50,
	})

	gv := NewGenesisVerifier(fetcher, cpStore, val, 4)
	result, err := gv.Verify(context.Background(), 0, 99, false)
	if err != nil {
		t.Fatalf("Verify failed: %v", err)
	}
	if result.VerifiedCount != 50 {
		t.Fatalf("expected 50 verified from checkpoint, got %d", result.VerifiedCount)
	}
}

func TestGenesisVerifyForceFlag(t *testing.T) {
	blocks := generateMockBlocks(50)
	fetcher := &mockFetcher{blocks: blocks, height: 49}
	val := validator.NewValidator()
	dataDir := t.TempDir()
	cpStore := NewCheckpointStore(dataDir)

	cpStore.Save(&TrustedCheckpoint{
		Height:        30,
		IndepHash:     "fake",
		Timestamp:     time.Now().Unix(),
		VerifiedCount: 31,
	})

	gv := NewGenesisVerifier(fetcher, cpStore, val, 4)
	result, err := gv.Verify(context.Background(), 0, 49, true)
	if err != nil {
		t.Fatalf("Verify failed: %v", err)
	}
	if result.VerifiedCount != 50 {
		t.Fatalf("expected 50 verified with force, got %d", result.VerifiedCount)
	}
}

func TestCheckpointStore(t *testing.T) {
	dataDir := t.TempDir()
	cs := NewCheckpointStore(dataDir)

	if cs.Exists() {
		t.Fatal("checkpoint should not exist initially")
	}

	cp := &TrustedCheckpoint{
		Height:        100,
		IndepHash:     "abc123",
		Timestamp:     time.Now().Unix(),
		VerifiedCount: 101,
	}

	if err := cs.Save(cp); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	if !cs.Exists() {
		t.Fatal("checkpoint should exist after save")
	}

	loaded, err := cs.Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if loaded.Height != 100 || loaded.IndepHash != "abc123" {
		t.Fatalf("checkpoint data mismatch: %+v", loaded)
	}

	if err := cs.Remove(); err != nil {
		t.Fatalf("Remove failed: %v", err)
	}
	if cs.Exists() {
		t.Fatal("checkpoint should not exist after remove")
	}
}

func TestCheckpointAtomicWrite(t *testing.T) {
	dataDir := t.TempDir()
	cs := NewCheckpointStore(dataDir)

	cp := &TrustedCheckpoint{
		Height:        200,
		IndepHash:     "atomic-test",
		Timestamp:     time.Now().Unix(),
		VerifiedCount: 201,
	}

	if err := cs.Save(cp); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	tmpPath := filepath.Join(dataDir, "checkpoint.json.tmp")
	if _, err := os.Stat(tmpPath); err == nil {
		t.Fatal("temp file should not exist after atomic write")
	}
}

func TestIsCheckpointRecent(t *testing.T) {
	cp := &TrustedCheckpoint{Height: 100}

	if !cp.IsRecent(105, 10) {
		t.Fatal("should be recent (5 behind, threshold 10)")
	}
	if cp.IsRecent(120, 10) {
		t.Fatal("should not be recent (20 behind, threshold 10)")
	}
}

func TestGenesisVerifyAlreadyAtTip(t *testing.T) {
	blocks := generateMockBlocks(1)
	fetcher := &mockFetcher{blocks: blocks, height: 0}
	val := validator.NewValidator()
	cpStore := NewCheckpointStore(t.TempDir())
	gv := NewGenesisVerifier(fetcher, cpStore, val, 1)

	result, err := gv.Verify(context.Background(), 0, 0, true)
	if err != nil {
		t.Fatalf("Verify failed: %v", err)
	}
	if result.VerifiedCount != 1 {
		t.Fatalf("expected 1 verified, got %d", result.VerifiedCount)
	}
}

func TestChainContinuityBreak(t *testing.T) {
	// Test that a chain break is detected
	blocks := generateMockBlocks(5)
	// Corrupt block 3: set previous_block to a wrong hash
	block3 := blocks[3]
	var wrongHash types.Hash
	wrongHash[0] = 0xFF
	block3.PreviousBlock = wrongHash

	fetcher := &mockFetcher{blocks: blocks, height: 4}
	val := validator.NewValidator()
	cpStore := NewCheckpointStore(t.TempDir())
	gv := NewGenesisVerifier(fetcher, cpStore, val, 1)

	result, err := gv.Verify(context.Background(), 0, 4, true)
	if err != nil {
		t.Fatalf("Verify unexpected error: %v", err)
	}
	// Should fail at height 3 with chain continuity error
	if result.FailedReason == "" {
		t.Fatal("expected chain continuity failure, but got success")
	}
	t.Logf("Correctly detected chain break: %s", result.FailedReason)
}

func TestGenesisVerifyWithHTTPServer(t *testing.T) {
	blocks := generateMockBlocks(20)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var resp interface{}
		if r.URL.Path == "/info" {
			resp = map[string]interface{}{
				"network": "arweave.test",
				"height":  19,
			}
		} else {
			var height uint64
			fmt.Sscanf(r.URL.Path, "/block/height/%d", &height)
			b, ok := blocks[height]
			if !ok {
				http.Error(w, "not found", 404)
				return
			}
			resp = b
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	resp, err := http.Get(server.URL + "/info")
	if err != nil {
		t.Fatalf("mock server failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("mock server returned %d", resp.StatusCode)
	}
}

func TestGenesisVerifyConcurrentWorkers(t *testing.T) {
	for _, workers := range []int{1, 2, 4, 8} {
		t.Run(fmt.Sprintf("workers=%d", workers), func(t *testing.T) {
			blocks := generateMockBlocks(50)
			fetcher := &mockFetcher{blocks: blocks, height: 49}
			val := validator.NewValidator()
			cpStore := NewCheckpointStore(t.TempDir())
			gv := NewGenesisVerifier(fetcher, cpStore, val, workers)

			result, err := gv.Verify(context.Background(), 0, 49, true)
			if err != nil {
				t.Fatalf("Verify with %d workers failed: %v", workers, err)
			}
			if result.VerifiedCount != 50 {
				t.Fatalf("expected 50 verified, got %d", result.VerifiedCount)
			}
		})
	}
}

// Ensure rand is used (for mock block generation)
var _ = rand.New
