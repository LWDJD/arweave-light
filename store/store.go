package store

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/arweave-light/types"
)

// errors
var (
	ErrNotFound    = errors.New("store: key not found")
	ErrNotOpen     = errors.New("store: database not open")
	ErrBlockExists = errors.New("store: block already exists")
)

// DB is a simple file-based key-value store.
type DB struct {
	mu       sync.RWMutex
	basePath string
	open     bool

	heightIndex map[uint64]types.Hash
	hashIndex   map[types.Hash]uint64
	txIndex     map[types.Hash]uint64
	chainInfo   *types.ChainInfo

	blocksFile *os.File
	txFile     *os.File
}

// Open opens or creates the database at the given path.
func Open(path string) (*DB, error) {
	if err := os.MkdirAll(path, 0755); err != nil {
		return nil, fmt.Errorf("create db dir: %w", err)
	}

	db := &DB{
		basePath:    path,
		heightIndex: make(map[uint64]types.Hash),
		hashIndex:   make(map[types.Hash]uint64),
		txIndex:     make(map[types.Hash]uint64),
	}

	// open blocks file
	blocksPath := filepath.Join(path, "blocks.jsonl")
	f, err := os.OpenFile(blocksPath, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0644)
	if err != nil {
		return nil, fmt.Errorf("open blocks file: %w", err)
	}
	db.blocksFile = f

	// open tx file
	txPath := filepath.Join(path, "transactions.jsonl")
	tf, err := os.OpenFile(txPath, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0644)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("open tx file: %w", err)
	}
	db.txFile = tf

	// load index
	if err := db.loadIndex(); err != nil {
		f.Close()
		tf.Close()
		return nil, fmt.Errorf("load index: %w", err)
	}

	db.open = true
	return db, nil
}

// loadIndex scans the blocks file and rebuilds the in-memory index.
func (db *DB) loadIndex() error {
	data, err := os.ReadFile(filepath.Join(db.basePath, "blocks.jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	lines := splitLines(string(data))
	for _, line := range lines {
		if line == "" {
			continue
		}
		var block types.Block
		if err := json.Unmarshal([]byte(line), &block); err != nil {
			continue
		}
		db.heightIndex[block.Height] = block.Hash
		db.hashIndex[block.Hash] = block.Height
		for _, txID := range block.Txs {
			db.txIndex[txID] = block.Height
		}
		db.chainInfo = &types.ChainInfo{
			Height:      block.Height,
			CurrentHash: block.Hash,
		}
	}
	return nil
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

// Close flushes and closes the database.
func (db *DB) Close() error {
	db.mu.Lock()
	defer db.mu.Unlock()
	db.open = false
	var errs []error
	if db.blocksFile != nil {
		if err := db.blocksFile.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if db.txFile != nil {
		if err := db.txFile.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("close errors: %v", errs)
	}
	return nil
}

// PutBlock stores a block and indexes its transactions.
func (db *DB) PutBlock(block *types.Block) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	if !db.open {
		return ErrNotOpen
	}

	if _, exists := db.heightIndex[block.Height]; exists {
		return ErrBlockExists
	}

	data, err := json.Marshal(block)
	if err != nil {
		return fmt.Errorf("marshal block: %w", err)
	}

	if _, err := db.blocksFile.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("write block: %w", err)
	}

	db.heightIndex[block.Height] = block.Hash
	db.hashIndex[block.Hash] = block.Height
	for _, txID := range block.Txs {
		db.txIndex[txID] = block.Height
	}

	if db.chainInfo == nil || block.Height > db.chainInfo.Height {
		db.chainInfo = &types.ChainInfo{
			Height:      block.Height,
			CurrentHash: block.Hash,
		}
	}

	return nil
}

// GetBlockByHeight retrieves a block by height.
func (db *DB) GetBlockByHeight(height uint64) (*types.Block, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	if !db.open {
		return nil, ErrNotOpen
	}

	hash, ok := db.heightIndex[height]
	if !ok {
		return nil, ErrNotFound
	}
	return db.getBlockByHashLocked(hash)
}

// GetBlockByHash retrieves a block by hash.
func (db *DB) GetBlockByHash(hash types.Hash) (*types.Block, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	if !db.open {
		return nil, ErrNotOpen
	}

	return db.getBlockByHashLocked(hash)
}

func (db *DB) getBlockByHashLocked(hash types.Hash) (*types.Block, error) {
	data, err := os.ReadFile(filepath.Join(db.basePath, "blocks.jsonl"))
	if err != nil {
		return nil, err
	}
	lines := splitLines(string(data))
	for _, line := range lines {
		if line == "" {
			continue
		}
		var b types.Block
		if err := json.Unmarshal([]byte(line), &b); err != nil {
			continue
		}
		if b.Hash == hash {
			return &b, nil
		}
	}
	return nil, ErrNotFound
}

// GetLatestBlock returns the highest block.
func (db *DB) GetLatestBlock() (*types.Block, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	if !db.open || db.chainInfo == nil {
		return nil, ErrNotFound
	}
	return db.GetBlockByHeight(db.chainInfo.Height)
}

// GetChainInfo returns the current chain state.
func (db *DB) GetChainInfo() (*types.ChainInfo, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	if !db.open {
		return nil, ErrNotOpen
	}
	if db.chainInfo == nil {
		return &types.ChainInfo{Height: 0}, nil
	}
	return db.chainInfo, nil
}

// GetTxLocation returns the block height containing the transaction.
func (db *DB) GetTxLocation(txID types.Hash) (uint64, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	if !db.open {
		return 0, ErrNotOpen
	}
	height, ok := db.txIndex[txID]
	if !ok {
		return 0, ErrNotFound
	}
	return height, nil
}

// DeleteBlocksAbove removes blocks above the given height (for rollback).
func (db *DB) DeleteBlocksAbove(height uint64) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	if !db.open {
		return ErrNotOpen
	}

	blocksPath := filepath.Join(db.basePath, "blocks.jsonl")
	data, err := os.ReadFile(blocksPath)
	if err != nil {
		return err
	}
	lines := splitLines(string(data))
	var kept []string
	for _, line := range lines {
		if line == "" {
			continue
		}
		var b types.Block
		if err := json.Unmarshal([]byte(line), &b); err != nil {
			continue
		}
		if b.Height <= height {
			kept = append(kept, line)
		}
	}

	// rewrite file
	f, err := os.Create(blocksPath)
	if err != nil {
		return err
	}
	defer f.Close()
	for _, line := range kept {
		if _, err := f.WriteString(line + "\n"); err != nil {
			return err
		}
	}

	// rebuild index
	db.heightIndex = make(map[uint64]types.Hash)
	db.hashIndex = make(map[types.Hash]uint64)
	db.txIndex = make(map[types.Hash]uint64)
	db.chainInfo = nil
	for _, line := range kept {
		var b types.Block
		if err := json.Unmarshal([]byte(line), &b); err != nil {
			continue
		}
		db.heightIndex[b.Height] = b.Hash
		db.hashIndex[b.Hash] = b.Height
		for _, txID := range b.Txs {
			db.txIndex[txID] = b.Height
		}
		if db.chainInfo == nil || b.Height > db.chainInfo.Height {
			db.chainInfo = &types.ChainInfo{
				Height:      b.Height,
				CurrentHash: b.Hash,
			}
		}
	}

	return nil
}

// PutTransaction stores a full transaction.
func (db *DB) PutTransaction(tx *types.Transaction) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	if !db.open {
		return ErrNotOpen
	}

	data, err := json.Marshal(tx)
	if err != nil {
		return fmt.Errorf("marshal tx: %w", err)
	}
	if _, err := db.txFile.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("write tx: %w", err)
	}
	return nil
}

// GetTransaction retrieves a transaction by its ID.
func (db *DB) GetTransaction(txID types.Hash) (*types.Transaction, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	if !db.open {
		return nil, ErrNotOpen
	}

	data, err := os.ReadFile(filepath.Join(db.basePath, "transactions.jsonl"))
	if err != nil {
		return nil, err
	}
	lines := splitLines(string(data))
	for _, line := range lines {
		if line == "" {
			continue
		}
		var tx types.Transaction
		if err := json.Unmarshal([]byte(line), &tx); err != nil {
			continue
		}
		if tx.ID == txID || tx.ComputedID == txID {
			return &tx, nil
		}
	}
	return nil, ErrNotFound
}

// Itob converts uint64 to big-endian bytes.
func Itob(v uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, v)
	return b
}

// Btoi converts big-endian bytes to uint64.
func Btoi(b []byte) uint64 {
	return binary.BigEndian.Uint64(b)
}
