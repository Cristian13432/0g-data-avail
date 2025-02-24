package core

import (
	"bytes"
	"encoding/gob"
	"errors"
	"fmt"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/wealdtech/go-merkletree"
	"github.com/wealdtech/go-merkletree/keccak256"
	"golang.org/x/crypto/sha3"
)

var ErrInvalidCommitment = errors.New("invalid commitment")

// SetBatchRoot sets the BatchRoot field of the BatchHeader to the Merkle root of the blob headers in the batch (i.e. the root of the Merkle tree whose leaves are the blob headers)
func (h *BatchHeader) SetBatchRoot(blobHeaders []*BlobHeader) (*merkletree.MerkleTree, error) {
	leafs := make([][]byte, len(blobHeaders))
	for i, header := range blobHeaders {
		leaf, err := header.GetBlobHeaderHash()
		if err != nil {
			return nil, fmt.Errorf("failed to compute blob header hash: %w", err)
		}
		leafs[i] = leaf[:]
	}

	tree, err := merkletree.NewTree(merkletree.WithData(leafs), merkletree.WithHashType(keccak256.New()))
	if err != nil {
		return nil, err
	}

	copy(h.BatchRoot[:], tree.Root())
	return tree, nil
}

func (h *BatchHeader) Encode() ([]byte, error) {
	// The order here has to match the field ordering of ReducedBatchHeader defined in IZGDAServiceManager.sol
	// ref: https://github.com/0glabs/0g-data-avail/blob/master/contracts/src/interfaces/IZGDAServiceManager.sol#L43
	batchHeaderType, err := abi.NewType("tuple", "", []abi.ArgumentMarshaling{
		{
			Name: "blobHeadersRoot",
			Type: "bytes32",
		},
		{
			Name: "referenceBlockNumber",
			Type: "uint32",
		},
	})
	if err != nil {
		return nil, err
	}

	arguments := abi.Arguments{
		{
			Type: batchHeaderType,
		},
	}

	s := struct {
		BlobHeadersRoot      [32]byte
		ReferenceBlockNumber uint32
	}{
		BlobHeadersRoot:      h.BatchRoot,
		ReferenceBlockNumber: 0,
	}

	bytes, err := arguments.Pack(s)
	if err != nil {
		return nil, err
	}

	return bytes, nil
}

// GetBatchHeaderHash returns the hash of the reduced BatchHeader that is used to sign the Batch
// ref: https://github.com/0glabs/0g-data-avail/blob/master/contracts/src/libraries/ZGDAHasher.sol#L65
func (h BatchHeader) GetBatchHeaderHash() ([32]byte, error) {
	headerByte, err := h.Encode()
	if err != nil {
		return [32]byte{}, err
	}

	var headerHash [32]byte
	hasher := sha3.NewLegacyKeccak256()
	hasher.Write(headerByte)
	copy(headerHash[:], hasher.Sum(nil)[:32])

	return headerHash, nil
}

func (h *BlobHeader) SetCommitmentRoot(commitments []Commitment) error {
	leafs := make([][]byte, len(commitments))
	for i, commitment := range commitments {
		leaf := GetCommitmentHash(commitment)
		leafs[i] = leaf[:]
	}

	tree, err := merkletree.NewTree(merkletree.WithData(leafs), merkletree.WithHashType(keccak256.New()))
	if err != nil {
		return err
	}

	h.CommitmentRoot = tree.Root()
	return nil
}

func GetCommitmentHash(commitment Commitment) [32]byte {
	var commitmentHash [32]byte
	hasher := sha3.NewLegacyKeccak256()
	hasher.Write(commitment[:])
	copy(commitmentHash[:], hasher.Sum(nil)[:32])

	return commitmentHash
}

// GetBlobHeaderHash returns the hash of the BlobHeader that is used to sign the Blob
func (h BlobHeader) GetBlobHeaderHash() ([32]byte, error) {
	headerByte, err := h.Encode()
	if err != nil {
		return [32]byte{}, err
	}

	var headerHash [32]byte
	hasher := sha3.NewLegacyKeccak256()
	hasher.Write(headerByte)
	copy(headerHash[:], hasher.Sum(nil)[:32])

	return headerHash, nil
}

func (h *BlobHeader) GetQuorumBlobParamsHash() ([32]byte, error) {
	quorumBlobParamsType, err := abi.NewType("tuple[]", "", []abi.ArgumentMarshaling{
		{
			Name: "quorumNumber",
			Type: "uint8",
		},
		{
			Name: "adversaryThresholdPercentage",
			Type: "uint8",
		},
		{
			Name: "quorumThresholdPercentage",
			Type: "uint8",
		},
		{
			Name: "quantizationParameter",
			Type: "uint8",
		},
	})

	if err != nil {
		return [32]byte{}, err
	}

	arguments := abi.Arguments{
		{
			Type: quorumBlobParamsType,
		},
	}

	type quorumBlobParams struct {
		QuorumNumber                 uint8
		AdversaryThresholdPercentage uint8
		QuorumThresholdPercentage    uint8
		QuantizationParameter        uint8
	}

	qbp := make([]quorumBlobParams, 0)

	bytes, err := arguments.Pack(qbp)
	if err != nil {
		return [32]byte{}, err
	}

	var res [32]byte
	hasher := sha3.NewLegacyKeccak256()
	hasher.Write(bytes)
	copy(res[:], hasher.Sum(nil)[:32])

	return res, nil
}

func (h *BlobHeader) Encode() ([]byte, error) {
	if h.CommitmentRoot == nil {
		return nil, ErrInvalidCommitment
	}

	return h.CommitmentRoot, nil
}

func (h *BatchHeader) Serialize() ([]byte, error) {
	return Encode(h)
}

func (h *BatchHeader) Deserialize(data []byte) (*BatchHeader, error) {
	err := Decode(data, h)
	return h, err
}

func (h *BlobHeader) Serialize() ([]byte, error) {
	return Encode(h)
}

func (h *BlobHeader) Deserialize(data []byte) (*BlobHeader, error) {
	err := Decode(data, h)
	return h, err
}

func Encode(obj any) ([]byte, error) {
	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)
	err := enc.Encode(obj)
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func Decode(data []byte, obj any) error {
	buf := bytes.NewBuffer(data)
	dec := gob.NewDecoder(buf)
	err := dec.Decode(obj)
	if err != nil {
		return err
	}
	return nil
}
