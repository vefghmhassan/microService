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
	// The inbound port became unique per IP family; drop the old index.
	if m := db.Migrator(); m.HasIndex(&models.InboundTemplate{}, "idx_inbound_templates_port") {
		if err := m.DropIndex(&models.InboundTemplate{}, "idx_inbound_templates_port"); err != nil {
			return err
		}
	}
	// Balancer configs are keyed by their mode now, not by location.
	if m := db.Migrator(); m.HasTable(&models.NodeUpload{}) && m.HasIndex(&models.NodeUpload{}, "idx_node_upload") {
		if err := m.DropIndex(&models.NodeUpload{}, "idx_node_upload"); err != nil {
			return err
		}
	}
	if err := db.AutoMigrate(models.All()...); err != nil {
		return err
	}
	// Servers from before ip_since: their IP dates from their last finished
	// IP rotation still in the history, else from when they were built.
	return db.Exec(`UPDATE servers SET ip_since = COALESCE(
		(SELECT MAX(j.finished_at) FROM jobs j WHERE j.server_id = servers.id AND j.kind = 'rotate' AND j.status = 'done'),
		created_at) WHERE ip_since IS NULL`).Error
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
