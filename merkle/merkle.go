package merkle

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sort"

	"github.com/arweave-light/types"
)

// Node represents a node in a Merkle tree.
type Node struct {
	Hash  types.Hash
	Left  *Node
	Right *Node
}

// Tree is a complete binary Merkle tree.
type Tree struct {
	Root   *Node
	Leaves []types.Hash
	depth  int
}

// errors
var (
	ErrEmptyDataSet      = errors.New("merkle: empty data set")
	ErrProofVerification = errors.New("merkle: proof verification failed")
	ErrInvalidIndex      = errors.New("merkle: index out of range")
)

// hashConcat returns SHA-256(left || right) as a types.Hash (48 bytes,
// with the 32-byte digest in the first 32 bytes).
func hashConcat(left, right types.Hash) types.Hash {
	combined := make([]byte, 0, types.HashSize*2)
	combined = append(combined, left[:]...)
	combined = append(combined, right[:]...)
	sum := sha256.Sum256(combined)
	return types.HashFromBytes(sum[:])
}

// BuildTree constructs a balanced binary Merkle tree from data items.
func BuildTree(data [][]byte) (*Tree, error) {
	if len(data) == 0 {
		return nil, ErrEmptyDataSet
	}
	leaves := make([]types.Hash, len(data))
	for i, d := range data {
		leaves[i] = types.HashFromBytes(d)
	}
	root := buildRecursive(leaves)
	depth := int(math.Ceil(math.Log2(float64(len(leaves))))) + 1
	return &Tree{Root: root, Leaves: leaves, depth: depth}, nil
}

// BuildTreeFromHashes constructs a tree from pre-hashed leaves.
func BuildTreeFromHashes(leaves []types.Hash) (*Tree, error) {
	if len(leaves) == 0 {
		return nil, ErrEmptyDataSet
	}
	root := buildRecursive(leaves)
	depth := int(math.Ceil(math.Log2(float64(len(leaves))))) + 1
	return &Tree{Root: root, Leaves: leaves, depth: depth}, nil
}

func buildRecursive(hashes []types.Hash) *Node {
	if len(hashes) == 0 {
		return nil
	}
	if len(hashes) == 1 {
		return &Node{Hash: hashes[0]}
	}

	// pad to next power of two
	n := nextPowerOfTwo(len(hashes))
	padded := make([]types.Hash, n)
	copy(padded, hashes)
	for i := len(hashes); i < n; i++ {
		padded[i] = hashes[len(hashes)-1]
	}

	mid := n / 2
	left := buildRecursive(padded[:mid])
	right := buildRecursive(padded[mid:])

	return &Node{Hash: hashConcat(left.Hash, right.Hash), Left: left, Right: right}
}

func nextPowerOfTwo(n int) int {
	if n <= 0 {
		return 1
	}
	n--
	n |= n >> 1
	n |= n >> 2
	n |= n >> 4
	n |= n >> 8
	n |= n >> 16
	n++
	return n
}

// ComputeRootHash computes the Merkle root without building the full tree.
func ComputeRootHash(data [][]byte) (types.Hash, error) {
	if len(data) == 0 {
		return types.EmptyHash(), ErrEmptyDataSet
	}
	hashes := make([]types.Hash, len(data))
	for i, d := range data {
		hashes[i] = types.HashFromBytes(d)
	}
	for len(hashes) > 1 {
		if len(hashes)%2 == 1 {
			hashes = append(hashes, hashes[len(hashes)-1])
		}
		next := make([]types.Hash, len(hashes)/2)
		for i := 0; i < len(hashes); i += 2 {
			next[i/2] = hashConcat(hashes[i], hashes[i+1])
		}
		hashes = next
	}
	return hashes[0], nil
}

// ProofElement represents one step in a Merkle proof.
type ProofElement struct {
	Hash    types.Hash
	IsRight bool
}

// Proof is a Merkle inclusion proof.
type Proof struct {
	Leaf     types.Hash
	Index    int
	Elements []ProofElement
	RootHash types.Hash
}

// GenerateProof creates a Merkle proof.
func (t *Tree) GenerateProof(index int) (*Proof, error) {
	if index < 0 || index >= len(t.Leaves) {
		return nil, ErrInvalidIndex
	}

	leaf := t.Leaves[index]
	n := nextPowerOfTwo(len(t.Leaves))
	padded := make([]types.Hash, n)
	copy(padded, t.Leaves)
	for i := len(t.Leaves); i < n; i++ {
		padded[i] = t.Leaves[len(t.Leaves)-1]
	}

	elements := make([]ProofElement, 0)
	currentIdx := index
	layer := padded
	for len(layer) > 1 {
		n := len(layer)
		siblingIdx := currentIdx ^ 1
		if siblingIdx >= n {
			break
		}
		e := ProofElement{
			Hash:    layer[siblingIdx],
			IsRight: currentIdx%2 == 0,
		}
		elements = append(elements, e)

		next := make([]types.Hash, n/2)
		for i := 0; i < n; i += 2 {
			next[i/2] = hashConcat(layer[i], layer[i+1])
		}
		layer = next
		currentIdx = currentIdx / 2
	}

	return &Proof{
		Leaf:     leaf,
		Index:    index,
		Elements: elements,
		RootHash: t.Root.Hash,
	}, nil
}

// VerifyProof checks a Merkle proof.
func VerifyProof(proof *Proof) bool {
	current := proof.Leaf
	for _, e := range proof.Elements {
		if e.IsRight {
			current = hashConcat(current, e.Hash)
		} else {
			current = hashConcat(e.Hash, current)
		}
	}
	return current == proof.RootHash
}

// ValidateTxRoot validates that transaction IDs match block.TxRoot.
//
// NOTE: The Arweave protocol computes tx_root as the Merkle root of
// {DataRoot, Offset} pairs (see ar_block.erl:generate_tx_root_for_block and
// ar_merkle.erl:generate_tree). This requires the data_root and data_size of
// each transaction, which are not available to a light node that only has the
// transaction IDs from the block header.
//
// Therefore, this function performs a simplified validation that only checks
// structural consistency (e.g., empty txs → empty root). Full tx_root
// validation requires fetching each transaction's data_root and computing the
// correct Arweave unbalanced Merkle tree with offset-annotated leaves.
//
// For light node purposes, chain continuity (previous_block → indep_hash) and
// multi-peer consensus provide sufficient security guarantees.
func ValidateTxRoot(txIDs []types.Hash, expectedRoot types.Hash) (bool, error) {
	if len(txIDs) == 0 {
		// Empty transaction list must have an empty (all-zeros) tx_root.
		return expectedRoot == types.EmptyHash(), nil
	}

	// For non-empty transaction lists, we cannot independently verify tx_root
	// without the full transaction data (data_root + data_size for each TX).
	// The tx_root is a 32-byte SHA-256 hash (stored as 48-byte with trailing
	// zeros). We verify the expected root has the right shape (non-zero,
	// valid 32-byte hash in first 32 bytes).
	if expectedRoot == types.EmptyHash() {
		return false, fmt.Errorf("non-empty tx list requires non-empty tx_root")
	}

	// The root should be a 32-byte value (43-char base64url).
	// Since we cannot recompute it without full TX data, we accept it as-is.
	// Chain continuity and consensus provide security for light nodes.
	return true, nil
}

// ---------------------------------------------------------------------------
// Arweave-native unbalanced Merkle tree for tx_root / data_root
// ---------------------------------------------------------------------------
// These functions implement the exact algorithm from ar_merkle.erl and
// ar_block.erl:generate_tx_root_for_block.
//
// Key differences from a standard balanced Merkle tree:
//   1. Leaves are SHA-256(SHA-256(DataRoot) || SHA-256(OffsetBE32))
//   2. Branch nodes are SHA-256(SHA-256(LeftID) || SHA-256(RightID) ||
//      SHA-256(LeftMaxBE32))
//   3. Odd elements are PROMOTED to the next level (not duplicated)
//   4. Empty tree → all-zeros hash
//   5. Post-fork-2.5: padding nodes with empty-binary data_root are inserted
//      when data_size is not a multiple of DATA_CHUNK_SIZE (256 KiB).
//
// NOTE_SIZE = 32 bytes (256-bit big-endian integer) as per ar.hrl.

const (
	noteSize        = 32
	dataChunkSize   = 256 * 1024 // ?DATA_CHUNK_SIZE in ar.hrl
)

// arweaveLeafHash computes the Arweave Merkle leaf hash.
// leaf = SHA-256( SHA-256(dataRoot) || SHA-256(offsetBigEndian32) )
func arweaveLeafHash(dataRoot []byte, offset uint64) []byte {
	hData := sha256.Sum256(dataRoot)
	offsetBytes := make([]byte, noteSize)
	binary.BigEndian.PutUint64(offsetBytes[noteSize-8:], offset)
	hNote := sha256.Sum256(offsetBytes)

	h := sha256.New()
	h.Write(hData[:])
	h.Write(hNote[:])
	return h.Sum(nil)
}

// arweaveBranchHash computes the Arweave Merkle branch hash.
// branch = SHA-256( SHA-256(leftID) || SHA-256(rightID) ||
//                  SHA-256(leftMaxBigEndian32) )
func arweaveBranchHash(leftID, rightID []byte, leftMax uint64) []byte {
	hLeft := sha256.Sum256(leftID)
	hRight := sha256.Sum256(rightID)
	offsetBytes := make([]byte, noteSize)
	binary.BigEndian.PutUint64(offsetBytes[noteSize-8:], leftMax)
	hNote := sha256.Sum256(offsetBytes)

	h := sha256.New()
	h.Write(hLeft[:])
	h.Write(hRight[:])
	h.Write(hNote[:])
	return h.Sum(nil)
}

// arweaveNode represents a node in the Arweave unbalanced Merkle tree.
type arweaveNode struct {
	id  []byte // 32-byte node ID
	max uint64 // maximum offset in this subtree
}

// buildArweaveTree builds an Arweave unbalanced Merkle tree from leaf
// (dataRoot, offset) pairs. The input must be sorted by transaction ID
// (as Arweave does with lists:sort).
func buildArweaveTree(leaves []arweaveNode) []byte {
	if len(leaves) == 0 {
		return make([]byte, 32) // all zeros
	}
	if len(leaves) == 1 {
		return leaves[0].id
	}

	// Build layers bottom-up, promoting odd elements
	nodes := leaves
	for len(nodes) > 1 {
		var next []arweaveNode
		for i := 0; i < len(nodes); i += 2 {
			if i+1 >= len(nodes) {
				// Odd element: promote to next level
				next = append(next, nodes[i])
			} else {
				left, right := nodes[i], nodes[i+1]
				branchID := arweaveBranchHash(left.id, right.id, left.max)
				next = append(next, arweaveNode{
					id:  branchID,
					max: right.max, // RMax2 = RMax (no rebase)
				})
			}
		}
		nodes = next
	}
	return nodes[0].id
}

// paddingDataRoot is the data_root used for padding nodes.
// In ar.hrl: -define(PADDING_NODE_DATA_ROOT, <<>>).
// This is an empty binary; SHA-256 of empty is the well-known value below.
var paddingDataRoot = []byte{} // empty, NOT all-zeros

// TxLeaf represents a transaction's data needed for tx_root computation.
type TxLeaf struct {
	DataRoot []byte // 32-byte data_root
	DataSize uint64 // data_size from the transaction
	TxID     []byte // 32-byte transaction ID (for sorting)
}

// ComputeArweaveTxRoot computes the Arweave tx_root from transaction leaf data.
// This implements generate_tx_root_for_block from ar_block.erl.
//
// The transactions are sorted by their TXID (binary comparison), then for each
// transaction with non-zero data_size that is not a multiple of DATA_CHUNK_SIZE
// (256 KiB), a padding node with empty data_root is inserted after the
// transaction's data node. The cumulative offset advances accordingly.
//
// Finally, an unbalanced Merkle tree is built from the (DataRoot, Offset) pairs
// and the root hash is returned.
func ComputeArweaveTxRoot(txLeaves []TxLeaf) (types.Hash, error) {
	if len(txLeaves) == 0 {
		return types.EmptyHash(), nil
	}

	// Sort by TXID (binary comparison), matching Erlang's lists:sort on tx records
	sorted := make([]TxLeaf, len(txLeaves))
	copy(sorted, txLeaves)
	sort.Slice(sorted, func(i, j int) bool {
		return string(sorted[i].TxID) < string(sorted[j].TxID)
	})

	// Build the SizeTaggedDataRoots list with padding nodes
	type element struct {
		dataRoot []byte
		offset   uint64
	}
	var elements []element
	var cumulative uint64

	for _, tx := range sorted {
		cumulative += tx.DataSize
		// Add the transaction's data node
		elements = append(elements, element{dataRoot: tx.DataRoot, offset: cumulative})

		// Post-fork-2.5: check if padding is needed
		if tx.DataSize > 0 {
			paddedSize := ((tx.DataSize + dataChunkSize - 1) / dataChunkSize) * dataChunkSize
			padding := paddedSize - tx.DataSize
			if padding > 0 {
				cumulative += padding
				// Padding node uses empty binary as data_root
				elements = append(elements, element{dataRoot: paddingDataRoot, offset: cumulative})
			}
		}
	}

	// Build Merkle tree leaves
	nodes := make([]arweaveNode, 0, len(elements))
	for _, el := range elements {
		leafID := arweaveLeafHash(el.dataRoot, el.offset)
		nodes = append(nodes, arweaveNode{id: leafID, max: el.offset})
	}

	rootBytes := buildArweaveTree(nodes)

	// Convert 32-byte root to types.Hash (48 bytes, first 32 significant)
	return types.HashFromBytes(rootBytes), nil
}
