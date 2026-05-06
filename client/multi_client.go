package client

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/arweave-light/peers"
	"github.com/arweave-light/types"
)

// MultiClient queries multiple Arweave nodes and uses consensus voting.
type MultiClient struct {
	peerStore    *peers.Store
	minConsensus int
	timeout      time.Duration
	queryCount   int // how many peers to query per request

	mu        sync.Mutex
	clients   map[string]*HTTPClient // URL → client cache
}

// NewMultiClient creates a MultiClient backed by a peer store.
func NewMultiClient(ps *peers.Store, minConsensus int, timeout time.Duration) *MultiClient {
	if minConsensus < 1 {
		minConsensus = 1
	}
	return &MultiClient{
		peerStore:    ps,
		minConsensus: minConsensus,
		timeout:      timeout,
		queryCount:   max(minConsensus*2, 5),
		clients:      make(map[string]*HTTPClient),
	}
}

// SetMinConsensus updates the consensus threshold.
func (mc *MultiClient) SetMinConsensus(n int) {
	if n < 1 {
		n = 1
	}
	mc.minConsensus = n
	mc.queryCount = max(n*2, 5)
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

	// Collect results, group by (height, hash)
	type voteKey struct {
		height uint64
		hash   string
	}
	votes := make(map[voteKey][]string)
	var allURLs []string

	for i := 0; i < len(peers); i++ {
		r := <-results
		allURLs = append(allURLs, r.url)
		if r.err != nil {
			log.Printf("[multiclient] Peer %s error: %v", r.url, r.err)
			mc.peerStore.RecordTimeout(r.url)
			continue
		}
		key := voteKey{height: r.info.Height, hash: r.info.CurrentHash.Base64()}
		votes[key] = append(votes[key], r.url)
	}

	// Find a key with >= minConsensus votes
	for key, urls := range votes {
		if len(urls) >= mc.minConsensus {
			// Reward agreeing peers
			for _, u := range urls {
				mc.peerStore.RecordSuccess(u)
			}
			// Penalize disagreeing peers
			for _, u := range allURLs {
				found := false
				for _, vu := range urls {
					if vu == u {
						found = true
						break
					}
				}
				if !found {
					mc.peerStore.RecordMismatch(u)
				}
			}

			return &types.ChainInfo{
				Height:      key.height,
				CurrentHash: types.HashFromBytes([]byte(key.hash)[:32]), // placeholder
			}, nil
		}
	}

	return nil, fmt.Errorf("no consensus: got %d results, need %d", len(votes), mc.minConsensus)
}

// GetBlockByHeight fetches a block using consensus from multiple peers.
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

	// Group by block hash
	type voteGroup struct {
		block *types.Block
		urls  []string
	}
	votes := make(map[string]*voteGroup) // hash → group
	var allResponded []string
	var allQueried []string
	for _, p := range peers {
		allQueried = append(allQueried, p.URL)
	}

	for i := 0; i < len(peers); i++ {
		r := <-results
		if r.err != nil {
			log.Printf("[multiclient] Peer %s error on block %d: %v", r.url, height, r.err)
			mc.peerStore.RecordTimeout(r.url)
			continue
		}
		allResponded = append(allResponded, r.url)
		hashKey := r.block.Hash.Base64()
		if g, ok := votes[hashKey]; ok {
			g.urls = append(g.urls, r.url)
		} else {
			votes[hashKey] = &voteGroup{block: r.block, urls: []string{r.url}}
		}
	}

	// Find best consensus
	var best *voteGroup
	for _, g := range votes {
		if best == nil || len(g.urls) > len(best.urls) {
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
	cr.Consensus = cr.Agreed >= mc.minConsensus

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
		return cr, fmt.Errorf("block %d: consensus not reached (%d/%d agree, need %d)",
			height, cr.Agreed, cr.Total, mc.minConsensus)
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
				log.Printf("[multiclient] Failed to get peers from %s: %v", url, err)
				mc.peerStore.RecordTimeout(url)
				return
			}
			mu.Lock()
			for _, u := range list {
				allURLs[trimTrailingSlash(u)] = true
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

func trimTrailingSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
