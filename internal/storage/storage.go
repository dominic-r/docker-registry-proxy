package storage

import (
	"context"
	"io"
	"time"
)

type Storage interface {
	Get(ctx context.Context, key string) ([]byte, string, string, error)
	Put(ctx context.Context, key string, content []byte, digest, mediaType string, ttl time.Duration) error
	PutStream(ctx context.Context, key string, content io.Reader, digest, mediaType string, ttl time.Duration) error
	Delete(ctx context.Context, key string) error
	UpdateLastAccess(ctx context.Context, key string) error
}
