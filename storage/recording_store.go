// Package storage provides backend-agnostic recording upload implementations
// and voice session state persistence.
package storage

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// RecordingStore is the interface for recording upload backends.
type RecordingStore interface {
	// Upload stores a chunk of recording data. Returns the storage path.
	Upload(ctx context.Context, key string, data []byte) (string, error)
	// Delete removes a recording from storage.
	Delete(ctx context.Context, key string) error
	// GetURL returns a download URL for a recording.
	GetURL(ctx context.Context, key string) (string, error)
	// Name returns the backend name.
	Name() string
}

// LocalStore implements RecordingStore using the local filesystem.
type LocalStore struct {
	basePath string
	// mu protects directory creation.
	mu sync.Mutex
}

// NewLocalStore creates a local filesystem recording store.
func NewLocalStore(basePath string) (*LocalStore, error) {
	if err := os.MkdirAll(basePath, 0750); err != nil {
		return nil, fmt.Errorf("local store: create dir %s: %w", basePath, err)
	}
	return &LocalStore{basePath: basePath}, nil
}

// Upload writes data to a file on the local filesystem.
func (ls *LocalStore) Upload(ctx context.Context, key string, data []byte) (string, error) {
	path := filepath.Join(ls.basePath, key)

	ls.mu.Lock()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0750); err != nil {
		ls.mu.Unlock()
		return "", fmt.Errorf("local store: create dir: %w", err)
	}
	ls.mu.Unlock()

	if err := os.WriteFile(path, data, 0640); err != nil {
		return "", fmt.Errorf("local store: write file: %w", err)
	}

	return path, nil
}

// Delete removes a file from the local filesystem.
func (ls *LocalStore) Delete(ctx context.Context, key string) error {
	path := filepath.Join(ls.basePath, key)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("local store: delete: %w", err)
	}
	return nil
}

// GetURL returns the local file path as the URL.
func (ls *LocalStore) GetURL(ctx context.Context, key string) (string, error) {
	path := filepath.Join(ls.basePath, key)
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("local store: file not found: %w", err)
	}
	return "file://" + path, nil
}

// Name returns "local".
func (ls *LocalStore) Name() string { return "local" }

// S3Store implements RecordingStore using Amazon S3.
type S3Store struct {
	client *s3.Client
	bucket string
	region string
}

// NewS3Store creates an S3 recording store.
func NewS3Store(ctx context.Context, bucket, region string) (*S3Store, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("s3 store: load config: %w", err)
	}

	client := s3.NewFromConfig(cfg)
	return &S3Store{
		client: client,
		bucket: bucket,
		region: region,
	}, nil
}

// Upload puts an object into S3.
func (ss *S3Store) Upload(ctx context.Context, key string, data []byte) (string, error) {
	_, err := ss.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(ss.bucket),
		Key:    aws.String(key),
		Body:   io.NopCloser(bytes.NewReader(data)),
	})
	if err != nil {
		return "", fmt.Errorf("s3 store: put object: %w", err)
	}

	return fmt.Sprintf("s3://%s/%s", ss.bucket, key), nil
}

// Delete removes an object from S3.
func (ss *S3Store) Delete(ctx context.Context, key string) error {
	_, err := ss.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(ss.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("s3 store: delete object: %w", err)
	}
	return nil
}

// GetURL returns a pre-signed URL or direct S3 URL.
func (ss *S3Store) GetURL(ctx context.Context, key string) (string, error) {
	return fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s", ss.bucket, ss.region, key), nil
}

// Name returns "s3".
func (ss *S3Store) Name() string { return "s3" }

// NewRecordingStore creates the appropriate recording store based on the
// backend name.
func NewRecordingStore(ctx context.Context, backend, s3Bucket, s3Region, localPath string) (RecordingStore, error) {
	switch backend {
	case "s3":
		return NewS3Store(ctx, s3Bucket, s3Region)
	case "local":
		return NewLocalStore(localPath)
	default:
		return NewLocalStore(localPath)
	}
}