package model

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/QuantumNous/new-api/common"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	APIRequestLogMaterializationPending    = "pending"
	APIRequestLogMaterializationProcessing = "processing"
	APIRequestLogMaterializationRetry      = "retry"
	APIRequestLogMaterializationOK         = "ok"

	apiRequestLogMaterializationDefaultLease  = 5 * time.Minute
	apiRequestLogMaterializationMaxRetryDelay = 5 * time.Minute
	apiRequestLogsTable                       = "api_request_logs"
	apiRequestLogItemsTable                   = "api_request_log_items"
)

type APIRequestLogMaterializationJob struct {
	Id            int64             `json:"id" gorm:"primaryKey"`
	LogId         int               `json:"log_id" gorm:"not null;uniqueIndex"`
	Status        string            `json:"status" gorm:"type:varchar(16);not null;index:idx_api_request_log_materialization_ready,priority:1;default:'pending'"`
	Attempts      int               `json:"attempts" gorm:"not null;default:0"`
	NextAttemptAt int64             `json:"next_attempt_at" gorm:"bigint;not null;index:idx_api_request_log_materialization_ready,priority:2;default:0"`
	LockedAt      int64             `json:"locked_at" gorm:"bigint;not null;default:0"`
	LeaseVersion  int               `json:"lease_version" gorm:"not null;default:0"`
	LastError     string            `json:"last_error,omitempty" gorm:"type:text"`
	TurnMeta      APIRequestLogBody `json:"-"`
	CreatedAt     int64             `json:"created_at" gorm:"autoCreateTime"`
	UpdatedAt     int64             `json:"updated_at" gorm:"autoUpdateTime"`
}

func (APIRequestLogMaterializationJob) TableName() string {
	return "api_request_log_materialization_jobs"
}

type APIRequestLogMaterializationQueueStatus struct {
	Pending                 int64 `json:"pending"`
	Processing              int64 `json:"processing"`
	Retry                   int64 `json:"retry"`
	OldestPending           int64 `json:"oldest_pending_at,omitempty"`
	OldestPendingAgeSeconds int64 `json:"oldest_pending_age_seconds,omitempty"`
}

type APIRequestLogMaterializationBackfillResult struct {
	Candidates int   `json:"candidates"`
	Scheduled  int64 `json:"scheduled"`
	HasMore    bool  `json:"has_more"`
}

type APIRequestLogMaterializationBacklogStatus struct {
	Recoverable                 int64 `json:"recoverable"`
	ParentOnly                  int64 `json:"parent_only"`
	OldestRecoverable           int64 `json:"oldest_recoverable_at,omitempty"`
	OldestRecoverableAgeSeconds int64 `json:"oldest_recoverable_age_seconds,omitempty"`
	OldestParentOnly            int64 `json:"oldest_parent_only_at,omitempty"`
	OldestParentOnlyAgeSeconds  int64 `json:"oldest_parent_only_age_seconds,omitempty"`
}

type APIRequestLogMaterializationWorkerOptions struct {
	Lease time.Duration
	Now   func() time.Time
}

func EnsureAPIRequestLogMaterializationJobTable(db *gorm.DB) error {
	if db == nil {
		return errors.New("request log database is not initialized")
	}
	return db.AutoMigrate(&APIRequestLogMaterializationJob{})
}

func enqueueAPIRequestLogMaterialization(db *gorm.DB, log *APIRequestLog) error {
	if db == nil || log == nil || log.Id <= 0 {
		return errors.New("persisted request log is required for turn materialization")
	}
	meta := APIRequestLogTurnMeta{Protocol: log.APIFormat}
	if log.TurnMeta != nil {
		meta = *log.TurnMeta
		if meta.Protocol == "" {
			meta.Protocol = log.APIFormat
		}
	}
	encoded, err := common.Marshal(meta)
	if err != nil {
		return fmt.Errorf("encode request log turn metadata: %w", err)
	}
	job := APIRequestLogMaterializationJob{
		LogId:    log.Id,
		Status:   APIRequestLogMaterializationPending,
		TurnMeta: APIRequestLogBody(encoded),
	}
	return db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "log_id"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"status":          APIRequestLogMaterializationPending,
			"next_attempt_at": 0,
			"locked_at":       0,
			"lease_version":   gorm.Expr("lease_version + 1"),
			"last_error":      "",
			"turn_meta":       APIRequestLogBody(encoded),
			"updated_at":      time.Now().Unix(),
		}),
	}).Create(&job).Error
}

func ScheduleAPIRequestLogMaterializationBacklog(ctx context.Context, db *gorm.DB, batchSize int) (APIRequestLogMaterializationBackfillResult, error) {
	var result APIRequestLogMaterializationBackfillResult
	if db == nil {
		return result, errors.New("request log database is not initialized")
	}
	if batchSize <= 0 {
		return result, errors.New("materialization backlog batch size must be greater than zero")
	}
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var logs []APIRequestLog
		itemExists := "EXISTS (SELECT 1 FROM " + apiRequestLogItemsTable + " backlog_item WHERE backlog_item.log_id = " + apiRequestLogsTable + ".id)"
		jobExists := "EXISTS (SELECT 1 FROM " + APIRequestLogMaterializationJob{}.TableName() + " backlog_job WHERE backlog_job.log_id = " + apiRequestLogsTable + ".id)"
		if err := tx.Select("id", "api_format").
			Where("items_status IN ?", []string{APIRequestLogItemsPending, APIRequestLogItemsFailed}).
			Where(itemExists).
			Where("NOT " + jobExists).
			Order("id ASC").
			Limit(batchSize + 1).
			Find(&logs).Error; err != nil {
			return err
		}
		if len(logs) > batchSize {
			result.HasMore = true
			logs = logs[:batchSize]
		}
		result.Candidates = len(logs)
		if len(logs) == 0 {
			return nil
		}
		jobs := make([]APIRequestLogMaterializationJob, 0, len(logs))
		logIds := make([]int, 0, len(logs))
		for index := range logs {
			meta, err := common.Marshal(APIRequestLogTurnMeta{Protocol: logs[index].APIFormat})
			if err != nil {
				return err
			}
			jobs = append(jobs, APIRequestLogMaterializationJob{
				LogId:    logs[index].Id,
				Status:   APIRequestLogMaterializationPending,
				TurnMeta: APIRequestLogBody(meta),
			})
			logIds = append(logIds, logs[index].Id)
		}
		create := tx.Clauses(clause.OnConflict{DoNothing: true}).CreateInBatches(jobs, batchSize)
		if create.Error != nil {
			return create.Error
		}
		result.Scheduled = create.RowsAffected
		return tx.Model(&APIRequestLog{}).
			Where("id IN ?", logIds).
			Updates(map[string]interface{}{"items_status": APIRequestLogItemsOK, "items_error": ""}).Error
	})
	return result, err
}

func GetAPIRequestLogMaterializationBacklogStatus(ctx context.Context, db *gorm.DB) (APIRequestLogMaterializationBacklogStatus, error) {
	var status APIRequestLogMaterializationBacklogStatus
	if db == nil {
		return status, errors.New("request log database is not initialized")
	}
	type backlogAggregate struct {
		Count  int64 `gorm:"column:count"`
		Oldest int64 `gorm:"column:oldest"`
	}
	itemExists := "EXISTS (SELECT 1 FROM " + apiRequestLogItemsTable + " backlog_item WHERE backlog_item.log_id = " + apiRequestLogsTable + ".id)"
	jobExists := "EXISTS (SELECT 1 FROM " + APIRequestLogMaterializationJob{}.TableName() + " backlog_job WHERE backlog_job.log_id = " + apiRequestLogsTable + ".id)"
	base := db.WithContext(ctx).Model(&APIRequestLog{}).
		Where("items_status IN ?", []string{APIRequestLogItemsPending, APIRequestLogItemsFailed})
	var recoverable backlogAggregate
	if err := base.Session(&gorm.Session{}).
		Where(itemExists).
		Where("NOT " + jobExists).
		Select("COUNT(*) AS count, COALESCE(MIN(created_at), 0) AS oldest").
		Scan(&recoverable).Error; err != nil {
		return status, err
	}
	var parentOnly backlogAggregate
	if err := base.Session(&gorm.Session{}).
		Where("NOT " + itemExists).
		Select("COUNT(*) AS count, COALESCE(MIN(created_at), 0) AS oldest").
		Scan(&parentOnly).Error; err != nil {
		return status, err
	}
	status.Recoverable = recoverable.Count
	status.ParentOnly = parentOnly.Count
	status.OldestRecoverable = recoverable.Oldest
	status.OldestParentOnly = parentOnly.Oldest
	nowUnix := time.Now().Unix()
	if status.OldestRecoverable > 0 && status.OldestRecoverable < nowUnix {
		status.OldestRecoverableAgeSeconds = nowUnix - status.OldestRecoverable
	}
	if status.OldestParentOnly > 0 && status.OldestParentOnly < nowUnix {
		status.OldestParentOnlyAgeSeconds = nowUnix - status.OldestParentOnly
	}
	return status, nil
}

func ClaimAPIRequestLogMaterializationJob(ctx context.Context, db *gorm.DB, options APIRequestLogMaterializationWorkerOptions) (*APIRequestLogMaterializationJob, error) {
	if db == nil {
		return nil, errors.New("request log database is not initialized")
	}
	now := time.Now
	if options.Now != nil {
		now = options.Now
	}
	lease := options.Lease
	if lease <= 0 {
		lease = apiRequestLogMaterializationDefaultLease
	}
	nowUnix := now().Unix()
	staleBefore := nowUnix - int64(lease/time.Second)
	var claimed APIRequestLogMaterializationJob
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		query := tx.Where(
			"((status = ? OR status = ?) AND next_attempt_at <= ?) OR (status = ? AND locked_at <= ?)",
			APIRequestLogMaterializationPending,
			APIRequestLogMaterializationRetry,
			nowUnix,
			APIRequestLogMaterializationProcessing,
			staleBefore,
		).Order("log_id ASC")
		if tx.Dialector != nil && tx.Dialector.Name() != "sqlite" {
			locking := clause.Locking{Strength: "UPDATE"}
			if tx.Dialector.Name() == "postgres" {
				locking.Options = "SKIP LOCKED"
			}
			query = query.Clauses(locking)
		}
		if err := query.First(&claimed).Error; err != nil {
			return err
		}
		claimed.Status = APIRequestLogMaterializationProcessing
		claimed.Attempts++
		claimed.LockedAt = nowUnix
		claimed.LeaseVersion++
		claimed.LastError = ""
		return tx.Model(&APIRequestLogMaterializationJob{}).
			Where("id = ?", claimed.Id).
			Updates(map[string]interface{}{
				"status":        claimed.Status,
				"attempts":      claimed.Attempts,
				"locked_at":     claimed.LockedAt,
				"lease_version": claimed.LeaseVersion,
				"last_error":    "",
			}).Error
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &claimed, nil
}

func ProcessAPIRequestLogMaterializationJob(ctx context.Context, db *gorm.DB, job *APIRequestLogMaterializationJob, now func() time.Time) error {
	if db == nil || job == nil || job.Id <= 0 {
		return errors.New("claimed request log materialization job is required")
	}
	var log APIRequestLog
	if err := db.WithContext(ctx).First(&log, job.LogId).Error; err != nil {
		return retryAPIRequestLogMaterializationJob(db, job, err, now)
	}
	var items []APIRequestLogItem
	if err := db.WithContext(ctx).Where("log_id = ?", log.Id).Order("seq ASC").Order("id ASC").Find(&items).Error; err != nil {
		return retryAPIRequestLogMaterializationJob(db, job, err, now)
	}
	meta := APIRequestLogTurnMeta{Protocol: log.APIFormat}
	if len(job.TurnMeta) > 0 {
		if err := common.Unmarshal([]byte(job.TurnMeta), &meta); err != nil {
			return retryAPIRequestLogMaterializationJob(db, job, fmt.Errorf("decode request log turn metadata: %w", err), now)
		}
	}
	if _, err := MaterializeAPIRequestLogTurn(db.WithContext(ctx), &log, meta, items); err != nil {
		return retryAPIRequestLogMaterializationJob(db, job, err, now)
	}
	result := db.Model(&APIRequestLogMaterializationJob{}).
		Where("id = ? AND lease_version = ?", job.Id, job.LeaseVersion).
		Updates(map[string]interface{}{
			"status":          APIRequestLogMaterializationOK,
			"next_attempt_at": 0,
			"locked_at":       0,
			"last_error":      "",
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return errors.New("request log materialization lease is no longer current")
	}
	return nil
}

func retryAPIRequestLogMaterializationJob(db *gorm.DB, job *APIRequestLogMaterializationJob, cause error, now func() time.Time) error {
	if now == nil {
		now = time.Now
	}
	delay := apiRequestLogMaterializationRetryDelay(job.Attempts)
	result := db.Model(&APIRequestLogMaterializationJob{}).
		Where("id = ? AND lease_version = ?", job.Id, job.LeaseVersion).
		Updates(map[string]interface{}{
			"status":          APIRequestLogMaterializationRetry,
			"next_attempt_at": now().Add(delay).Unix(),
			"locked_at":       0,
			"last_error":      cause.Error(),
		})
	if result.Error != nil {
		return fmt.Errorf("materialize request log turn: %v; schedule retry: %w", cause, result.Error)
	}
	return fmt.Errorf("materialize request log turn: %w", cause)
}

func apiRequestLogMaterializationRetryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := time.Second << min(attempt-1, 8)
	if delay > apiRequestLogMaterializationMaxRetryDelay {
		return apiRequestLogMaterializationMaxRetryDelay
	}
	return delay
}

func GetAPIRequestLogMaterializationQueueStatus(db *gorm.DB) (APIRequestLogMaterializationQueueStatus, error) {
	var status APIRequestLogMaterializationQueueStatus
	if db == nil || !db.Migrator().HasTable(&APIRequestLogMaterializationJob{}) {
		return status, nil
	}
	type countRow struct {
		Status string
		Count  int64
	}
	var counts []countRow
	if err := db.Model(&APIRequestLogMaterializationJob{}).
		Select("status, count(*) AS count").
		Where("status IN ?", []string{
			APIRequestLogMaterializationPending,
			APIRequestLogMaterializationProcessing,
			APIRequestLogMaterializationRetry,
		}).
		Group("status").Scan(&counts).Error; err != nil {
		return status, err
	}
	for _, row := range counts {
		switch row.Status {
		case APIRequestLogMaterializationPending:
			status.Pending = row.Count
		case APIRequestLogMaterializationProcessing:
			status.Processing = row.Count
		case APIRequestLogMaterializationRetry:
			status.Retry = row.Count
		}
	}
	if err := db.Model(&APIRequestLogMaterializationJob{}).
		Where("status IN ?", []string{APIRequestLogMaterializationPending, APIRequestLogMaterializationRetry}).
		Select("COALESCE(MIN(created_at), 0)").Scan(&status.OldestPending).Error; err != nil {
		return status, err
	}
	if status.OldestPending > 0 {
		status.OldestPendingAgeSeconds = time.Now().Unix() - status.OldestPending
		if status.OldestPendingAgeSeconds < 0 {
			status.OldestPendingAgeSeconds = 0
		}
	}
	return status, nil
}
