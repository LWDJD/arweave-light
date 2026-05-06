package merkle

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"math"

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
func ValidateTxRoot(txIDs []types.Hash, expectedRoot types.Hash) (bool, error) {
	if len(txIDs) == 0 {
		return expectedRoot == types.EmptyHash(), nil
	}
	data := make([][]byte, len(txIDs))
	for i, id := range txIDs {
		data[i] = id[:]
	}
	computed, err := ComputeRootHash(data)
	if err != nil {
		return false, fmt.Errorf("compute tx root: %w", err)
	}
	return computed == expectedRoot, nil
}
