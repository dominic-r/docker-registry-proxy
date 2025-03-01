package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
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
	client         *s3.S3
	uploader       *s3manager.Uploader
	cfg            *config.Config
	db             *gorm.DB
	log            *logrus.Entry
	activeUploads  sync.Map
	mu             sync.Mutex
	partSize       int64
	maxRetries     int
	uploadTimeouts map[string]time.Time
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

	uploader := s3manager.NewUploader(sess, func(u *s3manager.Uploader) {
		u.PartSize = 5 * 1024 * 1024
		u.Concurrency = 3
		u.LeavePartsOnError = false
	})

	return &S3Storage{
		client:         s3.New(sess),
		uploader:       uploader,
		cfg:            cfg,
		db:             db,
		log:            logger.WithField("component", "storage"),
		partSize:       10 * 1024 * 1024,
		maxRetries:     5,
		uploadTimeouts: make(map[string]time.Time),
	}
}

func (s *S3Storage) Get(ctx context.Context, key string) ([]byte, string, string, error) {
	log := s.log.WithFields(logrus.Fields{
		"operation": "get",
		"key":       key,
	})

	if expiry, exists := s.activeUploads.Load(key); exists {
		if time.Now().Before(expiry.(time.Time)) {
			log.Debug("Waiting for active upload completion")
			for i := 0; i < 10; i++ {
				time.Sleep(500 * time.Millisecond)
				var entry models.CacheEntry
				if err := s.db.WithContext(ctx).Where("key = ?", key).First(&entry).Error; err == nil {
					break
				}
			}
		} else {
			s.activeUploads.Delete(key)
		}
	}

	var entry models.CacheEntry
	if err := s.db.WithContext(ctx).Where("key = ?", key).First(&entry).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
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
		if awsErr, ok := err.(awserr.Error); ok {
			log.WithFields(logrus.Fields{
				"code":    awsErr.Code(),
				"message": awsErr.Message(),
			}).Error("S3 get failed")

			if reqErr, ok := err.(awserr.RequestFailure); ok {
				log.Errorf("HTTP Status: %d", reqErr.StatusCode())
				log.Errorf("Request ID: %s", reqErr.RequestID())
			}
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
		s.logS3ErrorDetails(err, log)
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

	s.mu.Lock()
	s.uploadTimeouts[key] = time.Now().Add(30 * time.Minute)
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.uploadTimeouts, key)
		s.mu.Unlock()
	}()

	var lastErr error
	for attempt := 1; attempt <= s.maxRetries; attempt++ {
		uploadCtx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()

		_, err := s.uploader.UploadWithContext(uploadCtx, &s3manager.UploadInput{
			Bucket:      aws.String(s.cfg.S3Bucket),
			Key:         aws.String(key),
			Body:        content,
			ContentType: aws.String(mediaType),
			Metadata: map[string]*string{
				"Docker-Content-Digest": aws.String(digest),
			},
		})

		if err == nil {
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

		lastErr = err
		s.logS3ErrorDetails(err, log)

		if awsErr, ok := err.(awserr.Error); ok {
			if awsErr.Code() == "RequestCanceled" {
				log.Warnf("Upload canceled, retry %d/%d", attempt, s.maxRetries)
				time.Sleep(time.Duration(attempt) * time.Second)
				continue
			}

			if reqErr, ok := err.(awserr.RequestFailure); ok {
				if reqErr.StatusCode() == 413 {
					log.Error("Entity too large - consider reducing part size")
					return fmt.Errorf("configured part size too large: %w", err)
				}
			}
		}

		if !isRetryableError(err) {
			log.Error("Non-retryable error encountered")
			break
		}

		log.Warnf("Retrying upload (%d/%d)", attempt, s.maxRetries)
		time.Sleep(time.Duration(attempt*2) * time.Second)
	}

	return fmt.Errorf("upload failed after %d attempts: %w", s.maxRetries, lastErr)
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

func (s *S3Storage) logS3ErrorDetails(err error, log *logrus.Entry) {
	if awsErr, ok := err.(awserr.Error); ok {
		log.WithField("code", awsErr.Code())

		if reqErr, ok := err.(awserr.RequestFailure); ok {
			log.WithFields(logrus.Fields{
				"status_code": reqErr.StatusCode(),
				"request_id":  reqErr.RequestID(),
				"host_id":     "e",
			})

			if reqErr.StatusCode() >= 400 {
				log.Errorf("Response Body: %s", string(reqErr.OrigErr().Error()))
			}
		}

		if origErr := awsErr.OrigErr(); origErr != nil {
			log.WithField("original_error", origErr.Error())
		}
	}
	log.Error("S3 operation failed")
}

func isRetryableError(err error) bool {
	if awsErr, ok := err.(awserr.Error); ok {
		switch awsErr.Code() {
		case "RequestTimeout",
			"Throttling",
			"ThrottlingException",
			"RequestLimitExceeded",
			"ServiceUnavailable",
			"InternalError",
			"EC2RoleRequestError":
			return true
		}
	}

	if reqErr, ok := err.(awserr.RequestFailure); ok {
		return reqErr.StatusCode() >= 500
	}

	return false
}
