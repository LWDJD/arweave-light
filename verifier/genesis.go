package verifier

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/arweave-light/types"
	"github.com/arweave-light/validator"
)

// BlockFetcher defines the interface for fetching blocks during genesis verification.
type BlockFetcher interface {
	FetchBlockByHeight(ctx context.Context, height uint64) (*types.Block, error)
	FetchNetworkHeight(ctx context.Context) (uint64, error)
}

// GenesisVerifier handles chain-wide verification from genesis or checkpoint.
type GenesisVerifier struct {
	fetcher    BlockFetcher
	checkpoint *CheckpointStore
	validator  *validator.Validator
	workers    int
}

// VerifyResult contains the result of a genesis verification run.
type VerifyResult struct {
	StartHeight    uint64
	EndHeight      uint64
	VerifiedCount  uint64
	FailedHeight   uint64
	FailedReason   string
	Checkpoint     *TrustedCheckpoint
	Duration       time.Duration
}

// NewGenesisVerifier creates a new genesis verifier.
func NewGenesisVerifier(fetcher BlockFetcher, cpStore *CheckpointStore, val *validator.Validator, workers int) *GenesisVerifier {
	if workers < 1 {
		workers = 1
	}
	return &GenesisVerifier{
		fetcher:    fetcher,
		checkpoint: cpStore,
		validator:  val,
		workers:    workers,
	}
}

// Verify performs genesis chain verification.
func (gv *GenesisVerifier) Verify(ctx context.Context, startFrom uint64, toHeight uint64, force bool) (*VerifyResult, error) {
	startTime := time.Now()

	currentHeight := startFrom
	if !force && gv.checkpoint.Exists() {
		cp, err := gv.checkpoint.Load()
		if err != nil {
			return nil, fmt.Errorf("load checkpoint: %w", err)
		}
		if cp != nil && cp.Height >= currentHeight {
			currentHeight = cp.Height + 1
			log.Printf("[genesis-verify] Resuming from checkpoint at height %d", cp.Height)
		}
	}

	networkHeight := toHeight
	if networkHeight == 0 {
		var err error
		networkHeight, err = gv.fetcher.FetchNetworkHeight(ctx)
		if err != nil {
			return nil, fmt.Errorf("get network height: %w", err)
		}
	}

	if currentHeight > networkHeight {
		return &VerifyResult{
			StartHeight:   startFrom,
			EndHeight:     networkHeight,
			VerifiedCount: 0,
			Checkpoint:    nil,
			Duration:      time.Since(startTime),
		}, nil
	}

	totalBlocks := networkHeight - currentHeight + 1
	log.Printf("[genesis-verify] Verifying %d blocks (height %d -> %d) with %d workers",
		totalBlocks, currentHeight, networkHeight, gv.workers)

	type fetchResult struct {
		height uint64
		block  *types.Block
		err    error
	}

	fetchCh := make(chan uint64, gv.workers*2)
	resultCh := make(chan fetchResult, gv.workers*2)

	var wg sync.WaitGroup
	for i := 0; i < gv.workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for h := range fetchCh {
				block, err := gv.fetcher.FetchBlockByHeight(ctx, h)
				resultCh <- fetchResult{height: h, block: block, err: err}
			}
		}()
	}

	go func() {
		for h := currentHeight; h <= networkHeight; h++ {
			select {
			case fetchCh <- h:
			case <-ctx.Done():
				return
			}
		}
		close(fetchCh)
	}()

	go func() {
		wg.Wait()
		close(resultCh)
	}()

	var verifiedCount uint64
	var prevBlock *types.Block
	batch := make([]fetchResult, 0, gv.workers*2)
	nextExpected := currentHeight
	progressTicker := time.NewTicker(5 * time.Second)
	defer progressTicker.Stop()

	done := false
	for !done {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case res, ok := <-resultCh:
			if !ok {
				done = true
				break
			}
			batch = append(batch, res)
		}

		processed := false
		for !processed {
			found := false
			for i, res := range batch {
				if res.height == nextExpected {
					if res.err != nil {
						return &VerifyResult{
							StartHeight:   currentHeight,
							EndHeight:     networkHeight,
							VerifiedCount: verifiedCount,
							FailedHeight:  nextExpected,
							FailedReason:  fmt.Sprintf("fetch error: %v", res.err),
							Duration:      time.Since(startTime),
						}, nil
					}

					if err := gv.validateBlock(res.block, prevBlock); err != nil {
						return &VerifyResult{
							StartHeight:   currentHeight,
							EndHeight:     networkHeight,
							VerifiedCount: verifiedCount,
							FailedHeight:  nextExpected,
							FailedReason:  err.Error(),
							Duration:      time.Since(startTime),
						}, nil
					}

					prevBlock = res.block
					atomic.AddUint64(&verifiedCount, 1)
					nextExpected++

					if verifiedCount%100 == 0 {
						cp := &TrustedCheckpoint{
							Height:        res.block.Height,
							IndepHash:     res.block.IndepHash.String(),
							Timestamp:     time.Now().Unix(),
							VerifiedCount: verifiedCount,
						}
						if err := gv.checkpoint.Save(cp); err != nil {
							log.Printf("[genesis-verify] Warning: checkpoint save failed: %v", err)
						}
					}

					batch = append(batch[:i], batch[i+1:]...)
					found = true
					break
				}
			}
			if !found {
				processed = true
			}
		}

		select {
		case <-progressTicker.C:
			pct := float64(verifiedCount) / float64(totalBlocks) * 100
			elapsed := time.Since(startTime)
			rate := float64(verifiedCount) / elapsed.Seconds()
			log.Printf("[genesis-verify] Progress: %d/%d (%.1f%%) | %.1f blocks/s | elapsed %s",
				verifiedCount, totalBlocks, pct, rate, elapsed.Round(time.Second))
		default:
		}
	}

	finalHash := prevBlock.IndepHash.String()
	cp := &TrustedCheckpoint{
		Height:        networkHeight,
		IndepHash:     finalHash,
		Timestamp:     time.Now().Unix(),
		VerifiedCount: verifiedCount,
	}
	if err := gv.checkpoint.Save(cp); err != nil {
		return nil, fmt.Errorf("save final checkpoint: %w", err)
	}

	return &VerifyResult{
		StartHeight:   currentHeight,
		EndHeight:     networkHeight,
		VerifiedCount: verifiedCount,
		Checkpoint:    cp,
		Duration:      time.Since(startTime),
	}, nil
}

// validateBlock performs full block validation including indep_hash verification.
func (gv *GenesisVerifier) validateBlock(block *types.Block, prevBlock *types.Block) error {
	// Chain continuity and structural checks
	if err := gv.validator.ValidateBlock(block, prevBlock); err != nil {
		return err
	}

	// Verify IndepHash against computed hash
	computedIndep := computeIndepHash(block)
	if computedIndep != block.IndepHash {
		return fmt.Errorf("indep_hash mismatch at height %d: computed %s, got %s",
			block.Height, computedIndep.String()[:16], block.IndepHash.String()[:16])
	}

	return nil
}

// computeIndepHash computes the block's independent hash per Arweave spec.
func computeIndepHash(block *types.Block) types.Hash {
	hasher := sha256.New()

	write := func(s string) {
		hasher.Write([]byte(s))
	}

	write(block.Nonce)
	write(block.PreviousBlock.Base64())
	write(fmt.Sprintf("%d", block.Timestamp))
	write(fmt.Sprintf("%d", block.LastRetarget))
	write(block.Diff)
	write(fmt.Sprintf("%d", block.Height))
	write(block.HashListMerkle.Base64())
	write(block.WalletList.Base64())
	write(block.RewardAddr)
	for _, tag := range block.Tags {
		hasher.Write([]byte(tag.Name))
		hasher.Write([]byte(tag.Value))
	}
	hasher.Write(block.TxRoot[:])
	hasher.Write(block.Hash[:])

	var h types.Hash
	copy(h[:], hasher.Sum(nil))
	return h
}
