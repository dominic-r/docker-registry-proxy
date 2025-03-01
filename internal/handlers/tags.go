package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/sdko-org/registry-proxy/internal/models"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm/clause"
)

func (h *ProxyHandler) handleTagsList(w http.ResponseWriter, r *http.Request, image string) {
	ctx := context.Background()
	log := h.log.WithFields(logrus.Fields{
		"repository": image,
		"operation":  "tags_list",
	})

	log.Debug("Handling tags list request")

	var cachedTag models.TagCache
	err := h.db.WithContext(ctx).
		Where("repository = ? AND expires_at > ?", image, time.Now()).
		First(&cachedTag).Error

	if err == nil && time.Since(cachedTag.StoredAt) < h.cfg.TagCacheTTL/2 {
		log.WithFields(logrus.Fields{
			"source":    "cache",
			"stored_at": cachedTag.StoredAt,
			"etag":      cachedTag.ETag,
		}).Info("Serving fresh cached tags")
		h.serveCachedTags(w, &cachedTag)
		return
	}

	if err == nil {
		log.WithFields(logrus.Fields{
			"source":    "cache",
			"stored_at": cachedTag.StoredAt,
			"etag":      cachedTag.ETag,
		}).Info("Validating stale tags cache with upstream")

		if h.validateTagsWithUpstream(ctx, image, &cachedTag) {
			log.Info("Cache validation successful, serving cached tags")
			h.serveCachedTags(w, &cachedTag)
			return
		}
	}

	log.WithFields(logrus.Fields{
		"reason": map[string]interface{}{
			"db_error":    err,
			"cache_fresh": err == nil,
		},
	}).Info("Fetching tags from upstream")

	resp, err := h.dhClient.GetTags(ctx, image)
	if err != nil {
		log.WithError(err).Error("Failed to fetch tags from upstream")
		http.Error(w, "Failed to fetch tags", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.WithFields(logrus.Fields{
			"status_code": resp.StatusCode,
			"headers":     resp.Header,
		}).Error("Unexpected response from upstream")
		forwardResponse(w, resp)
		return
	}

	body, _ := io.ReadAll(resp.Body)
	etag := resp.Header.Get("ETag")
	lastModified, _ := time.Parse(time.RFC1123, resp.Header.Get("Last-Modified"))

	log = log.WithFields(logrus.Fields{
		"etag":          etag,
		"last_modified": lastModified,
		"body_size":     len(body),
	})

	var tagsResponse struct {
		Name string   `json:"name"`
		Tags []string `json:"tags"`
	}
	if err := json.Unmarshal(body, &tagsResponse); err != nil {
		log.WithError(err).Error("Failed to parse tags response")
		http.Error(w, "Invalid tags response", http.StatusBadGateway)
		return
	}

	log.WithField("tag_count", len(tagsResponse.Tags)).Info("Caching new tags list")
	h.cacheTags(image, body, etag, lastModified)

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
	w.Header().Set("ETag", etag)
	w.WriteHeader(http.StatusOK)
	w.Write(body)
}

func (h *ProxyHandler) serveCachedTags(w http.ResponseWriter, cachedTag *models.TagCache) {
	h.log.WithFields(logrus.Fields{
		"repository":  cachedTag.Repository,
		"etag":        cachedTag.ETag,
		"expires_at":  cachedTag.ExpiresAt,
		"last_access": time.Now(),
		"tag_count":   len(cachedTag.Tags),
		"source":      "cache",
	}).Info("Serving tags from cache")

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
	w.Header().Set("ETag", cachedTag.ETag)
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(cachedTag.Tags))
}

func (h *ProxyHandler) validateTagsWithUpstream(ctx context.Context, image string, cachedTag *models.TagCache) bool {
	log := h.log.WithFields(logrus.Fields{
		"repository": image,
		"operation":  "cache_validation",
		"etag":       cachedTag.ETag,
	})

	req, _ := http.NewRequest("GET",
		fmt.Sprintf("https://registry-1.docker.io/v2/%s/tags/list", image), nil)
	req.Header.Set("If-None-Match", cachedTag.ETag)

	log.Debug("Sending conditional request to upstream")
	resp, err := h.dhClient.DoRequestWithAuth(ctx, req)
	if err != nil {
		log.WithError(err).Warn("Cache validation request failed")
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotModified {
		log.WithFields(logrus.Fields{
			"status_code": resp.StatusCode,
			"headers":     resp.Header,
		}).Warn("Cache validation failed - stale entry")
		return false
	}

	log.Info("Cache validation successful - refreshing expiration")
	h.db.Model(cachedTag).Updates(map[string]interface{}{
		"expires_at": time.Now().Add(h.cfg.TagCacheTTL),
		"stored_at":  time.Now(),
	})
	return true
}

func (h *ProxyHandler) cacheTags(image string, body []byte, etag string, lastModified time.Time) {
	log := h.log.WithFields(logrus.Fields{
		"repository":    image,
		"operation":     "cache_tags",
		"etag":          etag,
		"last_modified": lastModified,
		"ttl":           h.cfg.TagCacheTTL,
	})

	tagEntry := models.TagCache{
		Repository:   image,
		Tags:         string(body),
		ETag:         etag,
		LastModified: lastModified,
		ExpiresAt:    time.Now().Add(h.cfg.TagCacheTTL),
		StoredAt:     time.Now(),
	}

	log.Debug("Storing tags in cache")
	err := h.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "repository"}},
		DoUpdates: clause.AssignmentColumns([]string{"tags", "etag", "last_modified", "expires_at", "stored_at"}),
	}).Create(&tagEntry).Error

	if err != nil {
		log.WithError(err).Error("Failed to cache tags")
	} else {
		log.WithField("tag_count", len(tagEntry.Tags)).Info("Tags cached successfully")
	}
}

func (h *ProxyHandler) InvalidateCache(w http.ResponseWriter, r *http.Request) {
	log := h.log.WithField("operation", "cache_invalidation")
	image := r.URL.Query().Get("image")
	digest := r.URL.Query().Get("digest")

	if image != "" {
		log = log.WithField("repository", image)
		result := h.db.Where("repository = ?", image).Delete(&models.TagCache{})
		if result.Error != nil {
			log.WithError(result.Error).Error("Tag cache invalidation failed")
		} else {
			log.WithField("rows_affected", result.RowsAffected).Info("Invalidated tag cache")
		}
	}
	if digest != "" {
		log = log.WithField("digest", digest)
		result := h.db.Where("digest = ?", digest).Delete(&models.RegistryCache{})
		if result.Error != nil {
			log.WithError(result.Error).Error("Registry cache invalidation failed")
		} else {
			log.WithField("rows_affected", result.RowsAffected).Info("Invalidated registry cache")
		}
	}

	w.WriteHeader(http.StatusOK)
}

func HandleCatalog(w http.ResponseWriter, r *http.Request) {
	log := logrus.WithFields(logrus.Fields{
		"operation": "catalog",
		"method":    r.Method,
	})
	log.Debug("Handling catalog request")

	w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(map[string]interface{}{
		"repositories": []string{},
	}); err != nil {
		log.WithError(err).Error("Failed to encode catalog response")
	}
}
