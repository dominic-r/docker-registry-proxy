package storage

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/aws/aws-sdk-go/service/s3/s3manager"
	"github.com/sdko-org/registry-proxy/internal/config"
	"github.com/sdko-org/registry-proxy/internal/models"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type S3Storage struct {
	client   *s3.S3
	uploader *s3manager.Uploader
	cfg      *config.Config
	db       *gorm.DB
	log      *logrus.Entry
}

func NewS3Storage(logger *logrus.Logger, cfg *config.Config, db *gorm.DB) *S3Storage {
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
		log:      logger.WithField("component", "storage"),
	}
}

func (s *S3Storage) Get(ctx context.Context, key string) ([]byte, string, string, error) {
	log := s.log.WithFields(logrus.Fields{
		"operation": "get",
		"key":       key,
	})

	var entry models.CacheEntry
	if err := s.db.WithContext(ctx).Where("key = ?", key).First(&entry).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			log.Debug("Cache miss")
			return nil, "", "", fmt.Errorf("cache miss")
		}
		log.WithError(err).Error("Database query failed")
		return nil, "", "", fmt.Errorf("database error: %w", err)
	}

	if time.Now().After(entry.ExpiresAt) {
		log.Debug("Cache entry expired")
		if err := s.Delete(ctx, key); err != nil {
			log.WithError(err).Error("Failed to delete expired entry")
		}
		return nil, "", "", fmt.Errorf("cache expired")
	}

	resp, err := s.client.GetObjectWithContext(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.cfg.S3Bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok {
			log.WithFields(logrus.Fields{
				"code":    aerr.Code(),
				"message": aerr.Message(),
			}).Error("S3 get failed")
		}
		return nil, "", "", fmt.Errorf("s3 get failed: %w", err)
	}
	defer resp.Body.Close()

	content, err := io.ReadAll(resp.Body)
	if err != nil {
		log.WithError(err).Error("Failed to read S3 object")
		return nil, "", "", fmt.Errorf("read failed: %w", err)
	}

	mediaType := aws.StringValue(resp.ContentType)
	digest := aws.StringValue(resp.Metadata["Docker-Content-Digest"])
	if digest == "" {
		digest = entry.Digest
	}

	log.WithFields(logrus.Fields{
		"size":       len(content),
		"digest":     digest,
		"media_type": mediaType,
	}).Debug("Cache hit")

	if err := s.db.WithContext(ctx).Model(&models.CacheEntry{}).
		Where("key = ?", key).
		Update("last_access", time.Now()).Error; err != nil {
		log.WithError(err).Warn("Failed to update last access time")
	}

	return content, digest, mediaType, nil
}

func (s *S3Storage) Put(ctx context.Context, key string, content []byte, digest, mediaType string, ttl time.Duration) error {
	log := s.log.WithFields(logrus.Fields{
		"operation":  "put",
		"key":        key,
		"size":       len(content),
		"ttl":        ttl,
		"media_type": mediaType,
	})

	_, err := s.uploader.UploadWithContext(ctx, &s3manager.UploadInput{
		Bucket:      aws.String(s.cfg.S3Bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(content),
		ContentType: aws.String(mediaType),
		Metadata: map[string]*string{
			"Docker-Content-Digest": aws.String(digest),
		},
	})

	if err != nil {
		log.WithError(err).Error("S3 upload failed")
		return fmt.Errorf("upload failed: %w", err)
	}

	entry := models.CacheEntry{
		Key:        key,
		Digest:     digest,
		MediaType:  mediaType,
		StoredAt:   time.Now(),
		ExpiresAt:  time.Now().Add(ttl),
		LastAccess: time.Now(),
		SizeBytes:  int64(len(content)),
	}

	if err := s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"digest", "media_type", "expires_at", "last_access", "size_bytes"}),
	}).Create(&entry).Error; err != nil {
		log.WithError(err).Error("Failed to upsert cache entry")
		return fmt.Errorf("database error: %w", err)
	}

	log.Debug("Cache entry stored")
	return nil
}

func (s *S3Storage) PutStream(ctx context.Context, key string, content io.Reader, digest, mediaType string, ttl time.Duration) error {
	log := s.log.WithFields(logrus.Fields{
		"operation":  "put_stream",
		"key":        key,
		"digest":     digest,
		"media_type": mediaType,
	})

	_, err := s.uploader.UploadWithContext(ctx, &s3manager.UploadInput{
		Bucket:      aws.String(s.cfg.S3Bucket),
		Key:         aws.String(key),
		Body:        content,
		ContentType: aws.String(mediaType),
		Metadata: map[string]*string{
			"Docker-Content-Digest": aws.String(digest),
		},
	})
	if err != nil {
		log.WithError(err).Error("S3 stream upload failed")
		return fmt.Errorf("stream upload failed: %w", err)
	}

	entry := models.CacheEntry{
		Key:        key,
		Digest:     digest,
		MediaType:  mediaType,
		StoredAt:   time.Now(),
		ExpiresAt:  time.Now().Add(ttl),
		LastAccess: time.Now(),
		SizeBytes:  -1,
	}

	if err := s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"digest", "media_type", "expires_at", "last_access"}),
	}).Create(&entry).Error; err != nil {
		log.WithError(err).Error("Failed to upsert stream cache entry")
		return fmt.Errorf("database error: %w", err)
	}

	log.Debug("Stream cache entry stored")
	return nil
}

func (s *S3Storage) Delete(ctx context.Context, key string) error {
	log := s.log.WithFields(logrus.Fields{
		"operation": "delete",
		"key":       key,
	})

	_, err := s.client.DeleteObjectWithContext(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.cfg.S3Bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		log.WithError(err).Error("S3 delete failed")
		return fmt.Errorf("s3 delete failed: %w", err)
	}

	if err := s.db.WithContext(ctx).Where("key = ?", key).Delete(&models.CacheEntry{}).Error; err != nil {
		log.WithError(err).Error("Failed to delete cache entry from DB")
		return fmt.Errorf("database delete failed: %w", err)
	}

	log.Debug("Cache entry deleted")
	return nil
}

func (s *S3Storage) UpdateLastAccess(ctx context.Context, key string) error {
	return s.db.WithContext(ctx).Model(&models.CacheEntry{}).
		Where("key = ?", key).
		Update("last_access", time.Now()).Error
}
