package database

import (
	"fmt"
	"time"

	"github.com/sdko-org/registry-proxy/internal/models"
	"github.com/sirupsen/logrus"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

type PostgresConfig struct {
	User     string
	Password string
	Host     string
	Port     string
	DBName   string
	SSLMode  string
}

func NewPostgresDB(logger *logrus.Logger, cfg PostgresConfig) (*gorm.DB, error) {
	dsn := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
		cfg.Host, cfg.Port, cfg.User, cfg.Password, cfg.DBName, cfg.SSLMode)

	log := logger.WithFields(logrus.Fields{
		"component": "database",
		"host":      cfg.Host,
		"database":  cfg.DBName,
	})

	var db *gorm.DB
	var err error
	const maxRetries = 5
	retryDelay := 2 * time.Second

	for attempt := 1; attempt <= maxRetries; attempt++ {
		db, err = gorm.Open(postgres.Open(dsn), &gorm.Config{})
		if err == nil {
			break
		}

		log.WithFields(logrus.Fields{
			"attempt": attempt,
			"error":   err,
		}).Warn("Database connection failed")

		if attempt < maxRetries {
			time.Sleep(retryDelay)
			retryDelay *= 2
		}
	}

	if err != nil {
		log.WithError(err).Error("Failed to connect to database after retries")
		return nil, fmt.Errorf("database connection failed: %w", err)
	}

	if err := db.AutoMigrate(&models.AccessLog{}, &models.RegistryCache{}, &models.TagCache{}); err != nil {
		log.WithError(err).Error("Database migration failed")
		return nil, fmt.Errorf("database migration failed: %w", err)
	}

	log.Info("Database connection established")
	return db, nil
}
