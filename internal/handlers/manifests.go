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

	content, digest, mediaType, err := h.storage.Get(ctx, cacheKey)
	if err == nil {
		w.Header().Set("Content-Type", mediaType)
		w.Header().Set("Docker-Content-Digest", digest)
		w.Header().Set("Content-Length", fmt.Sprint(len(content)))
		w.WriteHeader(http.StatusOK)
		w.Write(content)
		return
	}

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

func (h *ProxyHandler) handleBlob(w http.ResponseWriter, r *http.Request, image, digest string) {
	ctx := r.Context()
	cacheKey := fmt.Sprintf("blobs/%s/%s", image, digest)

	content, cachedDigest, mediaType, err := h.storage.Get(ctx, cacheKey)
	if err == nil {
		w.Header().Set("Content-Type", mediaType)
		w.Header().Set("Docker-Content-Digest", cachedDigest)
		w.Header().Set("Content-Length", fmt.Sprint(len(content)))
		w.Write(content)
		return
	}

	resp, err := h.dhClient.GetBlob(ctx, image, digest)
	if err != nil {
		http.Error(w, "Failed to fetch blob", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	var buf bytes.Buffer
	tee := io.TeeReader(resp.Body, &buf)
	mediaType = resp.Header.Get("Content-Type")

	if err := h.storage.PutStream(ctx, cacheKey, tee, digest, mediaType, h.cfg.CacheTTL); err != nil {
		http.Error(w, "Caching failed", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", mediaType)
	w.Header().Set("Docker-Content-Digest", digest)
	io.Copy(w, &buf)
}

func forwardResponse(w http.ResponseWriter, resp *http.Response) {
	for k, v := range resp.Header {
		w.Header()[k] = v
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}
