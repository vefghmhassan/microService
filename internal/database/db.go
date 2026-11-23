package database

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/plugin/dbresolver"

	"microserver/internal/models"
)

var DB *gorm.DB

// ReadConns is how many connections serve reads at the same time.
const ReadConns = 4

// Open connects to SQLite in WAL mode with two pools: one connection for
// writes and transactions, and ReadConns read-only ones for queries.
//
// With a single connection for everything, one slow query (a report over a
// large table) made every page and job wait behind it. WAL lets readers run
// beside the writer, so plain reads (Find, Count, Raw SELECT) go to the read
// pool, and writes keep one connection: they queue in Go instead of failing
// with "database is locked".
func Open(path string) (*gorm.DB, error) {
	base := fmt.Sprintf("file:%s?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)", path)
	lg := logger.New(log.Default(), logger.Config{
		SlowThreshold:             500 * time.Millisecond,
		LogLevel:                  logger.Warn,
		IgnoreRecordNotFoundError: true,
	})
	db, err := gorm.Open(sqlite.Open(base), &gorm.Config{Logger: lg})
	if err != nil {
		return nil, err
	}
	writer, err := db.DB()
	if err != nil {
		return nil, err
	}
	writer.SetMaxOpenConns(1)

	// query_only makes a write that reached the read pool by mistake fail
	// at once instead of racing the writer.
	reader, err := sql.Open(sqlite.DriverName, base+"&_pragma=query_only(1)")
	if err != nil {
		return nil, err
	}
	reader.SetMaxOpenConns(ReadConns)
	reader.SetMaxIdleConns(ReadConns)
	if err := db.Use(dbresolver.Register(dbresolver.Config{
		Replicas: []gorm.Dialector{&sqlite.Dialector{Conn: reader}},
		Policy:   dbresolver.RandomPolicy{},
	})); err != nil {
		reader.Close()
		return nil, err
	}
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
