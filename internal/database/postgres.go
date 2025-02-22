package database

import (
	"fmt"
	"time"

	"github.com/sdko-org/registry-proxy/internal/models"
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

func NewPostgresDB(cfg PostgresConfig) (*gorm.DB, error) {
	dsn := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
		cfg.Host, cfg.Port, cfg.User, cfg.Password, cfg.DBName, cfg.SSLMode)

	var db *gorm.DB
	var err error

	for i := 0; i < 15; i++ {
		db, err = gorm.Open(postgres.Open(dsn), &gorm.Config{})
		if err == nil {
			break
		}

		fmt.Printf("Database connection failed (attempt %d): %v\n", i+1, err)
		time.Sleep(time.Duration(2+i) * time.Second)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to connect to database after multiple attempts: %w", err)
	}

	if err := db.AutoMigrate(&models.AccessLog{}, &models.CacheEntry{}); err != nil {
		return nil, fmt.Errorf("failed to migrate models: %w", err)
	}

	return db, nil
}
