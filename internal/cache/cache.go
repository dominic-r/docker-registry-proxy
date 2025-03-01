package cache

import (
	"context"
	"time"

	"github.com/sdko-org/registry-proxy/internal/config"
	"github.com/sdko-org/registry-proxy/internal/models"
	"github.com/sdko-org/registry-proxy/internal/storage"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

type CachePurger struct {
	logger  *logrus.Logger
	db      *gorm.DB
	storage storage.Storage
	cfg     *config.Config
}

func NewCachePurger(logger *logrus.Logger, db *gorm.DB, storage storage.Storage, cfg *config.Config) *CachePurger {
	return &CachePurger{
		logger:  logger,
		db:      db,
		storage: storage,
		cfg:     cfg,
	}
}

func (c *CachePurger) Start(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Minute)
	defer ticker.Stop()

	logEntry := c.logger.WithField("component", "cache_purger")
	logEntry.Info("Starting cache purger")

	for {
		select {
		case <-ticker.C:
			c.purgeExpiredCache(ctx, logEntry)
		case <-ctx.Done():
			logEntry.Info("Stopping cache purger")
			return
		}
	}
}

func (c *CachePurger) purgeExpiredCache(ctx context.Context, log *logrus.Entry) {
	start := time.Now()
	log = log.WithField("operation", "cache_purge")

	var entries []models.CacheEntry
	if err := c.db.WithContext(ctx).
		Where("expires_at < ? OR last_access < ?",
			time.Now(),
			time.Now().Add(-7*24*time.Hour)).
		Find(&entries).Error; err != nil {
		log.WithError(err).Error("Cache purge query failed")
		return
	}

	log.WithField("count", len(entries)).Info("Processing expired cache entries")
	deleted := 0

	for _, entry := range entries {
		if err := c.storage.Delete(ctx, entry.Key); err != nil {
			log.WithFields(logrus.Fields{
				"key":   entry.Key,
				"error": err,
			}).Error("Failed to delete cache entry")
			continue
		}
		deleted++
	}

	log.WithFields(logrus.Fields{
		"deleted_entries": deleted,
		"failed_deletes":  len(entries) - deleted,
		"duration":        time.Since(start),
	}).Info("Cache purge completed")
}
