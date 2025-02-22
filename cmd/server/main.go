package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gorilla/mux"
	"github.com/sdko-org/registry-proxy/internal/config"
	"github.com/sdko-org/registry-proxy/internal/database"
	"github.com/sdko-org/registry-proxy/internal/dockerhub"
	"github.com/sdko-org/registry-proxy/internal/handlers"
	"github.com/sdko-org/registry-proxy/internal/models"
	"github.com/sdko-org/registry-proxy/internal/storage"
	"gorm.io/gorm"
)

func main() {
	cfg := config.Load()

	db, err := database.NewPostgresDB(database.PostgresConfig{
		User:     cfg.PostgresUser,
		Password: cfg.PostgresPassword,
		Host:     cfg.PostgresHost,
		Port:     cfg.PostgresPort,
		DBName:   cfg.PostgresDatabase,
		SSLMode:  cfg.PostgresSSLMode,
	})
	if err != nil {
		log.Fatalf("Database initialization failed: %v", err)
	}

	s3Storage := storage.NewS3Storage(cfg, db)
	dhClient := dockerhub.NewClient(cfg)

	handler := handlers.NewProxyHandler(cfg, s3Storage, dhClient)

	r := mux.NewRouter()
	r.Use(handlers.LoggingMiddleware(db))

	r.HandleFunc("/v2/", handlers.HandleV2Check).Methods("GET")
	r.PathPrefix("/v2/").Handler(handler)

	go startCachePurger(context.Background(), db, s3Storage, cfg)

	server := &http.Server{
		Addr:         ":8080",
		Handler:      r,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
	}

	go func() {
		sigint := make(chan os.Signal, 1)
		signal.Notify(sigint, syscall.SIGINT, syscall.SIGTERM)
		<-sigint

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		if err := server.Shutdown(ctx); err != nil {
			log.Printf("Server shutdown error: %v", err)
		}
	}()

	log.Printf("Starting server on %s", server.Addr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("Server failed: %v", err)
	}
}

func startCachePurger(ctx context.Context, db *gorm.DB, storage storage.Storage, cfg *config.Config) {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			purgeExpiredCache(ctx, db, storage)
		case <-ctx.Done():
			return
		}
	}
}

func purgeExpiredCache(ctx context.Context, db *gorm.DB, storage storage.Storage) {
	var entries []models.CacheEntry
	now := time.Now()

	if err := db.Where("expires_at < ? OR last_access < ?",
		now, now.Add(-7*24*time.Hour)).Find(&entries).Error; err != nil {
		log.Printf("Cache purge query failed: %v", err)
		return
	}

	for _, entry := range entries {
		if err := storage.Delete(ctx, entry.Key); err != nil {
			log.Printf("Failed to delete cache key %s: %v", entry.Key, err)
			continue
		}
		db.Delete(&entry)
	}
}
