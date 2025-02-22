package models

import (
	"time"
)

type AccessLog struct {
	ID        uint      `gorm:"primaryKey"`
	Timestamp time.Time `gorm:"index"`
	Method    string
	Path      string `gorm:"index"`
	Status    int
	Duration  time.Duration
	ClientIP  string
	UserAgent string
	BytesSent int
}

type CacheEntry struct {
	Key        string    `gorm:"primaryKey;size:512"`
	Digest     string    `gorm:"size:128"`
	StoredAt   time.Time `gorm:"index"`
	ExpiresAt  time.Time `gorm:"index"`
	LastAccess time.Time `gorm:"index"`
	SizeBytes  int64
}
