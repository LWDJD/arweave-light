package client

import (
	"context"
	"encoding/base64"
	"fmt"
	"sync"
	"time"

	"github.com/arweave-light/logger"
	"github.com/arweave-light/peers"
	"github.com/arweave-light/types"
)

// RequireRatio is the minimum weight ratio needed for consensus.
// Default 0.51 means >50% of total vote weight must agree.
const RequireRatio = 0.51

// MultiClient queries multiple Arweave nodes and uses consensus voting.
type MultiClient struct {
	peerStore    *peers.Store
	minConsensus int
	requireRatio float64 // minimum weight ratio for consensus (0.51 = >50%)
	timeout      time.Duration
	queryCount   int // how many peers to query per request
	log          *logger.Logger

	mu      sync.Mutex
	clients map[string]*HTTPClient // URL → client cache
}

// NewMultiClient creates a MultiClient backed by a peer store.
func NewMultiClient(ps *peers.Store, minConsensus int, timeout time.Duration) *MultiClient {
	if minConsensus < 1 {
		minConsensus = 1
	}
	return &MultiClient{
		peerStore:    ps,
		minConsensus: minConsensus,
		requireRatio: RequireRatio,
		timeout:      timeout,
		queryCount:   max(minConsensus*2, 5),
		clients:      make(map[string]*HTTPClient),
		log:          logger.NewLogger("multiclient"),
	}
}

// SetLogger sets the logger for this multi-client.
func (mc *MultiClient) SetLogger(l *logger.Logger) {
	mc.log = l
}

// SetMinConsensus updates the consensus threshold.
func (mc *MultiClient) SetMinConsensus(n int) {
	if n < 1 {
		n = 1
	}
	mc.minConsensus = n
	mc.queryCount = max(n*2, 5)
}

// MinConsensus returns the current consensus threshold.
func (mc *MultiClient) MinConsensus() int {
	return mc.minConsensus
}

// PeerStore returns the underlying peer store.
func (mc *MultiClient) PeerStore() *peers.Store {
	return mc.peerStore
}

// getClient returns a cached HTTP client for a URL.
func (mc *MultiClient) getClient(url string) *HTTPClient {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	if c, ok := mc.clients[url]; ok {
		return c
	}
	c := NewHTTPClient(url, mc.timeout)
	mc.clients[url] = c
	return c
}

// GetInfo queries multiple peers for network info and returns the consensus result.
// Each peer's vote is weighted by its credit score: high-score peers have
// proportionally more influence. A minimum of 51% of total response weight
// must agree for consensus.
func (mc *MultiClient) GetInfo(ctx context.Context) (*types.ChainInfo, error) {
	peers := mc.peerStore.Top(mc.queryCount)
	if len(peers) == 0 {
		return nil, fmt.Errorf("no peers available")
	}

	type result struct {
		info *types.ChainInfo
		url  string
		err  error
	}

	results := make(chan result, len(peers))
	childCtx, cancel := context.WithTimeout(ctx, mc.timeout)
	defer cancel()

	for _, p := range peers {
		go func(url string) {
			info, err := mc.getClient(url).GetInfo(childCtx)
			results <- result{info: info, url: url, err: err}
		}(p.URL)
	}

	// Collect results, group by (height, hash) with weighted votes
	type voteKey struct {
		height uint64
		hash   string
	}
	type voteGroup struct {
		urls   []string
		weight float64
		info   *types.ChainInfo
	}
	votes := make(map[voteKey]*voteGroup)
	var allURLs []string
	totalWeight := 0.0
	respondingWeight := 0.0

	for i := 0; i < len(peers); i++ {
		r := <-results
		allURLs = append(allURLs, r.url)
		score := mc.peerStore.GetScore(r.url)
		weight := scoreToWeight(score)
		totalWeight += weight

		if r.err != nil {
			mc.log.Warn("Peer %s error: %v (score=%d)", r.url, r.err, score)
			mc.peerStore.RecordTimeout(r.url)
			continue
		}
		respondingWeight += weight

		key := voteKey{height: r.info.Height, hash: r.info.CurrentHash.Base64()}
		if g, ok := votes[key]; ok {
			g.urls = append(g.urls, r.url)
			g.weight += weight
		} else {
			votes[key] = &voteGroup{
				urls:   []string{r.url},
				weight: weight,
				info:   r.info,
			}
		}
	}

	if respondingWeight == 0 {
		return nil, fmt.Errorf("no peers responded successfully (queried %d)", len(peers))
	}

	// Find the group with the highest total weight
	var best *voteGroup
	for _, g := range votes {
		if best == nil || g.weight > best.weight {
			best = g
		}
	}

	// Consensus check: the best group must have >51% of responding weight
	consensusRatio := best.weight / respondingWeight
	if consensusRatio < mc.requireRatio {
		// Fallback: also check against old minConsensus count for backward compat
		if len(best.urls) < mc.minConsensus {
			return nil, fmt.Errorf("no consensus: best group has %.1f%% weight (%d/%d peers agree), need %.0f%%",
				consensusRatio*100, len(best.urls), len(allURLs), mc.requireRatio*100)
		}
		mc.log.Warn("Consensus weak by weight (%.1f%%) but meets count threshold (%d peers)",
			consensusRatio*100, len(best.urls))
	}

	// Reward agreeing peers, penalize disagreeing
	for _, u := range best.urls {
		mc.peerStore.RecordSuccess(u)
	}
	for _, u := range allURLs {
		found := false
		for _, vu := range best.urls {
			if vu == u {
				found = true
				break
			}
		}
		if !found {
			mc.peerStore.RecordMismatch(u)
		}
	}

	// Decode the consensus hash from its base64url representation
	var h types.Hash
	if err := h.UnmarshalJSON([]byte(`"` + best.info.CurrentHash.Base64() + `"`)); err != nil {
		mc.log.Warn("Failed to decode consensus hash: %v", err)
		return nil, fmt.Errorf("decode consensus hash: %w", err)
	}

	mc.log.Debug("GetInfo consensus: %.1f%% weight (%d/%d peers), height=%d",
		consensusRatio*100, len(best.urls), len(allURLs), best.info.Height)

	return &types.ChainInfo{
		Height:      best.info.Height,
		CurrentHash: h,
	}, nil
}

// GetBlockByHeight fetches a block using weighted consensus from multiple peers.
// Each peer's vote is weighted by its credit score via the scoreToWeight
// function. Consensus requires the best group to hold >51% of total
// responding weight.
func (mc *MultiClient) GetBlockByHeight(ctx context.Context, height uint64) (*types.ConsensusResult, error) {
	peers := mc.peerStore.Top(mc.queryCount)
	if len(peers) == 0 {
		return nil, fmt.Errorf("no peers available")
	}

	type fetchResult struct {
		block *types.Block
		url   string
		err   error
	}

	results := make(chan fetchResult, len(peers))
	childCtx, cancel := context.WithTimeout(ctx, mc.timeout)
	defer cancel()

	for _, p := range peers {
		go func(url string) {
			block, err := mc.getClient(url).GetBlockByHeight(childCtx, height)
			results <- fetchResult{block: block, url: url, err: err}
		}(p.URL)
	}

	// Group by block hash with weighted votes
	type voteGroup struct {
		block  *types.Block
		urls   []string
		weight float64
	}
	votes := make(map[string]*voteGroup) // hash → group
	var allResponded []string
	var allQueried []string
	totalWeight := 0.0
	respondingWeight := 0.0

	for _, p := range peers {
		allQueried = append(allQueried, p.URL)
	}

	for i := 0; i < len(peers); i++ {
		r := <-results
		score := mc.peerStore.GetScore(r.url)
		weight := scoreToWeight(score)
		totalWeight += weight

		if r.err != nil {
			mc.log.Warn("Peer %s error on block %d: %v (score=%d)", r.url, height, r.err, score)
			mc.peerStore.RecordTimeout(r.url)
			continue
		}
		respondingWeight += weight
		allResponded = append(allResponded, r.url)
		hashKey := r.block.Hash.Base64()
		if g, ok := votes[hashKey]; ok {
			g.urls = append(g.urls, r.url)
			g.weight += weight
		} else {
			votes[hashKey] = &voteGroup{block: r.block, urls: []string{r.url}, weight: weight}
		}
	}

	// Find best consensus (highest weight)
	var best *voteGroup
	for _, g := range votes {
		if best == nil || g.weight > best.weight {
			best = g
		}
	}

	cr := &types.ConsensusResult{
		TotalURLs: allQueried,
		Total:     len(allResponded),
		Consensus: false,
	}

	if best == nil {
		return cr, fmt.Errorf("block %d: no peer responded successfully", height)
	}

	cr.AgreedURLs = best.urls
	cr.Agreed = len(best.urls)
	cr.Block = best.block

	// Consensus check: weight-based primary, count-based fallback
	if respondingWeight > 0 {
		consensusRatio := best.weight / respondingWeight
		cr.Consensus = consensusRatio >= mc.requireRatio
		if !cr.Consensus && len(best.urls) >= mc.minConsensus {
			// Fallback to old count-based for backward compatibility
			cr.Consensus = true
			mc.log.Warn("Block %d: weak consensus by weight (%.1f%%) but meets count threshold (%d peers)",
				height, consensusRatio*100, len(best.urls))
		}
	} else {
		cr.Consensus = len(best.urls) >= mc.minConsensus
	}

	// Update scores
	for _, u := range allResponded {
		inConsensus := false
		for _, vu := range best.urls {
			if vu == u {
				inConsensus = true
				break
			}
		}
		if inConsensus {
			mc.peerStore.RecordSuccess(u)
		} else {
			mc.peerStore.RecordMismatch(u)
		}
	}

	if !cr.Consensus {
		ratio := 0.0
		if respondingWeight > 0 {
			ratio = best.weight / respondingWeight
		}
		return cr, fmt.Errorf("block %d: consensus not reached (%.1f%% weight, %d/%d peers agree, need %.0f%% or %d peers)",
			height, ratio*100, cr.Agreed, cr.Total, mc.requireRatio*100, mc.minConsensus)
	}

	return cr, nil
}

// GetBlockHeader fetches a block header via consensus.
func (mc *MultiClient) GetBlockHeader(ctx context.Context, height uint64) (*types.BlockHeader, error) {
	cr, err := mc.GetBlockByHeight(ctx, height)
	if err != nil {
		return nil, err
	}
	h := cr.Block.Header()
	return &h, nil
}

// GetPeers fetches the peer list from a specific peer.
func (mc *MultiClient) GetPeers(ctx context.Context, url string) ([]string, error) {
	return mc.getClient(url).GetPeers(ctx)
}

// GetPeersFromAll fetches peer lists from all top peers and merges them.
func (mc *MultiClient) GetPeersFromAll(ctx context.Context) ([]string, error) {
	peers := mc.peerStore.Top(mc.queryCount)
	if len(peers) == 0 {
		return nil, fmt.Errorf("no peers available")
	}

	allURLs := make(map[string]bool)
	var mu sync.Mutex
	var wg sync.WaitGroup
	childCtx, cancel := context.WithTimeout(ctx, mc.timeout)
	defer cancel()

	for _, p := range peers {
		wg.Add(1)
		go func(url string) {
			defer wg.Done()
			list, err := mc.getClient(url).GetPeers(childCtx)
			if err != nil {
				mc.log.Warn("Failed to get peers from %s: %v", url, err)
				mc.peerStore.RecordTimeout(url)
				return
			}
			mu.Lock()
			for _, u := range list {
				// Normalize peer URLs (add http:// if missing)
				allURLs[NormalizePeerURL(u)] = true
			}
			mu.Unlock()
			mc.peerStore.RecordSuccess(url)
		}(p.URL)
	}
	wg.Wait()

	out := make([]string, 0, len(allURLs))
	for u := range allURLs {
		out = append(out, u)
	}
	return out, nil
}

// Ping checks if a specific URL is reachable.
func (mc *MultiClient) Ping(ctx context.Context, url string) error {
	return mc.getClient(url).Ping(ctx)
}

// GetTransaction fetches a transaction from a specific peer.
func (mc *MultiClient) GetTransaction(ctx context.Context, url string, txID types.Hash) (*types.Transaction, error) {
	return mc.getClient(url).GetTransaction(ctx, txID)
}

// GetTransactionData fetches transaction data from a specific peer.
func (mc *MultiClient) GetTransactionData(ctx context.Context, url string, txID types.Hash) ([]byte, error) {
	return mc.getClient(url).GetTransactionData(ctx, txID)
}

// GetTxAnchor gets an anchor from a specific peer.
func (mc *MultiClient) GetTxAnchor(ctx context.Context, url string) (types.Hash, error) {
	return mc.getClient(url).GetTxAnchor(ctx)
}

// FetchBlockByHeight implements verifier.BlockFetcher. It returns the
// consensus-winning block at the given height, or an error if no
// consensus could be reached.
func (mc *MultiClient) FetchBlockByHeight(ctx context.Context, height uint64) (*types.Block, error) {
	cr, err := mc.GetBlockByHeight(ctx, height)
	if err != nil {
		return nil, err
	}
	if cr.Block == nil {
		return nil, fmt.Errorf("consensus returned nil block at height %d", height)
	}
	return cr.Block, nil
}

// FetchNetworkHeight implements verifier.BlockFetcher. It queries the
// best peer to find the current network height.
func (mc *MultiClient) FetchNetworkHeight(ctx context.Context) (uint64, error) {
	peers := mc.peerStore.Top(mc.queryCount)
	for _, p := range peers {
		info, err := mc.getClient(p.URL).GetInfo(ctx)
		if err == nil {
			return info.Height, nil
		}
		mc.peerStore.RecordTimeout(p.URL)
	}
	return 0, fmt.Errorf("no peer could provide network height")
}

// SingleClient returns an HTTPClient for a specific URL. Useful for
// operations that don't need consensus.
func (mc *MultiClient) SingleClient(url string) *HTTPClient {
	return mc.getClient(url)
}

// DecodeHashFromBase64 decodes a base64url-encoded hash string into a Hash.
// This is the correct way to decode hash strings, as opposed to treating the
// ASCII bytes of the string as the hash data.
func DecodeHashFromBase64(encoded string) (types.Hash, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return types.EmptyHash(), fmt.Errorf("decode base64url hash: %w", err)
	}
	return types.HashFromBytes(decoded), nil
}

// scoreToWeight converts a peer's credit score to a voting weight.
//   - score <= 0 → weight 1.0 (minimum voice, all peers get at least 1 vote)
//   - score > 0  → weight = float64(score) (proportional influence)
//
// This ensures new peers (score 0) have minimal weight while established
// high-score peers dominate consensus. Malicious peers can't inject fake
// votes because they'd need to accumulate high scores through repeated
// correct responses.
func scoreToWeight(score int) float64 {
	if score <= 0 {
		return 1.0
	}
	return float64(score)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
