package node

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"sync"
	"time"

	"github.com/arweave-light/client"
	"github.com/arweave-light/peers"
	"github.com/arweave-light/store"
	"github.com/arweave-light/syncer"
	"github.com/arweave-light/types"
	"github.com/arweave-light/validator"
)

// Config holds the node configuration.
type Config struct {
	DataDir        string
	PeerURL        string // legacy, kept for backwards compat
	HTTPTimeout    time.Duration
	SyncEnabled    bool
	ValidateBlocks bool
	SyncerConfig   syncer.Config

	// New peer-discovery fields
	Bootstrap     string // bootstrap peer URL (first run)
	AddPeer       string // manual peer addition
	ListPeers     bool   // print peer list and exit
	MinConsensus  int    // minimum agreeing peers for consensus
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
		MinConsensus:   3,
	}
}

// Node is the main coordinator.
type Node struct {
	cfg        Config
	db         *store.DB
	peerStore  *peers.Store
	mc         *client.MultiClient
	validator  *validator.Validator
	syncer     *syncer.Syncer
	singlePeer *client.HTTPClient // legacy single peer client, for direct queries

	mu       sync.RWMutex
	running  bool
	cancelFn context.CancelFunc

	eventCh chan Event

	startTime  time.Time
	blocksSeen uint64
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
		cfg:     cfg,
		eventCh: make(chan Event, 1000),
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

	// Keep legacy single-peer client for direct queries
	n.singlePeer = client.NewHTTPClient(cfg.PeerURL, cfg.HTTPTimeout)

	// Multi-client for consensus-based operations
	if cfg.MinConsensus < 1 {
		cfg.MinConsensus = 1
	}
	n.mc = client.NewMultiClient(n.peerStore, cfg.MinConsensus, cfg.HTTPTimeout)

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

	log.Printf("[node] Starting arweave-light node (multi-peer)")
	log.Printf("[node] Data directory: %s", n.cfg.DataDir)
	log.Printf("[node] Min consensus: %d", n.cfg.MinConsensus)

	// ---- Peer discovery lifecycle ----
	if err := n.peerDiscovery(ctx); err != nil {
		log.Printf("[node] WARNING: peer discovery: %v", err)
	}

	// ---- Print peer list if requested ----
	if n.cfg.ListPeers {
		n.printPeers()
	}

	// ---- Add manual peer ----
	if n.cfg.AddPeer != "" {
		n.peerStore.Add(n.cfg.AddPeer)
		if err := n.peerStore.Save(); err != nil {
			log.Printf("[node] Failed to save peers: %v", err)
		}
		log.Printf("[node] Manually added peer: %s", n.cfg.AddPeer)
	}

	// Check connectivity to best peers
	topPeers := n.peerStore.Top(5)
	if len(topPeers) > 0 {
		log.Printf("[node] Testing connectivity to top %d peers...", len(topPeers))
		for _, p := range topPeers {
			if err := n.mc.Ping(ctx, p.URL); err != nil {
				log.Printf("[node]   %s → unreachable: %v", p.URL, err)
				n.peerStore.RecordTimeout(p.URL)
			} else {
				log.Printf("[node]   %s → OK (score=%d)", p.URL, p.Score)
				n.peerStore.RecordSuccess(p.URL)
			}
		}
		n.peerStore.Save()
	}

	// Get network info
	info, err := n.mc.GetInfo(ctx)
	if err != nil {
		log.Printf("[node] WARNING: cannot get network info via consensus: %v", err)
		// Fall back to single peer
		if info2, err2 := n.singlePeer.GetInfo(ctx); err2 == nil {
			info = info2
			log.Printf("[node] Fallback to single peer: height=%d", info.Height)
		}
	}
	if info != nil {
		log.Printf("[node] Network height: %d, current hash: %s", info.Height, info.CurrentHash)
	}

	// Start syncer
	if n.syncer == nil && n.cfg.SyncEnabled {
		n.syncer = syncer.NewSyncer(n.db, n.mc, n.validator, n.cfg.SyncerConfig)
		n.syncer.SetCallbacks(n.onBlock, n.onSyncComplete)
	}

	if n.syncer != nil {
		go func() {
			if err := n.syncer.SyncToTip(ctx); err != nil {
				if !errors.Is(err, context.Canceled) {
					log.Printf("[node] Sync error: %v", err)
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
		// No peers file yet — first run
		return n.bootstrap(ctx)
	}

	log.Printf("[node] Loaded %d peers from %s", n.peerStore.Len(),
		filepath.Join(n.cfg.DataDir, "peers.json"))
	return nil
}

// bootstrap connects to a bootstrap peer to discover other peers.
func (n *Node) bootstrap(ctx context.Context) error {
	bootstrapURL := n.cfg.Bootstrap
	if bootstrapURL == "" {
		// Fall back to the legacy peer URL as bootstrap
		bootstrapURL = n.cfg.PeerURL
	}

	if bootstrapURL == "" {
		return fmt.Errorf("no bootstrap peer configured (use --bootstrap)")
	}

	log.Printf("[node] First run: bootstrapping from %s", bootstrapURL)

	// Add bootstrap as a regular peer
	n.peerStore.Add(bootstrapURL)
	n.peerStore.RecordSuccess(bootstrapURL)

	// Fetch peer list from bootstrap
	bootstrapClient := client.NewHTTPClient(bootstrapURL, n.cfg.HTTPTimeout)
	peerURLs, err := bootstrapClient.GetPeers(ctx)
	if err != nil {
		log.Printf("[node] Failed to get peers from bootstrap: %v", err)
		// Still save the bootstrap peer alone
		return n.peerStore.Save()
	}

	log.Printf("[node] Bootstrap returned %d peers", len(peerURLs))

	// Merge into store
	added := n.peerStore.AddPeers(peerURLs)
	log.Printf("[node] Added %d new peers (total: %d)", added, n.peerStore.Len())

	// Bootstrap is now just a regular peer — no special handling
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
			log.Printf("[node] Refreshing peer list...")
			newURLs, err := n.mc.GetPeersFromAll(ctx)
			if err != nil {
				log.Printf("[node] Peer refresh failed: %v", err)
				continue
			}
			added := n.peerStore.AddPeers(newURLs)
			if added > 0 {
				log.Printf("[node] Peer refresh: added %d new peers (total: %d)", added, n.peerStore.Len())
				n.peerStore.Save()
			} else {
				log.Printf("[node] Peer refresh: no new peers found")
			}
		}
	}
}

func (n *Node) printPeers() {
	all := n.peerStore.GetAll()
	log.Printf("[node] ---- Peer List (%d) ----", len(all))
	for i, p := range all {
		log.Printf("  %2d. %s  score=%d  success=%d  fail=%d  last=%s",
			i+1, p.URL, p.Score, p.SuccessCount, p.FailCount,
			p.LastConnected.Format("2006-01-02 15:04:05"))
	}
	log.Printf("[node] -------------------------")
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

	log.Printf("[node] Shutting down...")

	if n.syncer != nil {
		n.syncer.Cancel()
	}

	// Save peer state
	if err := n.peerStore.Save(); err != nil {
		log.Printf("[node] Failed to save peers: %v", err)
	}

	if err := n.db.Close(); err != nil {
		return fmt.Errorf("close db: %w", err)
	}

	close(n.eventCh)
	log.Printf("[node] Node stopped")
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

// GetNetworkInfo returns info from the peer network.
func (n *Node) GetNetworkInfo(ctx context.Context) (*types.ChainInfo, error) {
	info, err := n.mc.GetInfo(ctx)
	if err != nil {
		// Fall back to single peer
		return n.singlePeer.GetInfo(ctx)
	}
	return info, nil
}

// GetSyncStatus returns the current sync status.
func (n *Node) GetSyncStatus() types.SyncStatus {
	if n.syncer != nil {
		return n.syncer.Status()
	}
	return types.SyncStatus{}
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

	info, _ := n.db.GetChainInfo()
	height := uint64(0)
	if info != nil {
		height = info.Height
	}

	return map[string]interface{}{
		"running":      n.running,
		"uptime":       time.Since(n.startTime).String(),
		"height":       height,
		"blocks_seen":  n.blocksSeen,
		"peer_count":   n.peerStore.Len(),
		"peer":         n.cfg.PeerURL,
		"min_consensus": n.cfg.MinConsensus,
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
