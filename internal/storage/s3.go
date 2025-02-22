package storage

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/aws/aws-sdk-go/service/s3/s3manager"
	"github.com/sdko-org/registry-proxy/internal/config"
	"github.com/sdko-org/registry-proxy/internal/models"
	"gorm.io/gorm"
)

type Storage interface {
	Get(ctx context.Context, key string) ([]byte, string, error)
	Put(ctx context.Context, key string, content []byte, digest string, ttl time.Duration) error
	PutStream(ctx context.Context, key string, content io.Reader, digest string, ttl time.Duration) error
	Delete(ctx context.Context, key string) error
}

type S3Storage struct {
	client   *s3.S3
	uploader *s3manager.Uploader
	cfg      *config.Config
	db       *gorm.DB
}

func NewS3Storage(cfg *config.Config, db *gorm.DB) *S3Storage {
	awsConfig := &aws.Config{
		Region:           aws.String(cfg.S3Region),
		Credentials:      credentials.NewStaticCredentials(cfg.S3AccessKey, cfg.S3SecretKey, ""),
		S3ForcePathStyle: aws.Bool(true),
	}

	if cfg.S3Endpoint != "" {
		awsConfig.Endpoint = aws.String(cfg.S3Endpoint)
	}

	sess := session.Must(session.NewSession(awsConfig))

	return &S3Storage{
		client:   s3.New(sess),
		uploader: s3manager.NewUploader(sess),
		cfg:      cfg,
		db:       db,
	}
}

func (s *S3Storage) Get(ctx context.Context, key string) ([]byte, string, error) {
	resp, err := s.client.GetObjectWithContext(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.cfg.S3Bucket),
		Key:    aws.String(key),
	})

	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	content, _ := io.ReadAll(resp.Body)
	digest := aws.StringValue(resp.Metadata["Docker-Content-Digest"])

	return content, digest, nil
}

func (s *S3Storage) Put(ctx context.Context, key string, content []byte, digest string, ttl time.Duration) error {
	_, err := s.uploader.UploadWithContext(ctx, &s3manager.UploadInput{
		Bucket:      aws.String(s.cfg.S3Bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(content),
		ContentType: aws.String("application/vnd.docker.distribution.manifest.v2+json"),
		Metadata: map[string]*string{
			"Docker-Content-Digest": aws.String(digest),
		},
	})

	if err != nil {
		return fmt.Errorf("s3 upload failed: %w", err)
	}

	entry := models.CacheEntry{
		Key:        key,
		Digest:     digest,
		StoredAt:   time.Now(),
		ExpiresAt:  time.Now().Add(ttl),
		LastAccess: time.Now(),
		SizeBytes:  int64(len(content)),
	}

	if err := s.db.WithContext(ctx).Save(&entry).Error; err != nil {
		return fmt.Errorf("failed to save cache entry: %w", err)
	}

	return nil
}

func (s *S3Storage) PutStream(ctx context.Context, key string, content io.Reader, digest string, ttl time.Duration) error {
	_, err := s.uploader.UploadWithContext(ctx, &s3manager.UploadInput{
		Bucket:      aws.String(s.cfg.S3Bucket),
		Key:         aws.String(key),
		Body:        content,
		ContentType: aws.String("application/octet-stream"),
		Metadata: map[string]*string{
			"Docker-Content-Digest": aws.String(digest),
		},
	})

	return err
}

func (s *S3Storage) Delete(ctx context.Context, key string) error {
	_, err := s.client.DeleteObjectWithContext(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.cfg.S3Bucket),
		Key:    aws.String(key),
	})

	if dbErr := s.db.Where("key = ?", key).Delete(&models.CacheEntry{}).Error; dbErr != nil {
		log.Printf("Failed to delete cache entry from DB: %v", dbErr)
	}

	return err
}
