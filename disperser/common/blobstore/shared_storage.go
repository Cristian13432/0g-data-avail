package blobstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/0glabs/0g-data-avail/common"
	"github.com/0glabs/0g-data-avail/common/aws/s3"
	"github.com/0glabs/0g-data-avail/core"
	"github.com/0glabs/0g-data-avail/disperser"
	"github.com/gammazero/workerpool"
)

const (
	maxS3BlobFetchWorkers = 64
)

// The shared blob store that the disperser is operating on.
// The metadata store is backed by DynamoDB and the blob store is backed by S3.
//
// Note:
//   - For each entry in the store (i.e. an S3 object), the user has to ensure there is no
//     concurrent writers
//
// The blobs are identified by blobKey, which is hash(blob), where blob contains the content
// of the blob (bytes).
//
// The same blob (sameness determined by blobKey) at different requests are processed as different
// blobs in disperser. This is distinguished via requestAt, the timestamp (in ns) at which the
// request arrives, as well as security parameters.
// The blob object is reused for different requests in blobstore.
//
// This store tracks the blob, the state of the blob and the index (to facilitate retrieval).
//
// The blobs stored in S3 are key'd by the blob key and the metadata stored in DynamoDB.
// See blob_metadata_store.go for more details on BlobMetadataStore.
type SharedBlobStore struct {
	bucketName            string
	s3Client              *s3.Client
	blobMetadataStore     *BlobMetadataStore
	metadataHashAsBlobKey bool
	logger                common.Logger
}

type Config struct {
	BucketName            string
	TableName             string
	MetadataHashAsBlobKey bool
	InMemory              bool
	MemoryDBSize          uint64
}

// This represents the s3 fetch result for a blob.
type blobResultOrError struct {
	// Indicating if the s3 fetch succeeded.
	err error

	// The actual fetch results. Undefined if the err above isn't nil.
	blob              []byte
	blobKey           disperser.BlobKey
	blobRequestHeader core.BlobRequestHeader
}

var _ disperser.BlobStore = (*SharedBlobStore)(nil)

func NewSharedStorage(bucketName string, s3Client *s3.Client, MetadataHashAsBlobKey bool, blobMetadataStore *BlobMetadataStore, logger common.Logger) *SharedBlobStore {
	return &SharedBlobStore{
		bucketName:            bucketName,
		s3Client:              s3Client,
		blobMetadataStore:     blobMetadataStore,
		metadataHashAsBlobKey: MetadataHashAsBlobKey,
		logger:                logger,
	}
}

func (s *SharedBlobStore) MetadataHashAsBlobKey() bool {
	return s.metadataHashAsBlobKey
}

func (s *SharedBlobStore) RemoveBlob(ctx context.Context, metadata *disperser.BlobMetadata) error {
	err := s.s3Client.DeleteObject(ctx, s.bucketName, metadata.MetadataHash)
	if err != nil {
		return err
	}
	return s.blobMetadataStore.RemoveBlobMetadata(ctx, metadata)
}

func (s *SharedBlobStore) StoreBlob(ctx context.Context, blob *core.Blob, requestedAt uint64) (disperser.BlobKey, error) {
	metadataKey := disperser.BlobKey{}
	if blob == nil {
		return metadataKey, errors.New("blob is nil")
	}

	blobHash := getBlobHash(blob)
	metadataHash, err := getMetadataHash(requestedAt, blob.RequestHeader.SecurityParams)
	if err != nil {
		s.logger.Error("[sharedstorage] error creating metadata key", "err", err)
		return metadataKey, err
	}
	metadataKey.BlobHash = blobHash
	metadataKey.MetadataHash = metadataHash

	if s.metadataHashAsBlobKey {
		err = s.s3Client.UploadObject(ctx, s.bucketName, metadataHash, blob.Data)
	} else {
		err = s.s3Client.UploadObject(ctx, s.bucketName, blobObjectKey(blobHash), blob.Data)
	}
	if err != nil {
		s.logger.Error("[sharedstorage] error uploading blob", "err", err)
		return metadataKey, err
	}

	// don't expire if ttl is 0
	expiry := uint64(0)
	if s.blobMetadataStore.ttl > 0 {
		expiry = uint64(time.Now().Add(s.blobMetadataStore.ttl).Unix())
	}
	metadata := disperser.BlobMetadata{
		BlobHash:     blobHash,
		MetadataHash: metadataHash,
		NumRetries:   0,
		BlobStatus:   disperser.Processing,
		Expiry:       expiry,
		RequestMetadata: &disperser.RequestMetadata{
			BlobRequestHeader: blob.RequestHeader,
			BlobSize:          uint(len(blob.Data)),
			RequestedAt:       requestedAt,
		},
	}
	err = s.blobMetadataStore.QueueNewBlobMetadata(ctx, &metadata)
	if err != nil {
		s.logger.Error("[sharedstorage] error uploading blob metadata", "err", err)
		return metadataKey, err
	}

	return metadataKey, nil
}

// GetBlobContent retrieves blob content by the blob key.
func (s *SharedBlobStore) GetBlobContent(ctx context.Context, metadata *disperser.BlobMetadata) ([]byte, error) {
	if s.metadataHashAsBlobKey {
		return s.s3Client.DownloadObject(ctx, s.bucketName, metadata.MetadataHash)
	} else {
		return s.s3Client.DownloadObject(ctx, s.bucketName, blobObjectKey(metadata.BlobHash))
	}
}

func (s *SharedBlobStore) getBlobContentParallel(ctx context.Context, blobKey disperser.BlobKey, blobRequestHeader core.BlobRequestHeader, resultChan chan<- blobResultOrError) {
	var blob []byte
	var err error
	if s.metadataHashAsBlobKey {
		blob, err = s.s3Client.DownloadObject(ctx, s.bucketName, blobKey.MetadataHash)
	} else {
		blob, err = s.s3Client.DownloadObject(ctx, s.bucketName, blobObjectKey(blobKey.BlobHash))
	}
	if err != nil {
		resultChan <- blobResultOrError{err: err}
		return
	}
	resultChan <- blobResultOrError{blob: blob, blobKey: blobKey, blobRequestHeader: blobRequestHeader}
}

func (s *SharedBlobStore) MarkBlobConfirmed(ctx context.Context, existingMetadata *disperser.BlobMetadata, confirmationInfo *disperser.ConfirmationInfo) (*disperser.BlobMetadata, error) {
	newMetadata := *existingMetadata
	// Update the TTL if needed
	ttlFromNow := time.Now().Add(s.blobMetadataStore.ttl)
	if existingMetadata.Expiry < uint64(ttlFromNow.Unix()) {
		newMetadata.Expiry = uint64(ttlFromNow.Unix())
	}
	newMetadata.BlobStatus = disperser.Confirmed
	newMetadata.ConfirmationInfo = confirmationInfo
	return &newMetadata, s.blobMetadataStore.UpdateBlobMetadata(ctx, existingMetadata.GetBlobKey(), &newMetadata)
}

func (s *SharedBlobStore) MarkBlobFinalized(ctx context.Context, metadataKey disperser.BlobKey) error {
	return s.blobMetadataStore.SetBlobStatus(ctx, metadataKey, disperser.Finalized)
}

func (s *SharedBlobStore) MarkBlobProcessing(ctx context.Context, metadataKey disperser.BlobKey) error {
	return s.blobMetadataStore.SetBlobStatus(ctx, metadataKey, disperser.Processing)
}

func (s *SharedBlobStore) MarkBlobFailed(ctx context.Context, metadataKey disperser.BlobKey) error {
	return s.blobMetadataStore.SetBlobStatus(ctx, metadataKey, disperser.Failed)
}

func (s *SharedBlobStore) IncrementBlobRetryCount(ctx context.Context, existingMetadata *disperser.BlobMetadata) error {
	return s.blobMetadataStore.IncrementNumRetries(ctx, existingMetadata)
}

func (s *SharedBlobStore) GetBlobsByMetadata(ctx context.Context, metadata []*disperser.BlobMetadata) (map[disperser.BlobKey]*core.Blob, error) {
	pool := workerpool.New(maxS3BlobFetchWorkers)
	resultChan := make(chan blobResultOrError, len(metadata))

	blobs := make(map[disperser.BlobKey]*core.Blob, 0)

	for _, m := range metadata {
		mCopy := m // avoid capturing loop variable "m" directly by making a copy
		pool.Submit(func() {
			// Fetch blob content from S3
			s.getBlobContentParallel(ctx, mCopy.GetBlobKey(), mCopy.RequestMetadata.BlobRequestHeader, resultChan)
		})
	}

	pool.StopWait() // wait for pending tasks to complete
	close(resultChan)

	// Collect results from channel
	for result := range resultChan {
		if result.err != nil {
			return nil, result.err
		}
		blobs[result.blobKey] = &core.Blob{
			RequestHeader: result.blobRequestHeader,
			Data:          result.blob,
		}
	}

	return blobs, nil
}

func (s *SharedBlobStore) GetBlobMetadataByStatus(ctx context.Context, blobStatus disperser.BlobStatus) ([]*disperser.BlobMetadata, error) {
	return s.blobMetadataStore.GetBlobMetadataByStatus(ctx, blobStatus)
}

func (s *SharedBlobStore) GetMetadataInBatch(ctx context.Context, batchHeaderHash [32]byte, blobIndex uint32) (*disperser.BlobMetadata, error) {
	return s.blobMetadataStore.GetBlobMetadataInBatch(ctx, batchHeaderHash, blobIndex)
}

func (s *SharedBlobStore) GetAllBlobMetadataByBatch(ctx context.Context, batchHeaderHash [32]byte) ([]*disperser.BlobMetadata, error) {
	return s.blobMetadataStore.GetAllBlobMetadataByBatch(ctx, batchHeaderHash)
}

// GetMetadata returns a blob metadata given a metadata key
func (s *SharedBlobStore) GetBlobMetadata(ctx context.Context, metadataKey disperser.BlobKey) (*disperser.BlobMetadata, error) {
	return s.blobMetadataStore.GetBlobMetadata(ctx, metadataKey)
}

func (s *SharedBlobStore) HandleBlobFailure(ctx context.Context, metadata *disperser.BlobMetadata, maxRetry uint) error {
	if metadata.NumRetries < maxRetry {
		return s.IncrementBlobRetryCount(ctx, metadata)
	} else {
		return s.MarkBlobFailed(ctx, metadata.GetBlobKey())
	}
}

func getMetadataHash(requestedAt uint64, securityParams []*core.SecurityParam) (string, error) {
	var str string
	str = fmt.Sprintf("%d/", requestedAt)
	for _, param := range securityParams {
		appendStr := fmt.Sprintf("%d/%d/", param.QuorumID, param.AdversaryThreshold)
		// Append String incase of multiple securityParams
		str = str + appendStr
	}
	bytes := []byte(str)
	return hex.EncodeToString(sha256.New().Sum(bytes)), nil
}

func blobObjectKey(blobHash disperser.BlobHash) string {
	return fmt.Sprintf("blob/%s.json", blobHash)
}

func getBlobHash(blob *core.Blob) disperser.BlobHash {
	hasher := sha256.New()
	hasher.Write(blob.Data)
	hash := hasher.Sum(nil)
	return hex.EncodeToString(hash)
}
