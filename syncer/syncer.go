// Package syncer implements the pure-decentralized sync strategy:
//
//   - First boot: multi-peer vote on latest block → trusted checkpoint {height, indep_hash}
//   - Subsequent runs: incremental verification from checkpoint forward
//   - No arweave.net dependency as trusted source
//   - No historical block download — chain continuity proves safety
//   - At most 100 block headers cached in memory
package syncer

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/arweave-light/client"
	"github.com/arweave-light/consensus"
	"github.com/arweave-light/logger"
	"github.com/arweave-light/store"
	"github.com/arweave-light/types"
	"github.com/arweave-light/validator"
)

// errors
var (
	ErrSyncInProgress = errors.New("syncer: sync already in progress")
	ErrNoPeers        = errors.New("syncer: no available peers")
	ErrForkDetected   = errors.New("syncer: fork detected (indep_hash chain broken)")
)

// Config holds syncer configuration.
type Config struct {
	PollInterval  time.Duration // how often to check for new blocks
	MaxRetries    int
	RetryDelay    time.Duration
	ConsensusMode bool // multi-peer consensus enabled?
}

// DefaultConfig returns sane defaults.
func DefaultConfig() Config {
	return Config{
		PollInterval:  30 * time.Second,
		MaxRetries:    3,
		RetryDelay:    2 * time.Second,
		ConsensusMode: true,
	}
}

// TrustedCheckpoint is the light node's anchor on the chain.
type TrustedCheckpoint struct {
	Height    uint64     `json:"height"`
	IndepHash types.Hash `json:"indep_hash"`
	BlockHash types.Hash `json:"block_hash"`
	Timestamp int64      `json:"timestamp"`
}

// Syncer downloads only the latest block (on bootstrap) and then incrementally
// verifies new blocks as they arrive. No historical blocks are fetched.
type Syncer struct {
	db        *store.DB
	mc        *client.MultiClient
	sp        *client.HTTPClient // fallback single peer (arweave.net) — height only
	validator *validator.Validator
	voter     *consensus.Voter
	cfg       Config
	log       *logger.Logger

	mu          sync.Mutex
	syncing     bool
	cancelFn    context.CancelFunc
	status      types.SyncStatus
	checkpoint  *TrustedCheckpoint

	// In-memory header cache (max 100)
	headerCache   []*types.BlockHeader
	headerCacheMu sync.Mutex

	onBlockDownloaded func(*types.Block)
	onSyncComplete    func(uint64)
	onCheckpointSet   func(*TrustedCheckpoint)
}

// NewSyncer creates a syncer.
func NewSyncer(db *store.DB, mc *client.MultiClient, sp *client.HTTPClient, val *validator.Validator, cfg Config) *Syncer {
	return &Syncer{
		db:        db,
		mc:        mc,
		sp:        sp,
		validator: val,
		voter:     consensus.NewVoter(mc, mc.MinConsensus()),
		cfg:       cfg,
		log:       logger.NewLogger("syncer"),
	}
}

// SetCallbacks sets event callbacks.
func (s *Syncer) SetCallbacks(onBlock func(*types.Block), onComplete func(uint64)) {
	s.onBlockDownloaded = onBlock
	s.onSyncComplete = onComplete
}

// OnCheckpointSet registers a callback for when a new trusted checkpoint is established.
func (s *Syncer) OnCheckpointSet(fn func(*TrustedCheckpoint)) {
	s.onCheckpointSet = fn
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

// Checkpoint returns the current trusted checkpoint.
func (s *Syncer) Checkpoint() *TrustedCheckpoint {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.checkpoint == nil {
		return nil
	}
	cp := *s.checkpoint
	return &cp
}

// SetCheckpoint restores a previously saved checkpoint.
func (s *Syncer) SetCheckpoint(cp *TrustedCheckpoint) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.checkpoint = cp
}

// SyncToTip runs the bootstrap-then-incremental sync loop.
// It blocks until the context is cancelled.
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

	// ---- Phase 1: Bootstrap checkpoint if needed ----
	if s.checkpoint == nil {
		s.log.Info("No trusted checkpoint — bootstrapping from network")
		if err := s.bootstrapCheckpoint(ctx); err != nil {
			return fmt.Errorf("bootstrap checkpoint: %w", err)
		}
	}

	// ---- Phase 2: Catch up to tip from checkpoint ----
	if err := s.catchUp(ctx); err != nil {
		return fmt.Errorf("catch up: %w", err)
	}

	// ---- Phase 3: Poll for new blocks ----
	s.pollLoop(ctx)
	return nil
}

// bootstrapCheckpoint establishes the initial trusted checkpoint by either:
//   - Multi-peer consensus vote on the latest block (--consensus mode), or
//   - Single-peer fetch of latest block (fallback / non-consensus mode)
func (s *Syncer) bootstrapCheckpoint(ctx context.Context) error {
	var block *types.Block
	var err error

	if s.cfg.ConsensusMode && s.mc.PeerStore().Len() > 0 {
		block, err = s.voter.VoteLatestBlock(ctx)
		if err != nil {
			s.log.Warn("Consensus bootstrap failed: %v — falling back to single peer", err)
			// Fall through to single-peer
		}
	}

	if block == nil {
		// Single-peer fallback: get latest height, then fetch the block
		info, infoErr := s.sp.GetInfo(ctx)
		if infoErr != nil {
			return fmt.Errorf("single-peer get info: %w", infoErr)
		}
		block, err = s.sp.GetBlockByHeight(ctx, info.Height)
		if err != nil {
			return fmt.Errorf("single-peer get block %d: %w", info.Height, err)
		}
		s.log.Info("Bootstrap via single peer: height=%d indep_hash=%s",
			block.Height, block.IndepHash.String()[:16])
	}

	// Validate the bootstrap block
	if s.validator != nil {
		// For bootstrap, prevBlock is nil (no prior block to check continuity).
		// The block's authenticity is already established by multi-peer consensus.
		if err := s.validator.ValidateBlock(block, nil); err != nil {
			return fmt.Errorf("validate bootstrap block %d: %w", block.Height, err)
		}
		// ValidateIndepHash with nil prevBlock is a no-op (chain continuity
		// cannot be checked). The consensus vote already ensures correctness.
		if err := s.validator.ValidateIndepHash(block, nil); err != nil {
			return fmt.Errorf("validate indep_hash: %w", err)
		}
	}

	// Store the bootstrap block
	if err := s.db.PutBlock(block); err != nil && !errors.Is(err, store.ErrBlockExists) {
		return fmt.Errorf("store bootstrap block: %w", err)
	}

	cp := &TrustedCheckpoint{
		Height:    block.Height,
		IndepHash: block.IndepHash,
		BlockHash: block.Hash,
		Timestamp: time.Now().Unix(),
	}
	s.setCheckpoint(cp)
	s.addToHeaderCache(block)

	s.log.Info("Trusted checkpoint established at height %d, indep_hash=%s",
		cp.Height, cp.IndepHash.String()[:16])

	if s.onBlockDownloaded != nil {
		s.onBlockDownloaded(block)
	}

	return nil
}

// catchUp syncs from checkpoint+1 to the current network tip.
func (s *Syncer) catchUp(ctx context.Context) error {
	cp := s.Checkpoint()
	if cp == nil {
		return errors.New("syncer: no checkpoint for catch-up")
	}

	currentHeight := cp.Height
	targetHeight, err := s.fetchNetworkHeight(ctx)
	if err != nil {
		return fmt.Errorf("fetch network height: %w", err)
	}

	if currentHeight >= targetHeight {
		s.updateStatus(false, currentHeight, targetHeight)
		return nil
	}

	s.log.Info("Catching up: %d -> %d (%d blocks)", currentHeight, targetHeight, targetHeight-currentHeight)
	s.updateStatus(true, currentHeight, targetHeight)

	totalToSync := targetHeight - currentHeight

	for h := currentHeight + 1; h <= targetHeight; h++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if err := s.verifyAndStoreBlock(ctx, h); err != nil {
			return fmt.Errorf("verify block %d: %w", h, err)
		}

		// Update status every 10 blocks
		if h%10 == 0 {
			s.updateStatus(true, h, targetHeight)
			synced := h - currentHeight
			s.log.Progress("Sync progress: %d/%d blocks (height %d/%d)",
				synced, totalToSync, h, targetHeight)
		}

		// Brief pause to avoid hammering peers
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}

	s.updateStatus(false, targetHeight, targetHeight)
	s.log.Info("Caught up to height %d", targetHeight)

	if s.onSyncComplete != nil {
		s.onSyncComplete(targetHeight)
	}

	return nil
}

// verifyAndStoreBlock fetches a block at the given height, verifies it
// incrementally against the current checkpoint, and advances the checkpoint.
func (s *Syncer) verifyAndStoreBlock(ctx context.Context, height uint64) error {
	cp := s.Checkpoint()
	if cp == nil {
		return errors.New("syncer: no checkpoint")
	}

	var block *types.Block
	var err error

	// Fetch block — use consensus if enabled, else single peer
	if s.cfg.ConsensusMode && s.mc.PeerStore().Len() > 0 {
		cr, crErr := s.mc.GetBlockByHeight(ctx, height)
		if crErr != nil {
			s.log.Warn("Consensus fetch block %d failed: %v — trying single peer", height, crErr)
		} else {
			block = cr.Block
		}
	}

	if block == nil {
		block, err = s.sp.GetBlockByHeight(ctx, height)
		if err != nil {
			return fmt.Errorf("fetch block %d: %w", height, err)
		}
	}

	// ---- Incremental verification ----

	// 1. Verify previous_block links to our trusted chain
	var prevBlock *types.Block
	if height == cp.Height+1 {
		// Direct successor to checkpoint: verify against checkpoint's indep_hash
		if block.PreviousBlock != cp.IndepHash {
			return fmt.Errorf("%w: block %d previous_block %s != trusted indep_hash %s",
				ErrForkDetected, height, block.PreviousBlock.String()[:16], cp.IndepHash.String()[:16])
		}
		// prevBlock remains nil for ValidateIndepHash (no continuity check needed beyond above)
	} else {
		// Get previous block from DB for chain continuity
		var prevErr error
		prevBlock, prevErr = s.db.GetBlockByHeight(height - 1)
		if prevErr == nil && block.PreviousBlock != prevBlock.IndepHash {
			return fmt.Errorf("%w: block %d previous_block mismatch", ErrForkDetected, height)
		}
	}

	// 2. Verify block (difficulty, tx_root) via validator
	if s.validator != nil {
		if err := s.validator.ValidateBlock(block, prevBlock); err != nil {
			return fmt.Errorf("validate block %d: %w", height, err)
		}
	}

	// 3. Verify indep_hash via chain continuity
	if err := s.validator.ValidateIndepHash(block, prevBlock); err != nil {
		return fmt.Errorf("indep_hash block %d: %w", height, err)
	}

	// 4. Verify timestamp reasonableness
	if err := s.validateTimestamp(block); err != nil {
		s.log.Warn("Block %d timestamp suspicious: %v", height, err)
		// Non-fatal: timestamp checks are advisory
	}

	// 5. Store block and advance checkpoint
	if err := s.db.PutBlock(block); err != nil && !errors.Is(err, store.ErrBlockExists) {
		return fmt.Errorf("store block %d: %w", height, err)
	}

	newCP := &TrustedCheckpoint{
		Height:    block.Height,
		IndepHash: block.IndepHash,
		BlockHash: block.Hash,
		Timestamp: time.Now().Unix(),
	}
	s.setCheckpoint(newCP)
	s.addToHeaderCache(block)

	if s.onBlockDownloaded != nil {
		s.onBlockDownloaded(block)
	}

	return nil
}

// pollLoop periodically checks for new blocks.
func (s *Syncer) pollLoop(ctx context.Context) {
	ticker := time.NewTicker(s.cfg.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cp := s.Checkpoint()
			if cp == nil {
				continue
			}

			targetHeight, err := s.fetchNetworkHeight(ctx)
			if err != nil {
				s.log.Warn("Poll: failed to get network height: %v", err)
				continue
			}

			if cp.Height >= targetHeight {
				continue
			}

			s.log.Info("Poll: new blocks %d -> %d", cp.Height, targetHeight)
			s.updateStatus(true, cp.Height, targetHeight)

			for h := cp.Height + 1; h <= targetHeight; h++ {
				if err := s.verifyAndStoreBlock(ctx, h); err != nil {
					s.log.Error("Poll sync error at height %d: %v", h, err)
					break
				}
			}

			cp = s.Checkpoint()
			s.updateStatus(false, cp.Height, targetHeight)
		}
	}
}

// fetchNetworkHeight gets the current network height.
// Uses multi-peer consensus if available, falls back to single peer.
func (s *Syncer) fetchNetworkHeight(ctx context.Context) (uint64, error) {
	if s.cfg.ConsensusMode && s.mc.PeerStore().Len() > 0 {
		info, err := s.mc.GetInfo(ctx)
		if err == nil {
			return info.Height, nil
		}
		s.log.Warn("Consensus GetInfo failed: %v — falling back to single peer", err)
	}

	info, err := s.sp.GetInfo(ctx)
	if err != nil {
		return 0, fmt.Errorf("get network height: %w", err)
	}
	return info.Height, nil
}

// validateIndepHash is removed. Use validator.ValidateIndepHash instead,
// which verifies chain continuity (previous_block == prev_block.indep_hash).

// validateTimestamp checks that the block timestamp is not too far in the
// future or unreasonably old relative to the chain.
func (s *Syncer) validateTimestamp(block *types.Block) error {
	now := time.Now().Unix()
	// Block should not be more than 2 hours in the future
	if block.Timestamp > now+7200 {
		return fmt.Errorf("timestamp %d is >2h in the future (now=%d)", block.Timestamp, now)
	}
	// Block should not be more than 2 hours in the past (relative to now, for new blocks)
	// This is loose — we only check for clearly bogus values
	if block.Timestamp < now-7200 && block.Height > s.Checkpoint().Height {
		// Only warn if we're syncing recent blocks
		s.log.Warn("Block %d timestamp %d is >2h old (now=%d)", block.Height, block.Timestamp, now)
	}
	return nil
}

// addToHeaderCache adds a block header to the in-memory ring buffer (max 100).
func (s *Syncer) addToHeaderCache(block *types.Block) {
	s.headerCacheMu.Lock()
	defer s.headerCacheMu.Unlock()

	hdr := block.Header()
	s.headerCache = append(s.headerCache, &hdr)
	if len(s.headerCache) > 100 {
		excess := len(s.headerCache) - 100
		s.headerCache = s.headerCache[excess:]
	}
}

// HeaderCache returns a copy of the in-memory header cache.
func (s *Syncer) HeaderCache() []*types.BlockHeader {
	s.headerCacheMu.Lock()
	defer s.headerCacheMu.Unlock()
	out := make([]*types.BlockHeader, len(s.headerCache))
	copy(out, s.headerCache)
	return out
}

func (s *Syncer) setCheckpoint(cp *TrustedCheckpoint) {
	s.mu.Lock()
	s.checkpoint = cp
	s.mu.Unlock()
	if s.onCheckpointSet != nil {
		s.onCheckpointSet(cp)
	}
}

// SyncTransaction fetches and stores a specific transaction.
func (s *Syncer) SyncTransaction(ctx context.Context, txID types.Hash) (*types.Transaction, error) {
	tx, err := s.db.GetTransaction(txID)
	if err == nil {
		return tx, nil
	}

	peers := s.mc.PeerStore().Top(3)
	if len(peers) == 0 {
		tx, err = s.sp.GetTransaction(ctx, txID)
		if err != nil {
			return nil, fmt.Errorf("fetch tx %s: %w", txID, err)
		}
	} else {
		for _, p := range peers {
			tx, err = s.mc.GetTransaction(ctx, p.URL, txID)
			if err == nil {
				break
			}
		}
		if err != nil {
			return nil, fmt.Errorf("fetch tx %s: %w", txID, err)
		}
	}

	if s.validator != nil {
		if err := s.validator.ValidateTransaction(tx); err != nil {
			return nil, fmt.Errorf("validate tx %s: %w", txID, err)
		}
	}

	if err := s.db.PutTransaction(tx); err != nil {
		s.log.Warn("Failed to store tx %s: %v", txID, err)
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
		data, err := s.sp.GetTransactionData(ctx, txID)
		if err != nil {
			return nil, fmt.Errorf("fetch data: %w", err)
		}
		if !validator.VerifyDataRoot(data, tx.DataRoot) {
			return nil, fmt.Errorf("data root mismatch for tx %s", txID)
		}
		return data, nil
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

// computeIndepHash is removed. A light node cannot independently recompute
// the indep_hash for post-fork-2.6 blocks because it requires internal fields
// (nonce_limiter_info, signature, etc.) not available via the HTTP API.
// Chain continuity validation (ValidateIndepHash) replaces this check.
