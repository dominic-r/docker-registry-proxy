package models

import (
	"time"
)

type AccessLog struct {
	ID        uint      `gorm:"primaryKey;autoIncrement"`
	Timestamp time.Time `gorm:"index;not null"`
	Method    string    `gorm:"type:varchar(10);not null"`
	Path      string    `gorm:"type:text;not null;index:,length:256"`
	Status    int       `gorm:"not null;index"`
	Duration  time.Duration
	ClientIP  string `gorm:"type:varchar(45);not null"`
	UserAgent string `gorm:"type:text"`
	BytesSent int    `gorm:"not null;default:0"`
}

type RegistryCache struct {
	Key          string    `gorm:"primaryKey;type:varchar(512);not null"`
	Type         string    `gorm:"type:varchar(20);not null;index"`
	Digest       string    `gorm:"type:varchar(128);not null"`
	MediaType    string    `gorm:"type:varchar(128);not null"`
	StoredAt     time.Time `gorm:"index;not null"`
	ExpiresAt    time.Time `gorm:"index;not null"`
	LastAccess   time.Time `gorm:"index;not null"`
	SizeBytes    int64     `gorm:"not null;default:-1"`
	LastModified time.Time `gorm:"index"`
	ETag         string    `gorm:"type:varchar(128)"`
}

type TagCache struct {
	ID           uint      `gorm:"primaryKey;autoIncrement"`
	Repository   string    `gorm:"type:varchar(255);not null;index"`
	Tags         string    `gorm:"type:text;not null"`
	ETag         string    `gorm:"type:varchar(128);not null"`
	LastModified time.Time `gorm:"index;not null"`
	ExpiresAt    time.Time `gorm:"index;not null"`
	StoredAt     time.Time `gorm:"index;not null"`
}

func (RegistryCache) TableName() string {
	return "registry_cache"
}

func (TagCache) TableName() string {
	return "tag_cache"
}

func (AccessLog) TableName() string {
	return "access_logs"
}
