package types

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"time"
)

// Hash is a 32-byte SHA-256 hash used throughout Arweave.
type Hash [32]byte

// EmptyHash returns a zero-value hash.
func EmptyHash() Hash {
	return Hash{}
}

// HashFromBytes creates a Hash from a byte slice.
func HashFromBytes(data []byte) Hash {
	if len(data) == 32 {
		var h Hash
		copy(h[:], data)
		return h
	}
	return sha256.Sum256(data)
}

// Base64 returns the URL-safe base64 encoding (no padding).
func (h Hash) Base64() string {
	return base64.RawURLEncoding.EncodeToString(h[:])
}

// String returns the base64 representation.
func (h Hash) String() string {
	return h.Base64()
}

// MarshalJSON implements json.Marshaler.
func (h Hash) MarshalJSON() ([]byte, error) {
	return json.Marshal(h.Base64())
}

// UnmarshalJSON implements json.Unmarshaler.
func (h *Hash) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return err
	}
	if len(b) != 32 {
		return fmt.Errorf("invalid hash length: %d", len(b))
	}
	copy(h[:], b)
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
	tx.ComputedID = sha256.Sum256(sigBytes)
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
