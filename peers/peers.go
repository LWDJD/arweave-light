package peers

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/arweave-light/types"
)

const (
	// MaxPeers is the maximum number of peers to store.
	MaxPeers = 50

	// DefaultMinScore is the score assigned to a new peer.
	DefaultMinScore = 0

	// KickThreshold is the score below which a peer is removed.
	KickThreshold = -10

	// ScoreCorrect is added when a peer returns the consensus result.
	ScoreCorrect = 1

	// ScoreMismatch is subtracted when a peer returns a non-consensus result.
	ScoreMismatch = -2

	// ScoreTimeout is subtracted when a peer fails to respond in time.
	ScoreTimeout = -1
)

// Store manages the peers.json file and provides peer operations.
type Store struct {
	mu       sync.RWMutex
	path     string
	peers    types.PeerList
	urlIndex map[string]*types.Peer
}

// NewStore creates a new peer store backed by the given file path.
func NewStore(path string) *Store {
	return &Store{
		path:     path,
		peers:    make(types.PeerList, 0),
		urlIndex: make(map[string]*types.Peer),
	}
}

// Load reads peers from the JSON file. If the file does not exist it is not an error.
func (s *Store) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read peers file: %w", err)
	}

	var list types.PeerList
	if err := json.Unmarshal(data, &list); err != nil {
		return fmt.Errorf("parse peers file: %w", err)
	}

	// Rebuild index
	s.peers = make(types.PeerList, 0, len(list))
	s.urlIndex = make(map[string]*types.Peer, len(list))
	for _, p := range list {
		if p.URL == "" {
			continue
		}
		// Normalise: remove trailing slash
		p.URL = trimTrailingSlash(p.URL)
		if _, exists := s.urlIndex[p.URL]; exists {
			continue
		}
		if p.FirstSeen.IsZero() {
			p.FirstSeen = time.Now()
		}
		s.peers = append(s.peers, p)
		s.urlIndex[p.URL] = p
	}

	return nil
}

// Save writes the peer list to the JSON file.
func (s *Store) Save() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.saveLocked()
}

func (s *Store) saveLocked() error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create peers dir: %w", err)
	}

	data, err := json.MarshalIndent(s.peers, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal peers: %w", err)
	}
	if err := os.WriteFile(s.path, data, 0644); err != nil {
		return fmt.Errorf("write peers file: %w", err)
	}
	return nil
}

// Add adds a peer (or updates its URL if already present). Score is not
// overwritten for existing peers. Returns the peer and whether it was newly added.
func (s *Store) Add(url string) (*types.Peer, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	url = trimTrailingSlash(url)
	if url == "" {
		return nil, false
	}

	if p, ok := s.urlIndex[url]; ok {
		return p, false
	}

	now := time.Now()
	p := &types.Peer{
		URL:       url,
		Score:     DefaultMinScore,
		FirstSeen: now,
	}
	s.peers = append(s.peers, p)
	s.urlIndex[url] = p
	s.enforceLimitLocked()
	return p, true
}

// AddPeers merges a list of peer URLs into the store. Returns the number of
// genuinely new peers added (duplicates and empty strings are not counted).
func (s *Store) AddPeers(urls []string) int {
	added := 0
	for _, u := range urls {
		if _, isNew := s.Add(u); isNew {
			added++
		}
	}
	return added
}

// Remove deletes a peer by URL.
func (s *Store) Remove(url string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	url = trimTrailingSlash(url)
	if _, ok := s.urlIndex[url]; !ok {
		return false
	}

	delete(s.urlIndex, url)
	for i, p := range s.peers {
		if p.URL == url {
			s.peers = append(s.peers[:i], s.peers[i+1:]...)
			return true
		}
	}
	return false
}

// Get returns a peer by URL.
func (s *Store) Get(url string) *types.Peer {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.urlIndex[trimTrailingSlash(url)]
}

// GetAll returns a copy of all peers.
func (s *Store) GetAll() types.PeerList {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(types.PeerList, len(s.peers))
	copy(out, s.peers)
	return out
}

// Len returns the number of peers.
func (s *Store) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.peers)
}

// Top returns the N highest-scored peers.
func (s *Store) Top(n int) types.PeerList {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if n <= 0 || n > len(s.peers) {
		n = len(s.peers)
	}
	sorted := make(types.PeerList, len(s.peers))
	copy(sorted, s.peers)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Score > sorted[j].Score
	})
	return sorted[:n]
}

// UpdateScore adjusts a peer's score and records the outcome.
func (s *Store) UpdateScore(url string, delta int, success bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	url = trimTrailingSlash(url)
	p, ok := s.urlIndex[url]
	if !ok {
		return
	}

	p.Score += delta
	p.LastConnected = time.Now()
	if success {
		p.SuccessCount++
	} else {
		p.FailCount++
	}

	// Kick if below threshold
	if p.Score <= KickThreshold {
		delete(s.urlIndex, url)
		for i, peer := range s.peers {
			if peer.URL == url {
				s.peers = append(s.peers[:i], s.peers[i+1:]...)
				break
			}
		}
	}
}

// RecordSuccess increments score by ScoreCorrect.
func (s *Store) RecordSuccess(url string) {
	s.UpdateScore(url, ScoreCorrect, true)
}

// RecordMismatch decrements score by ScoreMismatch.
func (s *Store) RecordMismatch(url string) {
	s.UpdateScore(url, ScoreMismatch, false)
}

// RecordTimeout decrements score by ScoreTimeout.
func (s *Store) RecordTimeout(url string) {
	s.UpdateScore(url, ScoreTimeout, false)
}

// URLs returns a string slice of all peer URLs.
func (s *Store) URLs() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, len(s.peers))
	for i, p := range s.peers {
		out[i] = p.URL
	}
	return out
}

// enforceLimitLocked removes the lowest-scored peers until Len <= MaxPeers.
func (s *Store) enforceLimitLocked() {
	for len(s.peers) > MaxPeers {
		// Find lowest score
		worst := 0
		for i := 1; i < len(s.peers); i++ {
			if s.peers[i].Score < s.peers[worst].Score {
				worst = i
			}
		}
		url := s.peers[worst].URL
		delete(s.urlIndex, url)
		s.peers = append(s.peers[:worst], s.peers[worst+1:]...)
	}
}

func trimTrailingSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}
