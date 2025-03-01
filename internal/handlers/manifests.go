package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"

	"github.com/sirupsen/logrus"
)

func (h *ProxyHandler) handleManifest(w http.ResponseWriter, r *http.Request, image, reference string) {
	ctx := context.Background()
	cacheKey := fmt.Sprintf("manifests/%s/%s", image, reference)

	content, digest, mediaType, err := h.storage.Get(ctx, cacheKey)
	if err == nil {
		h.log.WithFields(logrus.Fields{
			"image":     image,
			"reference": reference,
			"source":    "s3",
		}).Info("Serving manifest from cache")
		w.Header().Set("Content-Type", mediaType)
		w.Header().Set("Docker-Content-Digest", digest)
		w.Header().Set("Content-Length", fmt.Sprint(len(content)))
		w.WriteHeader(http.StatusOK)
		w.Write(content)
		return
	}

	h.log.WithFields(logrus.Fields{
		"image":     image,
		"reference": reference,
		"source":    "dockerhub",
	}).Info("Fetching manifest from upstream")
	resp, err := h.dhClient.GetManifest(ctx, image, reference, r.Header.Get("Accept"))
	if err != nil {
		http.Error(w, "Failed to fetch manifest", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		forwardResponse(w, resp)
		return
	}

	body, _ := io.ReadAll(resp.Body)
	mediaType = resp.Header.Get("Content-Type")
	digest = resp.Header.Get("Docker-Content-Digest")
	if digest == "" {
		hash := sha256.Sum256(body)
		digest = "sha256:" + hex.EncodeToString(hash[:])
	}

	if err := h.storage.Put(ctx, cacheKey, body, digest, mediaType, h.cfg.CacheTTL); err != nil {
		h.log.WithError(err).Error("Failed to cache manifest")
	}

	w.Header().Set("Content-Type", mediaType)
	w.Header().Set("Docker-Content-Digest", digest)
	w.WriteHeader(resp.StatusCode)
	w.Write(body)
}
