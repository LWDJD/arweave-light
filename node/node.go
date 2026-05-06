package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/arweave-light/client"
	"github.com/arweave-light/logger"
	"github.com/arweave-light/peers"
	"github.com/arweave-light/store"
	"github.com/arweave-light/syncer"
	"github.com/arweave-light/types"
	"github.com/arweave-light/validator"
)

// Config holds the node configuration.
type Config struct {
	DataDir        string
	PeerURL        string // fallback single peer (arweave.net)
	HTTPTimeout    time.Duration
	SyncEnabled    bool
	ValidateBlocks bool
	SyncerConfig   syncer.Config

	// Peer-discovery
	Bootstrap    string
	AddPeer      string
	ListPeers    bool
	Consensus    bool // enable multi-peer consensus voting
	MinConsensus int  // minimum agreeing peers for consensus
}

// DefaultConfig returns sensible defaults.
func DefaultConfig() Config {
	return Config{
		DataDir:        "./arweave-light-data",
		PeerURL:        "https://arweave.net",
		HTTPTimeout:    30 * time.Second,
		SyncEnabled:    true,
		ValidateBlocks: true,
		SyncerConfig:   syncer.DefaultConfig(),
		Consensus:      true,
		MinConsensus:   3,
	}
}

// Node is the main coordinator.
type Node struct {
	cfg        Config
	log        *logger.Logger
	db         *store.DB
	peerStore  *peers.Store
	mc         *client.MultiClient
	validator  *validator.Validator
	syncer     *syncer.Syncer
	singlePeer *client.HTTPClient

	mu       sync.RWMutex
	running  bool
	cancelFn context.CancelFunc

	eventCh chan Event

	startTime  time.Time
	blocksSeen uint64

	checkpointPath string
}

// Event represents something that happened in the node.
type Event struct {
	Type string
	Data interface{}
	Time time.Time
}

// Event types
const (
	EventBlock        = "block"
	EventSyncComplete = "sync_complete"
	EventError        = "error"
	EventFork         = "fork"
)

// New creates a new Node instance.
func New(cfg Config) (*Node, error) {
	n := &Node{
		cfg:            cfg,
		log:            logger.NewLogger("node"),
		eventCh:        make(chan Event, 1000),
		checkpointPath: filepath.Join(cfg.DataDir, "checkpoint.json"),
	}

	// Open database
	db, err := store.Open(cfg.DataDir)
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}
	n.db = db

	// Init peer store
	peersPath := filepath.Join(cfg.DataDir, "peers.json")
	n.peerStore = peers.NewStore(peersPath)
	n.peerStore.SetLogger(logger.NewLogger("peers"))

	// Keep single-peer client for fallback queries (height-only, no data trust)
	n.singlePeer = client.NewHTTPClient(cfg.PeerURL, cfg.HTTPTimeout)

	// Multi-client for consensus-based operations
	minC := cfg.MinConsensus
	if !cfg.Consensus {
		minC = 1 // effectively single-peer mode
	}
	if minC < 1 {
		minC = 1
	}
	n.mc = client.NewMultiClient(n.peerStore, minC, cfg.HTTPTimeout)
	n.mc.SetLogger(logger.NewLogger("multiclient"))

	if cfg.ValidateBlocks {
		n.validator = validator.NewValidator()
	}

	return n, nil
}

// Start begins background operations.
func (n *Node) Start(ctx context.Context) error {
	n.mu.Lock()
	if n.running {
		n.mu.Unlock()
		return errors.New("node already running")
	}
	n.running = true
	n.startTime = time.Now()
	ctx, n.cancelFn = context.WithCancel(ctx)
	n.mu.Unlock()

	n.log.Info("Starting arweave-light (pure decentralized sync)")
	n.log.Info("Data directory: %s", n.cfg.DataDir)
	n.log.Info("Consensus mode: %v (min %d peers)", n.cfg.Consensus, n.cfg.MinConsensus)

	// ---- Peer discovery lifecycle ----
	if err := n.peerDiscovery(ctx); err != nil {
		n.log.Warn("Peer discovery: %v", err)
	}

	// ---- Print peer list if requested ----
	if n.cfg.ListPeers {
		n.printPeers()
	}

	// ---- Add manual peer ----
	if n.cfg.AddPeer != "" {
		n.peerStore.Add(n.cfg.AddPeer)
		if err := n.peerStore.Save(); err != nil {
			n.log.Warn("Failed to save peers: %v", err)
		}
		n.log.Info("Manually added peer: %s", n.cfg.AddPeer)
	}

	// Check connectivity to best peers
	topPeers := n.peerStore.Top(5)
	if len(topPeers) > 0 {
		n.log.Info("Testing connectivity to top %d peers...", len(topPeers))
		for _, p := range topPeers {
			if err := n.mc.Ping(ctx, p.URL); err != nil {
				n.log.Warn("  %s → unreachable: %v", p.URL, err)
				n.peerStore.RecordTimeout(p.URL)
			} else {
				n.log.Info("  %s → OK (score=%d)", p.URL, p.Score)
				n.peerStore.RecordSuccess(p.URL)
			}
		}
		n.peerStore.Save()
	}

	// ---- Load persisted checkpoint ----
	cp := n.loadCheckpoint()
	if cp != nil {
		n.log.Info("Loaded checkpoint: height=%d indep_hash=%s",
			cp.Height, cp.IndepHash.String()[:16])
	}

	// Get network info for display
	netHeight, err := n.fetchNetworkHeight(ctx)
	if err != nil {
		n.log.Warn("Cannot get network height: %v", err)
	} else {
		localInfo, _ := n.db.GetChainInfo()
		localH := uint64(0)
		if localInfo != nil {
			localH = localInfo.Height
		}
		if cp != nil {
			localH = cp.Height
		}
		n.log.Info("Network height: %d, local checkpoint: %d", netHeight, localH)
	}

	// ---- Start syncer ----
	if n.cfg.SyncEnabled {
		sCfg := n.cfg.SyncerConfig
		sCfg.ConsensusMode = n.cfg.Consensus
		n.syncer = syncer.NewSyncer(n.db, n.mc, n.singlePeer, n.validator, sCfg)
		n.syncer.SetCallbacks(n.onBlock, n.onSyncComplete)
		n.syncer.OnCheckpointSet(func(cp *syncer.TrustedCheckpoint) {
			n.saveCheckpoint(cp)
		})

		if cp != nil {
			n.syncer.SetCheckpoint(cp)
		}

		go func() {
			if err := n.syncer.SyncToTip(ctx); err != nil {
				if !errors.Is(err, context.Canceled) {
					n.log.Error("Sync error: %v", err)
					n.emitEvent(EventError, err.Error())
				}
			}
		}()
	}

	// Start periodic peer refresh
	go n.peerRefreshLoop(ctx)

	return nil
}

// peerDiscovery runs the bootstrap / load flow.
func (n *Node) peerDiscovery(ctx context.Context) error {
	if err := n.peerStore.Load(); err != nil {
		return fmt.Errorf("load peers: %w", err)
	}

	if n.peerStore.Len() == 0 {
		return n.bootstrap(ctx)
	}

	n.log.Info("Loaded %d peers from %s", n.peerStore.Len(),
		filepath.Join(n.cfg.DataDir, "peers.json"))
	return nil
}

// bootstrap connects to a bootstrap peer to discover other peers.
func (n *Node) bootstrap(ctx context.Context) error {
	bootstrapURL := n.cfg.Bootstrap
	if bootstrapURL == "" {
		bootstrapURL = n.cfg.PeerURL
	}

	if bootstrapURL == "" {
		return fmt.Errorf("no bootstrap peer configured (use --bootstrap)")
	}

	n.log.Info("First run: bootstrapping from %s", bootstrapURL)

	n.peerStore.Add(bootstrapURL)
	n.peerStore.RecordSuccess(bootstrapURL)

	bootstrapClient := client.NewHTTPClient(bootstrapURL, n.cfg.HTTPTimeout)
	peerURLs, err := bootstrapClient.GetPeers(ctx)
	if err != nil {
		n.log.Warn("Failed to get peers from bootstrap: %v", err)
		return n.peerStore.Save()
	}

	n.log.Info("Bootstrap returned %d peers", len(peerURLs))
	added := n.peerStore.AddPeers(peerURLs)
	n.log.Info("Added %d new peers (total: %d)", added, n.peerStore.Len())

	return n.peerStore.Save()
}

// peerRefreshLoop periodically fetches updated peer lists.
func (n *Node) peerRefreshLoop(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n.log.Info("Refreshing peer list...")
			newURLs, err := n.mc.GetPeersFromAll(ctx)
			if err != nil {
				n.log.Warn("Peer refresh failed: %v", err)
				continue
			}
			added := n.peerStore.AddPeers(newURLs)
			if added > 0 {
				n.log.Info("Peer refresh: added %d new peers (total: %d)", added, n.peerStore.Len())
				n.peerStore.Save()
			} else {
				n.log.Info("Peer refresh: no new peers found")
			}
		}
	}
}

func (n *Node) printPeers() {
	all := n.peerStore.GetAll()
	n.log.Info("---- Peer List (%d) ----", len(all))
	for i, p := range all {
		n.log.Info("  %2d. %s  score=%d  success=%d  fail=%d  last=%s",
			i+1, p.URL, p.Score, p.SuccessCount, p.FailCount,
			p.LastConnected.Format("2006-01-02 15:04:05"))
	}
	n.log.Info("-------------------------")
}

// Stop gracefully shuts down the node.
func (n *Node) Stop() error {
	n.mu.Lock()
	if !n.running {
		n.mu.Unlock()
		return nil
	}
	n.running = false
	if n.cancelFn != nil {
		n.cancelFn()
	}
	n.mu.Unlock()

	n.log.Info("Shutting down...")

	if n.syncer != nil {
		n.syncer.Cancel()
	}

	if err := n.peerStore.Save(); err != nil {
		n.log.Warn("Failed to save peers: %v", err)
	}

	if err := n.db.Close(); err != nil {
		return fmt.Errorf("close db: %w", err)
	}

	close(n.eventCh)
	n.log.Info("Node stopped")
	return nil
}

// IsRunning returns whether the node is running.
func (n *Node) IsRunning() bool {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.running
}

// Events returns the event channel.
func (n *Node) Events() <-chan Event {
	return n.eventCh
}

// GetLatestBlock returns the highest block.
func (n *Node) GetLatestBlock() (*types.Block, error) {
	return n.db.GetLatestBlock()
}

// GetBlockByHeight returns a block by height.
func (n *Node) GetBlockByHeight(height uint64) (*types.Block, error) {
	return n.db.GetBlockByHeight(height)
}

// GetBlockByHash returns a block by hash.
func (n *Node) GetBlockByHash(hash types.Hash) (*types.Block, error) {
	return n.db.GetBlockByHash(hash)
}

// GetTransaction returns a transaction.
func (n *Node) GetTransaction(ctx context.Context, txID types.Hash) (*types.Transaction, error) {
	if n.syncer != nil {
		return n.syncer.SyncTransaction(ctx, txID)
	}
	return n.singlePeer.GetTransaction(ctx, txID)
}

// GetTransactionData returns transaction data.
func (n *Node) GetTransactionData(ctx context.Context, txID types.Hash) ([]byte, error) {
	if n.syncer != nil {
		return n.syncer.SyncTransactionData(ctx, txID)
	}
	return n.singlePeer.GetTransactionData(ctx, txID)
}

// GetChainInfo returns local chain info.
func (n *Node) GetChainInfo() (*types.ChainInfo, error) {
	return n.db.GetChainInfo()
}

// FetchBlockByHeight fetches a block from the network by height.
func (n *Node) FetchBlockByHeight(ctx context.Context, height uint64) (*types.Block, error) {
	return n.singlePeer.GetBlockByHeight(ctx, height)
}

// FetchNetworkHeight returns the current network height.
func (n *Node) FetchNetworkHeight(ctx context.Context) (uint64, error) {
	return n.fetchNetworkHeight(ctx)
}

func (n *Node) fetchNetworkHeight(ctx context.Context) (uint64, error) {
	if n.cfg.Consensus && n.peerStore.Len() > 0 {
		info, err := n.mc.GetInfo(ctx)
		if err == nil {
			return info.Height, nil
		}
	}
	info, err := n.singlePeer.GetInfo(ctx)
	if err != nil {
		return 0, err
	}
	return info.Height, nil
}

// GetNetworkInfo returns info from the peer network.
func (n *Node) GetNetworkInfo(ctx context.Context) (*types.ChainInfo, error) {
	if n.cfg.Consensus && n.peerStore.Len() > 0 {
		info, err := n.mc.GetInfo(ctx)
		if err == nil {
			return info, nil
		}
	}
	return n.singlePeer.GetInfo(ctx)
}

// GetSyncStatus returns the current sync status.
func (n *Node) GetSyncStatus() types.SyncStatus {
	if n.syncer != nil {
		return n.syncer.Status()
	}
	cp := n.loadCheckpoint()
	info, _ := n.db.GetChainInfo()
	h := uint64(0)
	if cp != nil {
		h = cp.Height
	}
	if info != nil && info.Height > h {
		h = info.Height
	}
	return types.SyncStatus{
		Syncing:       false,
		CurrentHeight: h,
		TargetHeight:  0,
		BlocksBehind:  0,
	}
}

// SyncToTip triggers a manual sync.
func (n *Node) SyncToTip(ctx context.Context) error {
	if n.syncer == nil {
		return errors.New("sync not enabled")
	}
	return n.syncer.SyncToTip(ctx)
}

// GetTxAnchor returns an anchor for building a new transaction.
func (n *Node) GetTxAnchor(ctx context.Context) (types.Hash, error) {
	peers := n.peerStore.Top(3)
	if len(peers) > 0 {
		return n.mc.GetTxAnchor(ctx, peers[0].URL)
	}
	return n.singlePeer.GetTxAnchor(ctx)
}

// GetPeers returns the local peer list.
func (n *Node) GetPeers(ctx context.Context) ([]string, error) {
	return n.peerStore.URLs(), nil
}

// GetValidator returns the node's validator.
func (n *Node) GetValidator() *validator.Validator {
	return n.validator
}

// ValidateTransaction validates a transaction.
func (n *Node) ValidateTransaction(tx *types.Transaction) error {
	if n.validator == nil {
		return errors.New("validation not enabled")
	}
	return n.validator.ValidateTransaction(tx)
}

// PeerStore returns the underlying peer store.
func (n *Node) PeerStore() *peers.Store {
	return n.peerStore
}

// MultiClient returns the multi-client.
func (n *Node) MultiClient() *client.MultiClient {
	return n.mc
}

// Stats returns node statistics.
func (n *Node) Stats() map[string]interface{} {
	n.mu.RLock()
	defer n.mu.RUnlock()

	cp := n.loadCheckpoint()
	height := uint64(0)
	if cp != nil {
		height = cp.Height
	}
	info, _ := n.db.GetChainInfo()
	if info != nil && info.Height > height {
		height = info.Height
	}

	return map[string]interface{}{
		"running":       n.running,
		"uptime":        time.Since(n.startTime).String(),
		"height":        height,
		"blocks_seen":   n.blocksSeen,
		"peer_count":    n.peerStore.Len(),
		"peer":          n.cfg.PeerURL,
		"consensus":     n.cfg.Consensus,
		"min_consensus": n.cfg.MinConsensus,
	}
}

// ---- checkpoint persistence ----

type checkpointFile struct {
	Height    uint64 `json:"height"`
	IndepHash string `json:"indep_hash"`
	BlockHash string `json:"block_hash"`
	Timestamp int64  `json:"timestamp"`
}

func (n *Node) loadCheckpoint() *syncer.TrustedCheckpoint {
	data, err := os.ReadFile(n.checkpointPath)
	if err != nil {
		return nil
	}
	var cf checkpointFile
	if err := json.Unmarshal(data, &cf); err != nil {
		return nil
	}
	var indepHash types.Hash
	if err := indepHash.UnmarshalJSON([]byte(`"` + cf.IndepHash + `"`)); err != nil {
		return nil
	}
	var blockHash types.Hash
	if err := blockHash.UnmarshalJSON([]byte(`"` + cf.BlockHash + `"`)); err != nil {
		// block_hash may be empty for old checkpoints
		blockHash = types.EmptyHash()
	}
	return &syncer.TrustedCheckpoint{
		Height:    cf.Height,
		IndepHash: indepHash,
		BlockHash: blockHash,
		Timestamp: cf.Timestamp,
	}
}

func (n *Node) saveCheckpoint(cp *syncer.TrustedCheckpoint) {
	cf := checkpointFile{
		Height:    cp.Height,
		IndepHash: cp.IndepHash.Base64(),
		BlockHash: cp.BlockHash.Base64(),
		Timestamp: cp.Timestamp,
	}
	data, err := json.MarshalIndent(cf, "", "  ")
	if err != nil {
		n.log.Warn("Failed to marshal checkpoint: %v", err)
		return
	}
	tmpPath := n.checkpointPath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		n.log.Warn("Failed to write checkpoint: %v", err)
		return
	}
	if err := os.Rename(tmpPath, n.checkpointPath); err != nil {
		n.log.Warn("Failed to rename checkpoint: %v", err)
		return
	}
}

func (n *Node) onBlock(block *types.Block) {
	n.mu.Lock()
	n.blocksSeen++
	n.mu.Unlock()
	n.emitEvent(EventBlock, block)
}

func (n *Node) onSyncComplete(height uint64) {
	n.emitEvent(EventSyncComplete, height)
}

func (n *Node) emitEvent(typ string, data interface{}) {
	evt := Event{
		Type: typ,
		Data: data,
		Time: time.Now(),
	}
	select {
	case n.eventCh <- evt:
	default:
	}
}

// VerifyDataRoot checks if data matches a data root.
func VerifyDataRoot(data []byte, root types.Hash) bool {
	return validator.VerifyDataRoot(data, root)
}
