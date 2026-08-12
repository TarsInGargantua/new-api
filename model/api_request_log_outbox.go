package model

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"

	"gorm.io/gorm"
)

const (
	apiRequestLogOutboxDefaultLeaseSeconds = 900
	apiRequestLogOutboxRetryMaxDelay       = 5 * time.Minute
)

// APIRequestLogOutbox is a durable, local handoff for a request log destined
// for REQUEST_LOG_SQL_DSN. It deliberately lives in DB (SQL_DSN), which should
// be in the same region as the gateway, rather than in the remote log database.
type APIRequestLogOutbox struct {
	Id          int   `json:"id" gorm:"primaryKey"`
	CreatedAt   int64 `json:"created_at" gorm:"bigint;index:idx_api_request_log_outboxes_pending,priority:2"`
	AvailableAt int64 `json:"available_at" gorm:"bigint;index:idx_api_request_log_outboxes_ready,priority:1;index:idx_api_request_log_outboxes_claim,priority:1"`
	LeaseUntil  int64 `json:"lease_until" gorm:"bigint;index;index:idx_api_request_log_outboxes_ready,priority:2;index:idx_api_request_log_outboxes_pending,priority:1;index:idx_api_request_log_outboxes_claim,priority:2"`
	// The claim index matches the worker predicate; the pending index supports
	// queue age/status reads without scanning the payload column.
	Attempts   int               `json:"attempts" gorm:"default:0"`
	RequestId  string            `json:"request_id,omitempty" gorm:"type:varchar(64);index"`
	UsageLogId int               `json:"usage_log_id,omitempty" gorm:"index"`
	Payload    APIRequestLogBody `json:"payload"`
	LastError  string            `json:"last_error,omitempty" gorm:"type:text"`
}

func (APIRequestLogOutbox) TableName() string {
	return "api_request_log_outboxes"
}

type apiRequestLogOutboxPayload struct {
	Log      APIRequestLog          `json:"log"`
	TurnMeta *APIRequestLogTurnMeta `json:"turn_meta,omitempty"`
}

type APIRequestLogOutboxStatus struct {
	Enabled                  bool  `json:"enabled"`
	Workers                  int   `json:"workers"`
	BatchSize                int   `json:"batch_size"`
	Pending                  int64 `json:"pending"`
	Processing               int64 `json:"processing"`
	OldestPendingAt          int64 `json:"oldest_pending_at,omitempty"`
	OldestPendingAgeSecs     int64 `json:"oldest_pending_age_seconds,omitempty"`
	NewestPendingAt          int64 `json:"newest_pending_at,omitempty"`
	LastErrorAt              int64 `json:"last_error_at,omitempty"`
	SyncedSinceStart         int64 `json:"synced_since_start"`
	FailedAttemptsSinceStart int64 `json:"failed_attempts_since_start"`
}

type apiRequestLogOutboxTimestamp struct {
	Id        int   `gorm:"column:id"`
	CreatedAt int64 `gorm:"column:created_at"`
}

var apiRequestLogOutboxWorkerMu sync.Mutex
var apiRequestLogOutboxWorkersStarted bool
var apiRequestLogOutboxEnsureMu sync.Mutex
var apiRequestLogOutboxEnsuredDB *gorm.DB
var apiRequestLogOutboxSynced int64
var apiRequestLogOutboxFailedAttempts int64
var apiRequestLogOutboxLastErrorAt int64

func EnsureAPIRequestLogOutboxTable() error {
	if DB == nil {
		return errors.New("primary database is not initialized for request-log outbox")
	}
	apiRequestLogOutboxEnsureMu.Lock()
	defer apiRequestLogOutboxEnsureMu.Unlock()
	if apiRequestLogOutboxEnsuredDB == DB {
		return nil
	}
	if err := DB.AutoMigrate(&APIRequestLogOutbox{}); err != nil {
		return err
	}
	apiRequestLogOutboxEnsuredDB = DB
	return nil
}

func enqueueAPIRequestLogOutbox(log *APIRequestLog) error {
	if log == nil {
		return nil
	}
	if err := EnsureAPIRequestLogOutboxTable(); err != nil {
		return err
	}
	payload, err := common.Marshal(apiRequestLogOutboxPayload{
		Log:      *log,
		TurnMeta: cloneAPIRequestLogTurnMeta(log.TurnMeta),
	})
	if err != nil {
		return fmt.Errorf("marshal request-log outbox payload: %w", err)
	}
	now := common.GetTimestamp()
	return DB.Create(&APIRequestLogOutbox{
		CreatedAt:   now,
		AvailableAt: now,
		RequestId:   log.RequestId,
		UsageLogId:  log.UsageLogId,
		Payload:     APIRequestLogBody(payload),
	}).Error
}

func cloneAPIRequestLogTurnMeta(meta *APIRequestLogTurnMeta) *APIRequestLogTurnMeta {
	if meta == nil {
		return nil
	}
	copy := *meta
	copy.Items = append([]APIRequestLogTurnItemMeta(nil), meta.Items...)
	return &copy
}

func startAPIRequestLogOutboxWorkers() {
	if !common.APIRequestLogOutboxEnabled || common.GetEnvOrDefaultBool("REQUEST_LOG_DB_READ_ONLY", false) {
		return
	}
	apiRequestLogOutboxWorkerMu.Lock()
	defer apiRequestLogOutboxWorkerMu.Unlock()
	if apiRequestLogOutboxWorkersStarted {
		return
	}
	apiRequestLogOutboxWorkersStarted = true
	workers := common.APIRequestLogOutboxWorkers
	if workers < 1 {
		workers = 1
	}
	for i := 0; i < workers; i++ {
		go apiRequestLogOutboxWorker()
	}
}

func apiRequestLogOutboxWorker() {
	pollInterval := time.Duration(common.APIRequestLogOutboxPollIntervalMS) * time.Millisecond
	if pollInterval < 10*time.Millisecond {
		pollInterval = 10 * time.Millisecond
	}
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		if err := syncAPIRequestLogOutboxBatch(); err != nil {
			setAPIRequestLogLastWriteError(err)
		}
		<-ticker.C
	}
}

func syncAPIRequestLogOutboxBatch() error {
	if !common.APIRequestLogOutboxEnabled || DB == nil || requestLogDB() == nil {
		return nil
	}
	batchSize := common.APIRequestLogOutboxBatchSize
	if batchSize < 1 {
		batchSize = 1
	}
	jobs, err := claimAPIRequestLogOutboxBatch(DB, common.GetTimestamp(), batchSize)
	if err != nil {
		return err
	}
	var firstErr error
	for i := range jobs {
		if err := syncAPIRequestLogOutboxJob(DB, &jobs[i]); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func claimAPIRequestLogOutboxBatch(db *gorm.DB, now int64, batchSize int) ([]APIRequestLogOutbox, error) {
	if db == nil || batchSize < 1 {
		return nil, nil
	}
	var candidates []APIRequestLogOutbox
	if err := db.Where("available_at <= ? AND lease_until < ?", now, now).
		Order("id asc").Limit(batchSize).Find(&candidates).Error; err != nil {
		return nil, err
	}
	leaseSeconds := common.APIRequestLogOutboxLeaseSeconds
	if leaseSeconds < 30 {
		leaseSeconds = apiRequestLogOutboxDefaultLeaseSeconds
	}
	leaseUntil := now + int64(leaseSeconds)
	claimed := make([]APIRequestLogOutbox, 0, len(candidates))
	for _, candidate := range candidates {
		result := db.Model(&APIRequestLogOutbox{}).
			Where("id = ? AND available_at <= ? AND lease_until < ?", candidate.Id, now, now).
			Updates(map[string]interface{}{
				"lease_until": leaseUntil,
				"attempts":    gorm.Expr("attempts + ?", 1),
			})
		if result.Error != nil {
			return nil, result.Error
		}
		if result.RowsAffected == 0 {
			continue
		}
		candidate.LeaseUntil = leaseUntil
		candidate.Attempts++
		claimed = append(claimed, candidate)
	}
	return claimed, nil
}

func syncAPIRequestLogOutboxJob(db *gorm.DB, job *APIRequestLogOutbox) error {
	if db == nil || job == nil || job.Id <= 0 {
		return nil
	}
	var payload apiRequestLogOutboxPayload
	if err := common.Unmarshal([]byte(job.Payload), &payload); err != nil {
		return releaseAPIRequestLogOutboxJob(db, job, fmt.Errorf("decode request-log outbox payload: %w", err))
	}
	payload.Log.TurnMeta = payload.TurnMeta
	if err := createOrUpdateAPIRequestLog(&payload.Log); err != nil {
		return releaseAPIRequestLogOutboxJob(db, job, err)
	}
	if err := db.Delete(&APIRequestLogOutbox{}, job.Id).Error; err != nil {
		return fmt.Errorf("ack request-log outbox job %d: %w", job.Id, err)
	}
	atomic.AddInt64(&apiRequestLogOutboxSynced, 1)
	setAPIRequestLogLastWriteError(nil)
	return nil
}

func releaseAPIRequestLogOutboxJob(db *gorm.DB, job *APIRequestLogOutbox, cause error) error {
	if db == nil || job == nil || job.Id <= 0 || cause == nil {
		return cause
	}
	atomic.AddInt64(&apiRequestLogOutboxFailedAttempts, 1)
	now := common.GetTimestamp()
	atomic.StoreInt64(&apiRequestLogOutboxLastErrorAt, now)
	delay := apiRequestLogOutboxRetryDelay(job.Attempts)
	message := cause.Error()
	if len(message) > 4096 {
		message = message[:4096]
	}
	result := db.Model(&APIRequestLogOutbox{}).Where("id = ?", job.Id).Updates(map[string]interface{}{
		"lease_until":  0,
		"available_at": now + int64(delay/time.Second),
		"last_error":   message,
	})
	if result.Error != nil {
		return fmt.Errorf("reschedule request-log outbox job %d after %w: %v", job.Id, cause, result.Error)
	}
	return cause
}

func apiRequestLogOutboxRetryDelay(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	delay := time.Second
	for i := 1; i < attempts && delay < apiRequestLogOutboxRetryMaxDelay; i++ {
		delay *= 2
	}
	if delay > apiRequestLogOutboxRetryMaxDelay {
		return apiRequestLogOutboxRetryMaxDelay
	}
	return delay
}

func GetAPIRequestLogOutboxStatus() (APIRequestLogOutboxStatus, error) {
	status := APIRequestLogOutboxStatus{
		Enabled:                  common.APIRequestLogOutboxEnabled,
		Workers:                  common.APIRequestLogOutboxWorkers,
		BatchSize:                common.APIRequestLogOutboxBatchSize,
		SyncedSinceStart:         atomic.LoadInt64(&apiRequestLogOutboxSynced),
		FailedAttemptsSinceStart: atomic.LoadInt64(&apiRequestLogOutboxFailedAttempts),
	}
	if !status.Enabled {
		return status, nil
	}
	if DB == nil {
		return status, errors.New("primary database is not initialized for request-log outbox")
	}
	now := common.GetTimestamp()
	if err := DB.Model(&APIRequestLogOutbox{}).Where("lease_until >= ?", now).Count(&status.Processing).Error; err != nil {
		return status, err
	}
	if err := DB.Model(&APIRequestLogOutbox{}).Where("lease_until < ?", now).Count(&status.Pending).Error; err != nil {
		return status, err
	}
	var oldest apiRequestLogOutboxTimestamp
	result := DB.Model(&APIRequestLogOutbox{}).
		Select("id, created_at").Where("lease_until < ?", now).
		Order("created_at asc, id asc").Limit(1).Find(&oldest)
	if result.Error != nil {
		return status, result.Error
	}
	if result.RowsAffected > 0 {
		status.OldestPendingAt = oldest.CreatedAt
		if oldest.CreatedAt > 0 && now > oldest.CreatedAt {
			status.OldestPendingAgeSecs = now - oldest.CreatedAt
		}
	}
	var newest apiRequestLogOutboxTimestamp
	result = DB.Model(&APIRequestLogOutbox{}).
		Select("id, created_at").Order("id desc").Limit(1).Find(&newest)
	if result.Error != nil {
		return status, result.Error
	}
	if result.RowsAffected > 0 {
		status.NewestPendingAt = newest.CreatedAt
	}
	status.LastErrorAt = atomic.LoadInt64(&apiRequestLogOutboxLastErrorAt)
	return status, nil
}

func resetAPIRequestLogOutboxWorkersForTest() {
	apiRequestLogOutboxWorkerMu.Lock()
	apiRequestLogOutboxWorkersStarted = false
	apiRequestLogOutboxWorkerMu.Unlock()
	apiRequestLogOutboxEnsureMu.Lock()
	apiRequestLogOutboxEnsuredDB = nil
	apiRequestLogOutboxEnsureMu.Unlock()
	atomic.StoreInt64(&apiRequestLogOutboxSynced, 0)
	atomic.StoreInt64(&apiRequestLogOutboxFailedAttempts, 0)
	atomic.StoreInt64(&apiRequestLogOutboxLastErrorAt, 0)
}
