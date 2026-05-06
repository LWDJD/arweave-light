package validator

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"

	"github.com/arweave-light/merkle"
	"github.com/arweave-light/types"
	"golang.org/x/crypto/blake2b"
)

// errors
var (
	ErrInvalidSignature     = errors.New("validator: invalid RSA signature")
	ErrInvalidBlockHash     = errors.New("validator: block hash does not match")
	ErrInvalidTxRoot        = errors.New("validator: transaction root mismatch")
	ErrInvalidDifficulty    = errors.New("validator: insufficient difficulty")
	ErrBlockTooOld          = errors.New("validator: block too old")
	ErrInvalidPreviousBlock = errors.New("validator: previous block hash mismatch")
)

// Validator checks block and transaction validity.
type Validator struct {
	keyCache map[string]*rsa.PublicKey
}

// NewValidator creates a new Validator.
func NewValidator() *Validator {
	return &Validator{
		keyCache: make(map[string]*rsa.PublicKey),
	}
}

// ValidateBlock performs full block validation.
func (v *Validator) ValidateBlock(block *types.Block, prevBlock *types.Block) error {
	// 1. Verify block hash
	if err := v.validateBlockHash(block); err != nil {
		return err
	}

	// 2. Check previous block hash
	if prevBlock != nil {
		if block.PreviousBlock != prevBlock.Hash {
			return fmt.Errorf("%w: expected %s, got %s",
				ErrInvalidPreviousBlock, prevBlock.Hash, block.PreviousBlock)
		}
		if block.Height != prevBlock.Height+1 {
			return fmt.Errorf("validator: invalid height: %d, expected %d",
				block.Height, prevBlock.Height+1)
		}
	}

	// 3. Verify tx_root
	if err := v.validateTxRoot(block); err != nil {
		return err
	}

	// 4. Verify difficulty is a valid number
	if _, err := types.BigIntFromString(block.Diff.String()); err != nil {
		return fmt.Errorf("invalid difficulty: %w", err)
	}

	return nil
}

// validateBlockHash verifies block.Hash matches computed hash.
func (v *Validator) validateBlockHash(block *types.Block) error {
	computed := v.computeBlockHash(block)
	if computed != block.Hash {
		return fmt.Errorf("%w: computed %s, got %s",
			ErrInvalidBlockHash, computed, block.Hash)
	}
	return nil
}

// computeBlockHash computes the block hash per Arweave spec.
func (v *Validator) computeBlockHash(block *types.Block) types.Hash {
	hasher := sha256.New()

	write := func(s string) {
		hasher.Write([]byte(s))
	}

	write(block.Nonce)
	write(block.PreviousBlock.Base64())
	write(fmt.Sprintf("%d", block.Timestamp))
	write(fmt.Sprintf("%d", block.LastRetarget))
	write(block.Diff.String())
	write(fmt.Sprintf("%d", block.Height))
	write(block.HashListMerkle.Base64())
	write(block.WalletList.Base64())
	write(block.RewardAddr)
	for _, tag := range block.Tags {
		hasher.Write([]byte(tag.Name))
		hasher.Write([]byte(tag.Value))
	}

	var h types.Hash
	copy(h[:], hasher.Sum(nil))
	return h
}

// validateTxRoot checks the Merkle root of transaction IDs.
func (v *Validator) validateTxRoot(block *types.Block) error {
	if len(block.Txs) == 0 && block.TxRoot == types.EmptyHash() {
		return nil
	}
	ok, err := merkle.ValidateTxRoot(block.Txs, block.TxRoot)
	if err != nil {
		return fmt.Errorf("tx_root validation: %w", err)
	}
	if !ok {
		return ErrInvalidTxRoot
	}
	return nil
}

// ValidateTransaction checks a transaction's signature and structural validity.
func (v *Validator) ValidateTransaction(tx *types.Transaction) error {
	// 1. Decode owner (RSA modulus)
	ownerBytes, err := base64.RawURLEncoding.DecodeString(tx.Owner)
	if err != nil {
		return fmt.Errorf("decode owner: %w", err)
	}

	// 2. Parse RSA public key
	pubKey, err := v.parsePublicKey(ownerBytes)
	if err != nil {
		return fmt.Errorf("parse public key: %w", err)
	}

	// 3. Decode signature
	sigBytes, err := base64.RawURLEncoding.DecodeString(tx.Signature)
	if err != nil {
		return fmt.Errorf("decode signature: %w", err)
	}

	// 4. Verify PKCS1v15 signature
	msg := v.buildSigningMessage(tx)
	msgHash := sha256.Sum256(msg)

	err = rsa.VerifyPKCS1v15(pubKey, crypto.SHA256, msgHash[:], sigBytes)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidSignature, err)
	}

	// 5. Verify TXID = SHA256(signature)
	if err := tx.ComputeID(); err != nil {
		return err
	}
	if tx.ComputedID != tx.ID {
		return fmt.Errorf("validator: computed TXID %s != declared %s",
			tx.ComputedID, tx.ID)
	}

	return nil
}

// parsePublicKey parses an RSA public key from DER bytes.
func (v *Validator) parsePublicKey(derBytes []byte) (*rsa.PublicKey, error) {
	cacheKey := string(derBytes)
	if k, ok := v.keyCache[cacheKey]; ok {
		return k, nil
	}

	// Try PKCS1 first (common in Arweave)
	pub, err := x509.ParsePKCS1PublicKey(derBytes)
	if err == nil {
		v.keyCache[cacheKey] = pub
		return pub, nil
	}

	// Try PKIX format
	pub2, err2 := x509.ParsePKIXPublicKey(derBytes)
	if err2 != nil {
		return nil, fmt.Errorf("parse pubkey (PKCS1: %v, PKIX: %v)", err, err2)
	}
	rsaKey, ok := pub2.(*rsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("not an RSA key")
	}
	v.keyCache[cacheKey] = rsaKey
	return rsaKey, nil
}

// buildSigningMessage builds the message that was signed.
func (v *Validator) buildSigningMessage(tx *types.Transaction) []byte {
	h := sha256.New()
	write := func(s string) {
		h.Write([]byte(s))
	}
	write(tx.Owner)
	write(tx.Target)
	write(tx.Data)
	write(tx.Quantity)
	write(tx.Reward)
	write(tx.LastTx.Base64())
	for _, tag := range tx.Tags {
		h.Write([]byte(tag.Name))
		h.Write([]byte(tag.Value))
	}
	return h.Sum(nil)
}

// VerifyDataRoot verifies that data matches the data_root hash.
func VerifyDataRoot(data []byte, expected types.Hash) bool {
	if len(data) == 0 {
		return expected == types.EmptyHash()
	}
	sum := sha256.Sum256(data)
	computed := types.HashFromBytes(sum[:])
	return computed == expected
}

// Blake2bHash computes a BLAKE2b-256 hash.
func Blake2bHash(data []byte) ([]byte, error) {
	h, err := blake2b.New256(nil)
	if err != nil {
		return nil, err
	}
	h.Write(data)
	return h.Sum(nil), nil
}

// ValidateDifficulty checks if a block hash satisfies the required difficulty.
func ValidateDifficulty(blockHash types.Hash, diff *big.Int) bool {
	hashBig := new(big.Int).SetBytes(blockHash[:])
	return hashBig.Cmp(diff) <= 0
}

// DeepHash computes a deep hash (Arweave v2 style).
func DeepHash(data interface{}) ([]byte, error) {
	h, _ := blake2b.New256(nil)
	switch v := data.(type) {
	case []byte:
		tag := []byte("blob")
		h.Write(encodeUvarint(uint64(len(tag))))
		h.Write(tag)
		h.Write(encodeUvarint(uint64(len(v))))
		h.Write(v)
	case [][]byte:
		for _, item := range v {
			itemHash, err := DeepHash(item)
			if err != nil {
				return nil, err
			}
			h.Write(itemHash)
		}
	default:
		return nil, fmt.Errorf("unsupported deep hash type: %T", v)
	}
	return h.Sum(nil), nil
}

func encodeUvarint(x uint64) []byte {
	buf := make([]byte, 0, 10)
	for x >= 0x80 {
		buf = append(buf, byte(x)|0x80)
		x >>= 7
	}
	buf = append(buf, byte(x))
	return buf
}
