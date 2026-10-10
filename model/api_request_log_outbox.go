package model

import (
	"context"
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
	CapturePaused             bool  `json:"capture_paused"`
	SkippedSinceStart         int64 `json:"skipped_since_start"`
	RetryAt                   int64 `json:"retry_at,omitempty"`
	EnqueueFailuresSinceStart int64 `json:"enqueue_failures_since_start"`
	Enabled                   bool  `json:"enabled"`
	Workers                   int   `json:"workers"`
	BatchSize                 int   `json:"batch_size"`
	Pending                   int64 `json:"pending"`
	Processing                int64 `json:"processing"`
	OldestPendingAt           int64 `json:"oldest_pending_at,omitempty"`
	OldestPendingAgeSecs      int64 `json:"oldest_pending_age_seconds,omitempty"`
	NewestPendingAt           int64 `json:"newest_pending_at,omitempty"`
	LastErrorAt               int64 `json:"last_error_at,omitempty"`
	SyncedSinceStart          int64 `json:"synced_since_start"`
	FailedAttemptsSinceStart  int64 `json:"failed_attempts_since_start"`
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
	db := requestLogEnqueueDB()
	if db == nil {
		return errors.New("primary database is not initialized for request-log outbox")
	}
	apiRequestLogOutboxEnsureMu.Lock()
	defer apiRequestLogOutboxEnsureMu.Unlock()
	if apiRequestLogOutboxEnsuredDB == db {
		return nil
	}
	ctx, cancel := requestLogLocalContext()
	defer cancel()
	if err := db.WithContext(ctx).AutoMigrate(&APIRequestLogOutbox{}); err != nil {
		return err
	}
	apiRequestLogOutboxEnsuredDB = db
	return nil
}

func enqueueAPIRequestLogOutbox(log *APIRequestLog) error {
	if log == nil {
		return nil
	}
	if SkipUnavailableAPIRequestLogCapture() {
		return ErrAPIRequestLogCapturePaused
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
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	err = requestLogEnqueueDB().WithContext(ctx).Create(&APIRequestLogOutbox{
		CreatedAt:   now,
		AvailableAt: now,
		RequestId:   log.RequestId,
		UsageLogId:  log.UsageLogId,
		Payload:     APIRequestLogBody(payload),
	}).Error
	if err != nil {
		atomic.AddInt64(&apiRequestLogOutboxEnqueueFailures, 1)
	}
	return err
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
		runAPIRequestLogOutboxWorkerBatch()
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
	db := requestLogDeliveryDB()
	// Check the destination before touching the primary queue. A failed tunnel
	// must not cause endless queue scans and row updates on the business DB.
	remotePool, err := requestLogDB().DB()
	if err != nil {
		return err
	}
	probeCtx, probeCancel := context.WithTimeout(context.Background(), 3*time.Second)
	err = remotePool.PingContext(probeCtx)
	probeCancel()
	if err != nil {
		return err
	}
	jobs, err := claimAPIRequestLogOutboxBatch(db, common.GetTimestamp(), batchSize)
	if err != nil {
		return err
	}
	for i := range jobs {
		if err := syncAPIRequestLogOutboxJob(db, &jobs[i]); err != nil {
			// Unprocessed jobs remain durable; their leases expire for retry.
			return err
		}
	}
	return nil
}

func claimAPIRequestLogOutboxBatch(db *gorm.DB, now int64, batchSize int) ([]APIRequestLogOutbox, error) {
	if db == nil || batchSize < 1 {
		return nil, nil
	}
	ctx, cancel := requestLogLocalContext()
	defer cancel()
	db = db.WithContext(ctx)
	var candidates []APIRequestLogOutbox
	if err := db.Select("id", "attempts").Where("available_at <= ? AND lease_until < ?", now, now).
		Order("id asc").Limit(batchSize).Find(&candidates).Error; err != nil {
		return nil, err
	}
	leaseSeconds := common.APIRequestLogOutboxLeaseSeconds
	if leaseSeconds < 30 {
		leaseSeconds = apiRequestLogOutboxDefaultLeaseSeconds
	}
	// Jobs in a claimed batch are delivered sequentially with a 60s deadline.
	// The last job must still own its lease when delivery finishes.
	if minimum := batchSize*60 + 30; leaseSeconds < minimum {
		leaseSeconds = minimum
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
		// Fetch the large payload only after owning the lease.
		if err := db.First(&candidate, candidate.Id).Error; err != nil {
			return nil, err
		}
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
	remoteCtx, remoteCancel := context.WithTimeout(context.Background(), 60*time.Second)
	err := createOrUpdateAPIRequestLogOnDB(requestLogDB().WithContext(remoteCtx), &payload.Log, materializeAPIRequestLogTurnForWrite)
	remoteCancel()
	if err != nil {
		return releaseAPIRequestLogOutboxJob(db, job, err)
	}
	ctx, cancel := requestLogLocalContext()
	defer cancel()
	if err := db.WithContext(ctx).Where("id = ? AND lease_until = ? AND attempts = ?", job.Id, job.LeaseUntil, job.Attempts).
		Delete(&APIRequestLogOutbox{}).Error; err != nil {
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
	ctx, cancel := requestLogLocalContext()
	defer cancel()
	result := db.WithContext(ctx).Model(&APIRequestLogOutbox{}).
		Where("id = ? AND lease_until = ? AND attempts = ?", job.Id, job.LeaseUntil, job.Attempts).Updates(map[string]interface{}{
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
		CapturePaused:             APIRequestLogCapturePaused(),
		SkippedSinceStart:         atomic.LoadInt64(&apiRequestLogOutboxSkipped),
		RetryAt:                   atomic.LoadInt64(&apiRequestLogOutboxRetryAt) / int64(time.Second),
		EnqueueFailuresSinceStart: atomic.LoadInt64(&apiRequestLogOutboxEnqueueFailures),
		Enabled:                   common.APIRequestLogOutboxEnabled,
		Workers:                   common.APIRequestLogOutboxWorkers,
		BatchSize:                 common.APIRequestLogOutboxBatchSize,
		SyncedSinceStart:          atomic.LoadInt64(&apiRequestLogOutboxSynced),
		FailedAttemptsSinceStart:  atomic.LoadInt64(&apiRequestLogOutboxFailedAttempts),
	}
	if !status.Enabled {
		return status, nil
	}
	if DB == nil {
		return status, errors.New("primary database is not initialized for request-log outbox")
	}
	ctx, cancel := requestLogLocalContext()
	defer cancel()
	db := requestLogDeliveryDB().WithContext(ctx)
	now := common.GetTimestamp()
	if err := db.Model(&APIRequestLogOutbox{}).Where("lease_until >= ?", now).Count(&status.Processing).Error; err != nil {
		return status, err
	}
	if err := db.Model(&APIRequestLogOutbox{}).Where("lease_until < ?", now).Count(&status.Pending).Error; err != nil {
		return status, err
	}
	var oldest apiRequestLogOutboxTimestamp
	result := db.Model(&APIRequestLogOutbox{}).
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
	result = db.Model(&APIRequestLogOutbox{}).
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
	atomic.StoreInt64(&apiRequestLogOutboxRetryAt, 0)
	atomic.StoreInt64(&apiRequestLogOutboxEnqueueFailures, 0)
	atomic.StoreInt32(&apiRequestLogDeliveryUnhealthy, 0)
	atomic.StoreInt64(&apiRequestLogOutboxSkipped, 0)
	apiRequestLogOutboxFailures = 0
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
