package handlers

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/sdko-org/registry-proxy/internal/config"
	"github.com/sdko-org/registry-proxy/internal/dockerhub"
	"github.com/sdko-org/registry-proxy/internal/storage"
	"github.com/sirupsen/logrus"
)

type ProxyHandler struct {
	cfg      *config.Config
	storage  storage.Storage
	dhClient *dockerhub.Client
	log      *logrus.Entry
}

func NewProxyHandler(logger *logrus.Logger, cfg *config.Config, storage storage.Storage, dhClient *dockerhub.Client) *ProxyHandler {
	return &ProxyHandler{
		cfg:      cfg,
		storage:  storage,
		dhClient: dhClient,
		log:      logger.WithField("component", "proxy_handler"),
	}
}

func (h *ProxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v2/")
	parts := strings.Split(path, "/")

	if len(parts) < 2 {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	image := strings.Join(parts[:len(parts)-2], "/")
	resourceType := parts[len(parts)-2]
	reference := parts[len(parts)-1]

	switch resourceType {
	case "manifests":
		h.handleManifest(w, r, image, reference)
	case "blobs":
		h.handleBlob(w, r, image, reference)
	default:
		http.Error(w, "Not found", http.StatusNotFound)
	}
}

func (h *ProxyHandler) handleManifest(w http.ResponseWriter, r *http.Request, image, reference string) {
	ctx := r.Context()
	cacheKey := fmt.Sprintf("manifests/%s/%s", image, reference)
	log := h.log.WithFields(logrus.Fields{
		"operation": "manifest",
		"image":     image,
		"reference": reference,
	})

	content, digest, err := h.storage.Get(ctx, cacheKey)
	if err == nil {
		if err := h.storage.UpdateLastAccess(ctx, cacheKey); err != nil {
			log.WithError(err).Warn("Failed to update last access")
		}
		w.Header().Set("Content-Type", "application/vnd.docker.distribution.manifest.v2+json")
		w.Header().Set("Docker-Content-Digest", digest)
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(content)))
		w.Write(content)
		return
	}

	resp, err := h.dhClient.GetManifest(ctx, image, reference)
	if err != nil {
		log.WithError(err).Error("Failed to fetch manifest")
		http.Error(w, "Failed to fetch manifest", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		forwardResponse(w, resp)
		return
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.WithError(err).Error("Failed to read manifest body")
		http.Error(w, "Failed to process manifest", http.StatusInternalServerError)
		return
	}

	digest = resp.Header.Get("Docker-Content-Digest")
	if digest == "" {
		hash := sha256.Sum256(body)
		digest = "sha256:" + hex.EncodeToString(hash[:])
	}

	if err := h.storage.Put(ctx, cacheKey, body, digest, h.cfg.CacheTTL); err != nil {
		log.WithError(err).Error("Failed to cache manifest")
	}

	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.Header().Set("Docker-Content-Digest", digest)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(body)))
	w.WriteHeader(resp.StatusCode)
	w.Write(body)
}

func (h *ProxyHandler) handleBlob(w http.ResponseWriter, r *http.Request, image, digest string) {
	ctx := r.Context()
	cacheKey := fmt.Sprintf("blobs/%s/%s", image, digest)
	log := h.log.WithFields(logrus.Fields{
		"operation": "blob",
		"image":     image,
		"digest":    digest,
	})

	content, _, err := h.storage.Get(ctx, cacheKey)
	if err == nil {
		if err := h.storage.UpdateLastAccess(ctx, cacheKey); err != nil {
			log.WithError(err).Warn("Failed to update last access")
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(content)))
		w.Write(content)
		return
	}

	resp, err := h.dhClient.GetBlob(ctx, image, digest)
	if err != nil {
		log.WithError(err).Error("Failed to fetch blob")
		http.Error(w, "Failed to fetch blob", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		forwardResponse(w, resp)
		return
	}

	var buf bytes.Buffer
	tee := io.TeeReader(resp.Body, &buf)

	contentLength := resp.Header.Get("Content-Length")
	if contentLength == "" {
		log.Warn("Missing Content-Length header for blob")
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	if contentLength != "" {
		w.Header().Set("Content-Length", contentLength)
	}

	if err := h.storage.PutStream(ctx, cacheKey, tee, digest, h.cfg.CacheTTL); err != nil {
		log.WithError(err).Error("Caching failed")
		http.Error(w, "Caching failed", http.StatusInternalServerError)
		return
	}

	io.Copy(w, &buf)
}

func forwardResponse(w http.ResponseWriter, resp *http.Response) {
	for k, v := range resp.Header {
		w.Header()[k] = v
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}
