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
	log = log.WithField("operation", "cache_purge")

	var registryEntries []models.RegistryCache
	if err := c.db.WithContext(ctx).
		Where("expires_at < ? OR last_access < ?", time.Now(), time.Now().Add(-7*24*time.Hour)).
		Find(&registryEntries).Error; err != nil {
		log.WithError(err).Error("Registry cache purge query failed")
	}

	var tagEntries []models.TagCache
	if err := c.db.WithContext(ctx).
		Where("expires_at < ?", time.Now()).
		Find(&tagEntries).Error; err != nil {
		log.WithError(err).Error("Tag cache purge query failed")
	}

	log.WithField("count", len(registryEntries)+len(tagEntries)).Info("Processing expired cache entries")

	for _, entry := range registryEntries {
		if err := c.storage.Delete(ctx, entry.Key); err != nil {
			log.WithFields(logrus.Fields{"key": entry.Key, "error": err}).Error("Failed to delete registry cache entry")
		}
	}

	for _, entry := range tagEntries {
		if err := c.db.Delete(&entry).Error; err != nil {
			log.WithFields(logrus.Fields{"repository": entry.Repository, "error": err}).Error("Failed to delete tag cache entry")
		}
	}
}
