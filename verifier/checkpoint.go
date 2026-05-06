package verifier

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// TrustedCheckpoint represents a verified block checkpoint.
type TrustedCheckpoint struct {
	Height        uint64 `json:"height"`
	IndepHash     string `json:"indep_hash"`
	Timestamp     int64  `json:"timestamp"`
	VerifiedCount uint64 `json:"verified_count"`
}

// CheckpointStore manages persistent checkpoint storage.
type CheckpointStore struct {
	path string
}

// NewCheckpointStore creates a new checkpoint store.
func NewCheckpointStore(dataDir string) *CheckpointStore {
	return &CheckpointStore{
		path: filepath.Join(dataDir, "checkpoint.json"),
	}
}

// Load reads the checkpoint from disk.
func (cs *CheckpointStore) Load() (*TrustedCheckpoint, error) {
	data, err := os.ReadFile(cs.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read checkpoint: %w", err)
	}
	var cp TrustedCheckpoint
	if err := json.Unmarshal(data, &cp); err != nil {
		return nil, fmt.Errorf("parse checkpoint: %w", err)
	}
	return &cp, nil
}

// Save atomically writes the checkpoint to disk.
func (cs *CheckpointStore) Save(cp *TrustedCheckpoint) error {
	data, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal checkpoint: %w", err)
	}
	tmpPath := cs.path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return fmt.Errorf("write temp checkpoint: %w", err)
	}
	if err := os.Rename(tmpPath, cs.path); err != nil {
		return fmt.Errorf("atomic rename checkpoint: %w", err)
	}
	return nil
}

// Remove deletes the checkpoint file.
func (cs *CheckpointStore) Remove() error {
	if err := os.Remove(cs.path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("remove checkpoint: %w", err)
	}
	return nil
}

// Exists checks if a checkpoint file exists.
func (cs *CheckpointStore) Exists() bool {
	_, err := os.Stat(cs.path)
	return err == nil
}

// IsRecent checks if the checkpoint is within threshold blocks of the given height.
func (cp *TrustedCheckpoint) IsRecent(networkHeight uint64, threshold uint64) bool {
	if cp == nil {
		return false
	}
	if networkHeight < cp.Height {
		return false
	}
	return (networkHeight - cp.Height) <= threshold
}

// Age returns how many blocks behind the network the checkpoint is.
func (cp *TrustedCheckpoint) Age(networkHeight uint64) uint64 {
	if cp == nil || networkHeight < cp.Height {
		return 0
	}
	return networkHeight - cp.Height
}

// IsFresh checks if the checkpoint was created within the given duration.
func (cp *TrustedCheckpoint) IsFresh(maxAge time.Duration) bool {
	if cp == nil {
		return false
	}
	return time.Since(time.Unix(cp.Timestamp, 0)) <= maxAge
}
