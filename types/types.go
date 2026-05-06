package types

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"time"
)

// HashSize is the Arweave native hash size in bytes (see ar.hrl: -define(HASH_SIZE, 48)).
const HashSize = 48

// Hash is a 48-byte hash used throughout Arweave for block hashes,
// indep_hash, tx_root, wallet_list, etc.
//
// Transaction IDs are SHA-256 (32 bytes). To accommodate both, the Hash
// type uses a 48-byte backing array. For 32-byte values the trailing
// 16 bytes are zero. Base64() / MarshalJSON() automatically encode
// only the significant bytes (43 chars for 32-byte TXIDs, 64 chars
// for full 48-byte block hashes).
type Hash [HashSize]byte

// EmptyHash returns a zero-value hash.
func EmptyHash() Hash {
	return Hash{}
}

// HashFromBytes creates a Hash from a byte slice.
//   - 48 bytes      → copied directly
//   - 32 bytes      → placed in first 32 bytes, last 16 zeroed
//   - anything else → SHA-256 hashed (32 bytes → stored in first 32)
func HashFromBytes(data []byte) Hash {
	var h Hash
	switch len(data) {
	case HashSize:
		copy(h[:], data)
	case 32:
		copy(h[:32], data)
	default:
		sum := sha256.Sum256(data)
		copy(h[:32], sum[:])
	}
	return h
}

// HashFromBytes48 creates a 48-byte Hash, copying data directly.
// Panics if len(data) != 48.
func HashFromBytes48(data []byte) Hash {
	if len(data) != HashSize {
		panic(fmt.Sprintf("HashFromBytes48: expected 48 bytes, got %d", len(data)))
	}
	var h Hash
	copy(h[:], data)
	return h
}

// Base64 returns the URL-safe base64 encoding (no padding).
// It encodes only the significant bytes: 32 bytes for TXIDs
// (trailing 16 bytes zero) and 48 bytes for block hashes.
func (h Hash) Base64() string {
	if h.is32() {
		// Transaction ID or other SHA-256 value.
		return base64.RawURLEncoding.EncodeToString(h[:32])
	}
	return base64.RawURLEncoding.EncodeToString(h[:])
}

// String returns the base64 representation.
func (h Hash) String() string {
	return h.Base64()
}

// is32 reports whether the hash is a 32-byte value (last 16 bytes are all zero).
func (h Hash) is32() bool {
	for i := 32; i < HashSize; i++ {
		if h[i] != 0 {
			return false
		}
	}
	return true
}

// MarshalJSON implements json.Marshaler.
func (h Hash) MarshalJSON() ([]byte, error) {
	return json.Marshal(h.Base64())
}

// UnmarshalJSON implements json.Unmarshaler.
// Accepts both 32-byte (43-char base64url) and 48-byte (64-char base64url)
// encoded strings. An empty string is treated as the zero hash.
func (h *Hash) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	// Empty string → zero hash
	if s == "" {
		*h = EmptyHash()
		return nil
	}
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return fmt.Errorf("decode base64url hash: %w", err)
	}
	switch len(b) {
	case HashSize:
		copy(h[:], b)
	case 32:
		copy(h[:32], b)
		// last 16 bytes remain zero (already zeroed by default)
	default:
		return fmt.Errorf("invalid hash length: %d (expected 32 or %d)", len(b), HashSize)
	}
	return nil
}

// Tag is a name-value metadata pair.
type Tag struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Transaction represents an Arweave transaction.
type Transaction struct {
	ID        Hash   `json:"id"`
	LastTx    Hash   `json:"last_tx"`
	Owner     string `json:"owner"`
	Target    string `json:"target"`
	Quantity  string `json:"quantity"`
	Data      string `json:"data"`
	DataSize  string `json:"data_size"`
	DataRoot  Hash   `json:"data_root"`
	Reward    string `json:"reward"`
	Signature string `json:"signature"`
	Tags      []Tag  `json:"tags"`

	// Extended local fields
	BlockHeight uint64 `json:"block_height,omitempty"`
	BlockID     Hash   `json:"block_id,omitempty"`
	DataContent []byte `json:"-"`
	ComputedID  Hash   `json:"-"`
}

// ComputeID calculates TXID = SHA256(signature).
func (tx *Transaction) ComputeID() error {
	sigBytes, err := base64.RawURLEncoding.DecodeString(tx.Signature)
	if err != nil {
		return fmt.Errorf("decode signature: %w", err)
	}
	sum := sha256.Sum256(sigBytes)
	var h Hash
	copy(h[:32], sum[:])
	tx.ComputedID = h
	return nil
}

// Block represents a lightweight Arweave block.
type Block struct {
	Nonce          string `json:"nonce"`
	PreviousBlock  Hash   `json:"previous_block"`
	Timestamp      int64  `json:"timestamp"`
	LastRetarget   int64  `json:"last_retarget"`
	Diff           string `json:"diff"`
	Height         uint64 `json:"height"`
	Hash           Hash   `json:"hash"`
	IndepHash      Hash   `json:"indep_hash"`
	Txs            []Hash `json:"txs"`
	TxRoot         Hash   `json:"tx_root"`
	WalletList     Hash   `json:"wallet_list"`
	RewardAddr     string `json:"reward_addr"`
	Tags           []Tag  `json:"tags"`
	RewardPool     string `json:"reward_pool"`
	WeaveSize      string `json:"weave_size"`
	BlockSize      string `json:"block_size"`
	CumulativeDiff string `json:"cumulative_diff"`
	HashListMerkle Hash   `json:"hash_list_merkle"`
}

// BlockHeader is a minimal block header.
type BlockHeader struct {
	Height         uint64 `json:"height"`
	Hash           Hash   `json:"hash"`
	PreviousBlock  Hash   `json:"previous_block"`
	Timestamp      int64  `json:"timestamp"`
	Diff           string `json:"diff"`
	CumulativeDiff string `json:"cumulative_diff"`
}

// Header extracts a BlockHeader from a Block.
func (b *Block) Header() BlockHeader {
	return BlockHeader{
		Height:         b.Height,
		Hash:           b.Hash,
		PreviousBlock:  b.PreviousBlock,
		Timestamp:      b.Timestamp,
		Diff:           b.Diff,
		CumulativeDiff: b.CumulativeDiff,
	}
}

// BigIntFromString parses a decimal string into *big.Int.
func BigIntFromString(s string) (*big.Int, error) {
	n := new(big.Int)
	_, ok := n.SetString(s, 10)
	if !ok {
		return nil, fmt.Errorf("invalid big.Int string: %s", s)
	}
	return n, nil
}

// ChainInfo holds summary information.
type ChainInfo struct {
	Height      uint64 `json:"height"`
	CurrentHash Hash   `json:"current_hash"`
}

// PeerInfo represents a known Arweave peer (legacy).
type PeerInfo struct {
	URL     string `json:"url"`
	Height  uint64 `json:"height"`
	Latency int64  `json:"latency_ms"`
}

// SyncStatus reports current sync state.
type SyncStatus struct {
	Syncing       bool   `json:"syncing"`
	CurrentHeight uint64 `json:"current_height"`
	TargetHeight  uint64 `json:"target_height"`
	BlocksBehind  uint64 `json:"blocks_behind"`
}

// ---------------------------------------------------------------------------
// Peer management types
// ---------------------------------------------------------------------------

// Peer represents a known Arweave node with scoring metadata.
type Peer struct {
	URL           string    `json:"url"`
	Score         int       `json:"score"`
	LastConnected time.Time `json:"last_connected"`
	SuccessCount  int       `json:"success_count"`
	FailCount     int       `json:"fail_count"`
	FirstSeen     time.Time `json:"first_seen"`
}

// PeerList is a collection of peers.
type PeerList []*Peer

// ConsensusResult holds the outcome of a multi-peer query.
type ConsensusResult struct {
	Block      *Block
	AgreedURLs []string // peers that returned the consensus result
	TotalURLs  []string // all peers queried
	Agreed     int      // number agreeing
	Total      int      // number that responded
	Consensus  bool     // whether min-consensus was reached
}
