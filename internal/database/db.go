package database

import (
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"microserver/internal/models"
)

var DB *gorm.DB

// Open connects to SQLite in WAL mode. busy_timeout lets the job workers and
// HTTP handlers write concurrently without "database is locked" errors.
func Open(path string) (*gorm.DB, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)", path)
	lg := logger.New(log.Default(), logger.Config{
		SlowThreshold:             500 * time.Millisecond,
		LogLevel:                  logger.Warn,
		IgnoreRecordNotFoundError: true,
	})
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: lg})
	if err != nil {
		return nil, err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	// SQLite allows a single writer; one connection avoids lock contention
	// entirely at the cost of serialising queries, which is fine at this scale.
	sqlDB.SetMaxOpenConns(1)
	DB = db
	return db, nil
}

func Migrate(db *gorm.DB) error {
	return db.AutoMigrate(models.All()...)
}

// SeedAdmin creates the first admin when the table is empty.
func SeedAdmin(db *gorm.DB, username, password string) error {
	var n int64
	if err := db.Model(&models.AdminUser{}).Count(&n).Error; err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	if password == "" {
		return errors.New("no admin exists yet: set ADMIN_PASSWORD for the first boot")
	}
	u := models.AdminUser{Username: username}
	if err := u.SetPassword(password); err != nil {
		return err
	}
	log.Printf("created admin %q", username)
	return db.Create(&u).Error
}
