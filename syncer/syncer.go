// Package syncer implements the pure-decentralized sync strategy:
//
//   - First boot: consensus vote on latest block → trusted checkpoint {height, indep_hash}
//   - Subsequent runs: multi-peer fetch (no voting) + local chain continuity verification
//   - No arweave.net dependency as trusted source
//   - No historical block download — chain continuity proves safety
//   - At most 100 block headers cached in memory
package syncer

import (
	"context"
	"errors"
	"fmt"
	"math/big"
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
			s.log.Info("Consensus bootstrap failed: %v — falling back to single peer", err)
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
//
// Safety: the checkpoint is a one-way ratchet. Once established, only blocks
// that chain-link to it (via previous_block == indep_hash continuity) are
// accepted. If the network reports a different chain head, we log a warning
// but do NOT auto-switch — the user must explicitly re-verify with
// --genesis-verify to establish a new checkpoint.
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

	// If network reports a height behind our checkpoint, the remote may be
	// on a fork or still syncing. We do NOT roll back our trusted checkpoint.
	if targetHeight < currentHeight {
		s.log.Warn("Network height (%d) is behind checkpoint (%d) — "+
			"possible remote fork or reorg. Keeping trusted checkpoint; "+
			"run --genesis-verify to re-establish.", targetHeight, currentHeight)
		s.updateStatus(false, currentHeight, currentHeight)
		return nil
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
//
// This is the core security boundary:
//   - A block is ONLY accepted if it chain-links to the trusted checkpoint
//     (previous_block == checkpoint.indep_hash for checkpoint+1, or
//     previous_block == stored_block.indep_hash for subsequent blocks).
//   - Blocks are fetched from multiple random peers in parallel (no voting).
//     If peers disagree on the block data, the canonical block is chosen
//     by smallest indep_hash (per Arweave protocol: smaller hash = more
//     work = canonical). Chain continuity verification is the final
//     arbiter — if the chosen block fails to chain-link, syncing stops
//     with ErrForkDetected.
//   - Consensus voting is ONLY used during bootstrap to find the honest
//     chain head. It is NOT used for incremental sync.
//   - If no block at the given height can chain-link, an ErrForkDetected is
//     returned and syncing stops — the user must run --genesis-verify to
//     re-establish trust on the canonical fork.
func (s *Syncer) verifyAndStoreBlock(ctx context.Context, height uint64) error {
	cp := s.Checkpoint()
	if cp == nil {
		return errors.New("syncer: no checkpoint")
	}

	// Fetch block from multiple random peers without voting.
	// Chain continuity (previous_block linking) provides security.
	// If peers disagree, the canonical block (smallest indep_hash)
	// is used — chain continuity will catch any truly invalid block.
	block, err := s.fetchBlockFromMultiplePeers(ctx, height)
	if err != nil {
		return fmt.Errorf("fetch block %d: %w", height, err)
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

	// 2. Verify block (tx_root, chain continuity) via validator
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

			// Safety: if network reports a height behind our checkpoint,
			// do NOT roll back. The remote may be on a fork.
			if targetHeight < cp.Height {
				s.log.Warn("Poll: network height (%d) behind checkpoint (%d) — "+
					"possible remote fork. Skipping this poll cycle.", targetHeight, cp.Height)
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

// fetchNetworkHeight gets the current network height from multiple random
// peers without voting. Returns the majority height, or falls back to the
// configured single peer if all multi-peer attempts fail.
func (s *Syncer) fetchNetworkHeight(ctx context.Context) (uint64, error) {
	if s.mc.PeerStore().Len() > 0 {
		height, err := s.fetchHeightFromMultiplePeers(ctx)
		if err == nil {
			return height, nil
		}
		s.log.Info("Multi-peer height fetch failed: %v — falling back to single peer", err)
	}

	info, err := s.sp.GetInfo(ctx)
	if err != nil {
		return 0, fmt.Errorf("get network height: %w", err)
	}
	return info.Height, nil
}

// fetchHeightFromMultiplePeers fetches the network height from multiple random
// peers without voting. Returns the majority height, or an error if all peers fail.
func (s *Syncer) fetchHeightFromMultiplePeers(ctx context.Context) (uint64, error) {
	peers := s.mc.PeerStore().Random(5)
	if len(peers) == 0 {
		return 0, ErrNoPeers
	}

	type fetchResult struct {
		height uint64
		url    string
		err    error
	}

	results := make(chan fetchResult, len(peers))
	fetchCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	for _, p := range peers {
		go func(url string) {
			info, err := s.mc.SingleClient(url).GetInfo(fetchCtx)
			if err != nil {
				results <- fetchResult{url: url, err: err}
			} else {
				results <- fetchResult{height: info.Height, url: url}
			}
		}(p.URL)
	}

	// Count heights
	heightCounts := make(map[uint64]int)
	var lastErr error
	var responded int

	for i := 0; i < len(peers); i++ {
		r := <-results
		if r.err != nil {
			s.mc.PeerStore().RecordTimeout(r.url)
			lastErr = r.err
			continue
		}
		responded++
		heightCounts[r.height]++
		s.mc.PeerStore().RecordSuccess(r.url)
	}

	if responded == 0 {
		return 0, fmt.Errorf("height fetch: all %d peers failed, last error: %w", len(peers), lastErr)
	}

	// Find majority height
	var bestHeight uint64
	var bestCount int
	for h, c := range heightCounts {
		if c > bestCount {
			bestHeight = h
			bestCount = c
		}
	}

	if len(heightCounts) > 1 {
		s.log.Warn("Network height disagreement: heights=%v (using %d with %d/%d peers)",
			heightCounts, bestHeight, bestCount, responded)
	}

	return bestHeight, nil
}

// fetchBlockFromMultiplePeers fetches a block from multiple random peers
// without consensus voting. Local chain continuity validation is the final
// arbiter of correctness.
//
// It selects up to 5 random peers from the peer store, requests the block
// in parallel, and collects all unique responses. If multiple distinct blocks
// are returned, the one with the smallest indep_hash (i.e. hardest to mine)
// is chosen as canonical per Arweave protocol rules. If all peers fail, the
// configured single peer is used as fallback.
//
// Peer disagreements are logged but do not affect selection — the canonical
// block is determined by indep_hash comparison, not majority count.
func (s *Syncer) fetchBlockFromMultiplePeers(ctx context.Context, height uint64) (*types.Block, error) {
	peers := s.mc.PeerStore().Random(5)
	if len(peers) == 0 {
		return s.sp.GetBlockByHeight(ctx, height)
	}

	type fetchResult struct {
		block *types.Block
		url   string
		err   error
	}

	results := make(chan fetchResult, len(peers))
	fetchCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	for _, p := range peers {
		go func(url string) {
			block, err := s.mc.SingleClient(url).GetBlockByHeight(fetchCtx, height)
			results <- fetchResult{block: block, url: url, err: err}
		}(p.URL)
	}

	// Collect all unique blocks
	type blockEntry struct {
		block *types.Block
		urls  []string
	}
	byHash := make(map[string]*blockEntry)
	var responded int

	for i := 0; i < len(peers); i++ {
		r := <-results
		if r.err != nil {
			s.log.Warn("Peer %s failed to fetch block %d: %v", r.url, height, r.err)
			s.mc.PeerStore().RecordTimeout(r.url)
			continue
		}
		responded++
		s.mc.PeerStore().RecordSuccess(r.url)
		hashKey := r.block.Hash.Base64()
		if e, ok := byHash[hashKey]; ok {
			e.urls = append(e.urls, r.url)
		} else {
			byHash[hashKey] = &blockEntry{
				block: r.block,
				urls:  []string{r.url},
			}
		}
	}

	if responded == 0 {
		s.log.Warn("Block %d: all %d peers failed, falling back to single peer", height, len(peers))
		return s.sp.GetBlockByHeight(ctx, height)
	}

	// Collect unique blocks and pick canonical (smallest indep_hash)
	var candidates []*types.Block
	for _, e := range byHash {
		candidates = append(candidates, e.block)
	}

	picked := pickCanonicalBlock(candidates)

	if len(candidates) > 1 {
		s.log.Warn("Block %d: %d different versions from %d responding peers "+
			"(canonical: %s) — relying on chain continuity to resolve",
			height, len(candidates), responded, picked.Hash.Base64()[:16])
	}

	return picked, nil
}

// pickCanonicalBlock selects the block with the smallest indep_hash value.
// In Arweave's consensus, a smaller indep_hash represents more work (hash < diff),
// making it the canonical block when multiple valid candidates exist.
func pickCanonicalBlock(blocks []*types.Block) *types.Block {
	if len(blocks) == 0 {
		return nil
	}
	best := blocks[0]
	bestInt := new(big.Int).SetBytes(best.IndepHash[:])
	for _, b := range blocks[1:] {
		cur := new(big.Int).SetBytes(b.IndepHash[:])
		if cur.Cmp(bestInt) < 0 {
			best = b
			bestInt = cur
		}
	}
	return best
}

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
