package seaweed

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/minio/minio-go/v7"

	"github.com/algonode/blocksrv/store"
)

type objectMeta struct {
	ContentType  string
	CacheControl string
	UserMeta     map[string]string
}

type objectInfo struct {
	Size        int64
	ContentType string
	ETag        string
}

// objectStore is the minimal S3 surface the backend needs; it keeps the
// backend logic testable against an in-memory fake.
type objectStore interface {
	Put(ctx context.Context, key string, payload []byte, meta objectMeta) error
	// Get returns the object body; the caller must close it. Missing keys return store.ErrNotFound.
	Get(ctx context.Context, key string) (io.ReadCloser, objectInfo, error)
	Stat(ctx context.Context, key string) (objectInfo, error)
	Exists(ctx context.Context, key string) (bool, error)
}

var errNoSuchBucket = errors.New("bucket does not exist")

func mapErr(err error) error {
	if err == nil {
		return nil
	}
	var er minio.ErrorResponse
	if errors.As(err, &er) {
		switch er.Code {
		case minio.NoSuchKey:
			return store.ErrNotFound
		case minio.NoSuchBucket:
			return fmt.Errorf("%w: %s", errNoSuchBucket, er.BucketName)
		}
	}
	return err
}

type minioStore struct {
	mc      *minio.Client
	bucket  string
	timeout time.Duration
}

func (s *minioStore) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if s.timeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, s.timeout)
}

func (s *minioStore) Put(ctx context.Context, key string, payload []byte, meta objectMeta) error {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	_, err := s.mc.PutObject(ctx, s.bucket, key, bytes.NewReader(payload), int64(len(payload)), minio.PutObjectOptions{
		ContentType:      meta.ContentType,
		CacheControl:     meta.CacheControl,
		UserMetadata:     meta.UserMeta,
		DisableMultipart: true,
	})
	return mapErr(err)
}

// cancelReadCloser releases the request context once the body is closed.
type cancelReadCloser struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c *cancelReadCloser) Close() error {
	err := c.ReadCloser.Close()
	c.cancel()
	return err
}

func (s *minioStore) Get(ctx context.Context, key string) (io.ReadCloser, objectInfo, error) {
	ctx, cancel := s.withTimeout(ctx)
	// Core.GetObject issues the request eagerly, so errors surface here rather than on the first Read.
	body, info, _, err := minio.Core{Client: s.mc}.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		cancel()
		return nil, objectInfo{}, mapErr(err)
	}
	return &cancelReadCloser{ReadCloser: body, cancel: cancel}, toInfo(info), nil
}

func (s *minioStore) Stat(ctx context.Context, key string) (objectInfo, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	info, err := s.mc.StatObject(ctx, s.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		return objectInfo{}, mapErr(err)
	}
	return toInfo(info), nil
}

func (s *minioStore) Exists(ctx context.Context, key string) (bool, error) {
	_, err := s.Stat(ctx, key)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func toInfo(info minio.ObjectInfo) objectInfo {
	return objectInfo{Size: info.Size, ContentType: info.ContentType, ETag: info.ETag}
}
