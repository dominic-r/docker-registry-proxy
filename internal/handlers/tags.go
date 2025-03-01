package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/sirupsen/logrus"
)

func (h *ProxyHandler) handleTagsList(w http.ResponseWriter, r *http.Request, image string) {
	ctx := context.Background()
	cacheKey := fmt.Sprintf("tags/%s", image)

	content, _, _, err := h.storage.Get(ctx, cacheKey)
	if err == nil {
		h.log.WithFields(logrus.Fields{
			"image":  image,
			"source": "cache",
		}).Info("Serving tags from cache")
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
		w.WriteHeader(http.StatusOK)
		w.Write(content)
		return
	}

	h.log.WithFields(logrus.Fields{
		"image":  image,
		"source": "dockerhub",
	}).Info("Fetching tags from upstream")
	resp, err := h.dhClient.GetTags(ctx, image)
	if err != nil {
		http.Error(w, "Failed to fetch tags", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		forwardResponse(w, resp)
		return
	}

	body, _ := io.ReadAll(resp.Body)

	var tagsResponse struct {
		Name string   `json:"name"`
		Tags []string `json:"tags"`
	}
	if err := json.Unmarshal(body, &tagsResponse); err != nil {
		h.log.WithError(err).Error("Invalid tags response from Docker Hub")
		http.Error(w, "Invalid tags response", http.StatusBadGateway)
		return
	}

	if err := h.storage.Put(ctx, cacheKey, body, "", "application/json", 24*time.Hour); err != nil {
		h.log.WithError(err).Error("Failed to cache tags list")
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
	w.WriteHeader(resp.StatusCode)
	w.Write(body)
}

func HandleCatalog(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"repositories": []string{},
	})
}
