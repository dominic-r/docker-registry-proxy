package handlers

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/sdko-org/registry-proxy/internal/config"
	"github.com/sdko-org/registry-proxy/internal/dockerhub"
	"github.com/sdko-org/registry-proxy/internal/storage"
)

type ProxyHandler struct {
	cfg      *config.Config
	storage  storage.Storage
	dhClient *dockerhub.Client
}

func NewProxyHandler(cfg *config.Config, storage storage.Storage, dhClient *dockerhub.Client) *ProxyHandler {
	return &ProxyHandler{
		cfg:      cfg,
		storage:  storage,
		dhClient: dhClient,
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

	content, digest, err := h.storage.Get(ctx, cacheKey)
	if err == nil {
		if err := h.storage.UpdateLastAccess(ctx, cacheKey); err != nil {
			log.Printf("Failed to update last access time: %v", err)
		}
		w.Header().Set("Content-Type", "application/vnd.docker.distribution.manifest.v2+json")
		w.Header().Set("Docker-Content-Digest", digest)
		w.Write(content)
		return
	}

	resp, err := h.dhClient.GetManifest(ctx, image, reference)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		headers := w.Header()
		for k, v := range resp.Header {
			headers[k] = v
		}
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
		return
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("Error reading manifest body: %v", err)
		http.Error(w, "Failed to process manifest", http.StatusInternalServerError)
		return
	}

	digest = resp.Header.Get("Docker-Content-Digest")
	if digest == "" {
		hash := sha256.Sum256(body)
		digest = "sha256:" + hex.EncodeToString(hash[:])
	}

	if err := h.storage.Put(ctx, cacheKey, body, digest, h.cfg.CacheTTL); err != nil {
		log.Printf("Failed to cache manifest: %v", err)
	}

	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.Header().Set("Docker-Content-Digest", digest)
	w.WriteHeader(resp.StatusCode)
	w.Write(body)
}

func (h *ProxyHandler) handleBlob(w http.ResponseWriter, r *http.Request, image, digest string) {
	ctx := r.Context()
	cacheKey := fmt.Sprintf("blobs/%s/%s", image, digest)

	content, _, err := h.storage.Get(ctx, cacheKey)
	if err == nil {
		if err := h.storage.UpdateLastAccess(ctx, cacheKey); err != nil {
			log.Printf("Failed to update last access time: %v", err)
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write(content)
		return
	}

	resp, err := h.dhClient.GetBlob(ctx, image, digest)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		headers := w.Header()
		for k, v := range resp.Header {
			headers[k] = v
		}
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
		return
	}

	var buf bytes.Buffer
	tee := io.TeeReader(resp.Body, &buf)

	if err := h.storage.PutStream(ctx, cacheKey, tee, digest, h.cfg.CacheTTL); err != nil {
		http.Error(w, "Caching failed", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	io.Copy(w, &buf)
}
