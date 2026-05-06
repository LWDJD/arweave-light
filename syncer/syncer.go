package syncer

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/arweave-light/client"
	"github.com/arweave-light/store"
	"github.com/arweave-light/types"
	"github.com/arweave-light/validator"
)

// errors
var (
	ErrSyncInProgress = errors.New("syncer: sync already in progress")
	ErrNoPeers        = errors.New("syncer: no available peers")
	ErrForkDetected   = errors.New("syncer: fork detected")
	ErrNoConsensus    = errors.New("syncer: no consensus on block")
)

// Config holds syncer configuration.
type Config struct {
	MaxRetries       int
	RetryDelay       time.Duration
	BatchSize        uint64
	RollbackMaxDepth uint64
}

// DefaultConfig returns sane defaults.
func DefaultConfig() Config {
	return Config{
		MaxRetries:       3,
		RetryDelay:       2 * time.Second,
		BatchSize:        50,
		RollbackMaxDepth: 100,
	}
}

// Syncer coordinates block download and chain synchronization.
type Syncer struct {
	db        *store.DB
	mc        *client.MultiClient
	validator *validator.Validator
	cfg       Config

	mu       sync.Mutex
	syncing  bool
	cancelFn context.CancelFunc
	status   types.SyncStatus

	onBlockDownloaded func(*types.Block)
	onSyncComplete    func(uint64)
}

// NewSyncer creates a new chain syncer backed by a MultiClient.
func NewSyncer(db *store.DB, mc *client.MultiClient, v *validator.Validator, cfg Config) *Syncer {
	return &Syncer{
		db:        db,
		mc:        mc,
		validator: v,
		cfg:       cfg,
	}
}

// SetCallbacks sets event callbacks.
func (s *Syncer) SetCallbacks(onBlock func(*types.Block), onComplete func(uint64)) {
	s.onBlockDownloaded = onBlock
	s.onSyncComplete = onComplete
}

// Status returns the current sync status.
func (s *Syncer) Status() types.SyncStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

// IsSyncing returns true if a sync is running.
func (s *Syncer) IsSyncing() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.syncing
}

// SyncToTip downloads all blocks from current height to network tip.
func (s *Syncer) SyncToTip(ctx context.Context) error {
	s.mu.Lock()
	if s.syncing {
		s.mu.Unlock()
		return ErrSyncInProgress
	}
	s.syncing = true
	ctx, s.cancelFn = context.WithCancel(ctx)
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		s.syncing = false
		s.cancelFn = nil
		s.mu.Unlock()
	}()

	// get network info via consensus
	chainInfo, err := s.mc.GetInfo(ctx)
	if err != nil {
		return fmt.Errorf("get network info: %w", err)
	}

	// get local info
	localInfo, err := s.db.GetChainInfo()
	if err != nil {
		return fmt.Errorf("get local info: %w", err)
	}

	targetHeight := chainInfo.Height
	currentHeight := localInfo.Height

	log.Printf("[syncer] Local height: %d, Network height: %d (via consensus)", currentHeight, targetHeight)

	if currentHeight >= targetHeight {
		log.Printf("[syncer] Already at tip")
		s.updateStatus(false, currentHeight, targetHeight)
		return nil
	}

	s.updateStatus(true, currentHeight, targetHeight)

	// sync in batches
	for h := currentHeight + 1; h <= targetHeight; {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		end := h + s.cfg.BatchSize
		if end > targetHeight {
			end = targetHeight
		}

		for height := h; height <= end; height++ {
			if err := s.downloadBlock(ctx, height); err != nil {
				if errors.Is(err, ErrForkDetected) {
					if rollbackErr := s.rollback(ctx, height-1); rollbackErr != nil {
						return fmt.Errorf("rollback after fork: %w", rollbackErr)
					}
					localInfo, _ = s.db.GetChainInfo()
					if localInfo != nil {
						h = localInfo.Height + 1
					} else {
						h = 1
					}
					continue
				}
				return fmt.Errorf("download block %d: %w", height, err)
			}
		}

		h = end + 1

		// Save peers periodically
		if h%100 == 0 {
			s.mc.PeerStore().Save()
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}

	s.updateStatus(false, targetHeight, targetHeight)
	log.Printf("[syncer] Sync complete at height %d", targetHeight)

	// Save peer scores
	s.mc.PeerStore().Save()

	if s.onSyncComplete != nil {
		s.onSyncComplete(targetHeight)
	}

	return nil
}

// downloadBlock fetches, validates, and stores a single block.
func (s *Syncer) downloadBlock(ctx context.Context, height uint64) error {
	var lastErr error
	for retry := 0; retry <= s.cfg.MaxRetries; retry++ {
		if retry > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(s.cfg.RetryDelay):
			}
		}

		// Use multi-client consensus to get block
		cr, err := s.mc.GetBlockByHeight(ctx, height)
		if err != nil {
			lastErr = err
			log.Printf("[syncer] Failed to get block %d via consensus (retry %d/%d): %v",
				height, retry, s.cfg.MaxRetries, err)
			continue
		}

		block := cr.Block
		log.Printf("[syncer] Block %d: consensus %d/%d, hash=%s",
			height, cr.Agreed, cr.Total, block.Hash)

		// get previous block for validation
		var prevBlock *types.Block
		if height > 0 {
			prevBlock, err = s.db.GetBlockByHeight(height - 1)
			if err != nil && !errors.Is(err, store.ErrNotFound) {
				lastErr = fmt.Errorf("get prev block: %w", err)
				continue
			}
		}

		// validate block
		if s.validator != nil {
			if err := s.validator.ValidateBlock(block, prevBlock); err != nil {
				if prevBlock != nil && block.PreviousBlock != prevBlock.Hash {
					log.Printf("[syncer] Fork detected at height %d", height)
					return ErrForkDetected
				}
				lastErr = fmt.Errorf("validate block %d: %w", height, err)
				log.Printf("[syncer] Block %d validation failed: %v", height, err)
				continue
			}
		}

		// store block
		if err := s.db.PutBlock(block); err != nil {
			if errors.Is(err, store.ErrBlockExists) {
				return nil
			}
			lastErr = fmt.Errorf("store block: %w", err)
			continue
		}

		log.Printf("[syncer] Stored block %d (%d txs)", height, len(block.Txs))

		if s.onBlockDownloaded != nil {
			s.onBlockDownloaded(block)
		}

		return nil
	}

	return fmt.Errorf("download block %d failed after %d retries: %w",
		height, s.cfg.MaxRetries, lastErr)
}

// rollback removes blocks above the given height.
func (s *Syncer) rollback(ctx context.Context, height uint64) error {
	log.Printf("[syncer] Rolling back to height %d", height)
	return s.db.DeleteBlocksAbove(height)
}

// SyncTransaction fetches and stores a specific transaction.
func (s *Syncer) SyncTransaction(ctx context.Context, txID types.Hash) (*types.Transaction, error) {
	tx, err := s.db.GetTransaction(txID)
	if err == nil {
		return tx, nil
	}

	// Try top peers for transaction
	peers := s.mc.PeerStore().Top(3)
	if len(peers) == 0 {
		return nil, ErrNoPeers
	}

	for _, p := range peers {
		tx, err = s.mc.GetTransaction(ctx, p.URL, txID)
		if err == nil {
			break
		}
	}
	if err != nil {
		return nil, fmt.Errorf("fetch tx %s: %w", txID, err)
	}

	if s.validator != nil {
		if err := s.validator.ValidateTransaction(tx); err != nil {
			return nil, fmt.Errorf("validate tx %s: %w", txID, err)
		}
	}

	if err := s.db.PutTransaction(tx); err != nil {
		log.Printf("[syncer] Failed to store tx %s: %v", txID, err)
	}

	return tx, nil
}

// SyncTransactionData fetches transaction data and verifies data_root.
func (s *Syncer) SyncTransactionData(ctx context.Context, txID types.Hash) ([]byte, error) {
	tx, err := s.SyncTransaction(ctx, txID)
	if err != nil {
		return nil, err
	}

	peers := s.mc.PeerStore().Top(3)
	if len(peers) == 0 {
		return nil, ErrNoPeers
	}

	var data []byte
	for _, p := range peers {
		data, err = s.mc.GetTransactionData(ctx, p.URL, txID)
		if err == nil {
			break
		}
	}
	if err != nil {
		return nil, fmt.Errorf("fetch data: %w", err)
	}

	if !validator.VerifyDataRoot(data, tx.DataRoot) {
		return nil, fmt.Errorf("data root mismatch for tx %s", txID)
	}

	return data, nil
}

func (s *Syncer) updateStatus(syncing bool, current, target uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	behind := uint64(0)
	if target > current {
		behind = target - current
	}
	s.status = types.SyncStatus{
		Syncing:       syncing,
		CurrentHeight: current,
		TargetHeight:  target,
		BlocksBehind:  behind,
	}
}

// Cancel stops an ongoing sync.
func (s *Syncer) Cancel() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancelFn != nil {
		s.cancelFn()
	}
}
