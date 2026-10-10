package model

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	drivermysql "github.com/go-sql-driver/mysql"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// These pools use the primary database but never borrow business connections.
// Keep enqueue and delivery separate so a stalled claim cannot starve capture.
var apiRequestLogEnqueueDB, apiRequestLogDeliveryDB *gorm.DB
var apiRequestLogOutboxBatchMu sync.Mutex
var apiRequestLogOutboxRetryAt int64
var apiRequestLogOutboxEnqueueFailures int64
var apiRequestLogOutboxFailures int
var apiRequestLogDeliveryUnhealthy int32
var apiRequestLogOutboxSkipped int64

var ErrAPIRequestLogCapturePaused = errors.New("request-log capture paused while backup delivery is unavailable")

// Availability takes priority over raw-content capture during an outage.
// Existing durable jobs stay queued, but new payloads must not fill the
// primary database's disk while its destination is unreachable.
func APIRequestLogCapturePaused() bool {
	return common.APIRequestLogOutboxEnabled && atomic.LoadInt32(&apiRequestLogDeliveryUnhealthy) != 0
}

func SkipUnavailableAPIRequestLogCapture() bool {
	if !APIRequestLogCapturePaused() {
		return false
	}
	atomic.AddInt64(&apiRequestLogOutboxSkipped, 1)
	return true
}

const apiRequestLogOutboxLocalTimeout = 2 * time.Second

// Open lazily: neither GORM's automatic ping nor MySQL's version query may
// make the gateway's startup depend on the remote backup server.
func openIsolatedRequestLogDB(dsn string) (*gorm.DB, error) {
	config := &gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)}
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		return gorm.Open(postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true}), config)
	}
	if dsn == "" || strings.HasPrefix(dsn, "local") {
		return gorm.Open(sqlite.Open(common.SQLitePath), config)
	}
	cfg, err := drivermysql.ParseDSN(ensureMySQLDSNDefaults(dsn))
	if err != nil {
		return nil, errors.New("invalid request-log database DSN")
	}
	// Context deadlines cover whole operations; these also bound handshakes
	// and I/O when a driver operation has no caller context.
	if cfg.Timeout == 0 || cfg.Timeout > 3*time.Second {
		cfg.Timeout = 3 * time.Second
	}
	if cfg.ReadTimeout == 0 || cfg.ReadTimeout > 30*time.Second {
		cfg.ReadTimeout = 30 * time.Second
	}
	if cfg.WriteTimeout == 0 || cfg.WriteTimeout > 30*time.Second {
		cfg.WriteTimeout = 30 * time.Second
	}
	return gorm.Open(mysql.New(mysql.Config{DSN: cfg.FormatDSN(), SkipInitializeWithVersion: true}), config)
}

func initAPIRequestLogOutboxPools() error {
	dsn := resolveConfiguredDSN("SQL_DSN")
	enqueue, err := openIsolatedRequestLogDB(dsn)
	if err != nil {
		return err
	}
	delivery, err := openIsolatedRequestLogDB(dsn)
	if err != nil {
		_ = closeDB(enqueue)
		return err
	}
	for db, size := range map[*gorm.DB]int{enqueue: 2, delivery: 1} {
		pool, err := db.DB()
		if err != nil {
			_ = closeDB(enqueue)
			_ = closeDB(delivery)
			return err
		}
		pool.SetMaxOpenConns(size)
		pool.SetMaxIdleConns(1)
		pool.SetConnMaxLifetime(5 * time.Minute)
	}
	apiRequestLogEnqueueDB, apiRequestLogDeliveryDB = enqueue, delivery
	return nil
}

func requestLogEnqueueDB() *gorm.DB {
	if apiRequestLogEnqueueDB != nil {
		return apiRequestLogEnqueueDB
	}
	return DB // Embedded callers/tests that do not run InitRequestLogDB.
}

func requestLogDeliveryDB() *gorm.DB {
	if apiRequestLogDeliveryDB != nil {
		return apiRequestLogDeliveryDB
	}
	return DB
}

// All configured workers share one failure backoff and one delivery batch.
// This prevents workers from repeatedly loading/locking the same large rows
// during an outage. No local transaction is held while writing remotely.
func runAPIRequestLogOutboxWorkerBatch() {
	if !apiRequestLogOutboxBatchMu.TryLock() {
		return
	}
	defer apiRequestLogOutboxBatchMu.Unlock()
	if time.Now().UnixNano() < atomic.LoadInt64(&apiRequestLogOutboxRetryAt) {
		return
	}
	if err := syncAPIRequestLogOutboxBatch(); err != nil {
		atomic.StoreInt32(&apiRequestLogDeliveryUnhealthy, 1)
		apiRequestLogOutboxFailures++
		delay := apiRequestLogOutboxRetryDelay(apiRequestLogOutboxFailures + 3)
		atomic.StoreInt64(&apiRequestLogOutboxRetryAt, time.Now().Add(delay).UnixNano())
		setAPIRequestLogLastWriteError(err)
		common.SysError("request-log delivery paused; relay remains available: " + err.Error())
		return
	}
	apiRequestLogOutboxFailures = 0
	atomic.StoreInt32(&apiRequestLogDeliveryUnhealthy, 0)
	atomic.StoreInt64(&apiRequestLogOutboxRetryAt, 0)
}

func requestLogLocalContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), apiRequestLogOutboxLocalTimeout)
}
