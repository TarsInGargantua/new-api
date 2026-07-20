package model

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

const (
	APIRequestLogSchemaVersion = 1
	apiRequestLogItemBatchSize = 20
	apiRequestLogItemMaxRetry  = 3

	APIRequestLogSourceLive     = "live"
	APIRequestLogSourceLegacy   = "legacy_api_request_logs"
	APIRequestLogParseOK        = "ok"
	APIRequestLogParsePartial   = "partial"
	APIRequestLogParseFailed    = "failed"
	APIRequestLogPhaseInput     = "input"
	APIRequestLogPhaseOutput    = "output"
	APIRequestLogItemMessage    = "message"
	APIRequestLogItemReasoning  = "reasoning"
	APIRequestLogItemToolSpec   = "tool_spec"
	APIRequestLogItemToolCall   = "tool_call"
	APIRequestLogItemToolResult = "tool_result"
	APIRequestLogItemError      = "error"
	APIRequestLogItemRaw        = "raw_unparsed"
	APIRequestLogItemsEmpty     = "empty"
	APIRequestLogItemsPending   = "pending"
	APIRequestLogItemsOK        = "ok"
	APIRequestLogItemsFailed    = "failed"
)

type APIRequestLogBody string

func (APIRequestLogBody) GormDataType() string {
	return "text"
}

func (APIRequestLogBody) GormDBDataType(db *gorm.DB, field *schema.Field) string {
	if db != nil && db.Dialector != nil && db.Dialector.Name() == "mysql" {
		return "LONGTEXT"
	}
	return "TEXT"
}

type APIRequestLog struct {
	Id                    int                    `json:"id" gorm:"primaryKey;index:idx_api_request_logs_created_at_id,priority:1"`
	Source                string                 `json:"source" gorm:"type:varchar(32);index:idx_api_request_logs_source_id,priority:1;default:'live'"`
	SourceId              int                    `json:"source_id,omitempty" gorm:"index:idx_api_request_logs_source_id,priority:2;default:0"`
	UsageLogId            int                    `json:"usage_log_id" gorm:"index;default:0"`
	UserId                int                    `json:"user_id" gorm:"index;default:0"`
	Username              string                 `json:"username" gorm:"index;default:''"`
	TokenId               int                    `json:"token_id" gorm:"index;default:0"`
	TokenName             string                 `json:"token_name" gorm:"index;default:''"`
	ModelName             string                 `json:"model_name" gorm:"index;default:''"`
	CreatedAt             int64                  `json:"created_at" gorm:"bigint;index:idx_api_request_logs_created_at_id,priority:2"`
	RequestId             string                 `json:"request_id,omitempty" gorm:"type:varchar(64);index;default:''"`
	UpstreamRequestId     string                 `json:"upstream_request_id,omitempty" gorm:"type:varchar(128);index;default:''"`
	Method                string                 `json:"method" gorm:"type:varchar(16);default:''"`
	RequestPath           string                 `json:"request_path" gorm:"index;default:''"`
	APIFormat             string                 `json:"api_format,omitempty" gorm:"type:varchar(64);index;default:''"`
	StatusCode            int                    `json:"status_code" gorm:"index;default:0"`
	IsStream              bool                   `json:"is_stream"`
	ChannelId             int                    `json:"channel_id" gorm:"index;default:0"`
	Group                 string                 `json:"group" gorm:"index;default:''"`
	RequestContentType    string                 `json:"request_content_type" gorm:"default:''"`
	ResponseContentType   string                 `json:"response_content_type" gorm:"default:''"`
	RequestSize           int64                  `json:"request_size" gorm:"default:0"`
	ResponseSize          int64                  `json:"response_size" gorm:"default:0"`
	RequestOmittedReason  string                 `json:"request_omitted_reason,omitempty" gorm:"default:''"`
	ResponseOmittedReason string                 `json:"response_omitted_reason,omitempty" gorm:"default:''"`
	Redacted              bool                   `json:"redacted"`
	Quota                 int                    `json:"quota" gorm:"default:0"`
	PromptTokens          int                    `json:"prompt_tokens" gorm:"default:0"`
	CompletionTokens      int                    `json:"completion_tokens" gorm:"default:0"`
	TokenUsed             int                    `json:"token_used" gorm:"default:0"`
	UseTime               int                    `json:"use_time" gorm:"default:0"`
	SchemaVersion         int                    `json:"schema_version" gorm:"default:1"`
	ParseStatus           string                 `json:"parse_status" gorm:"type:varchar(16);index;default:'ok'"`
	ParseError            string                 `json:"parse_error,omitempty" gorm:"type:text"`
	ItemsStatus           string                 `json:"items_status" gorm:"type:varchar(16);index;default:''"`
	ItemsError            string                 `json:"items_error,omitempty" gorm:"type:text"`
	Items                 []APIRequestLogItem    `json:"items,omitempty" gorm:"foreignKey:LogId;constraint:OnDelete:CASCADE"`
	Usage                 *APIRequestLogUsage    `json:"usage,omitempty" gorm:"-"`
	TurnMeta              *APIRequestLogTurnMeta `json:"-" gorm:"-"`

	// Compatibility-only fields for older controller/frontend code and tests.
	// They are intentionally excluded from the normalized table.
	RequestBody  APIRequestLogBody `json:"request_body,omitempty" gorm:"-"`
	ResponseBody APIRequestLogBody `json:"response_body,omitempty" gorm:"-"`
	Metadata     APIRequestLogBody `json:"metadata,omitempty" gorm:"-"`
}

type APIRequestLogItem struct {
	Id          int               `json:"id" gorm:"primaryKey"`
	LogId       int               `json:"log_id" gorm:"index:idx_api_request_log_items_log_seq,priority:1;index"`
	Seq         int               `json:"seq" gorm:"index:idx_api_request_log_items_log_seq,priority:2"`
	Phase       string            `json:"phase" gorm:"type:varchar(16);index;default:''"`
	ItemType    string            `json:"item_type" gorm:"type:varchar(32);index;default:''"`
	Role        string            `json:"role,omitempty" gorm:"type:varchar(32);index;default:''"`
	ContentType string            `json:"content_type" gorm:"type:varchar(32);default:''"`
	Content     APIRequestLogBody `json:"content,omitempty"`
	ToolCallId  string            `json:"tool_call_id,omitempty" gorm:"type:varchar(128);index;default:''"`
	Name        string            `json:"name,omitempty" gorm:"type:varchar(255);default:''"`
	Source      string            `json:"source,omitempty" gorm:"type:varchar(128);default:''"`
	Redacted    bool              `json:"redacted"`
	Truncated   bool              `json:"truncated"`
}

type APIRequestLogListItem struct {
	Id                    int                 `json:"id"`
	Source                string              `json:"source"`
	SourceId              int                 `json:"source_id,omitempty"`
	UsageLogId            int                 `json:"usage_log_id"`
	UserId                int                 `json:"user_id"`
	Username              string              `json:"username"`
	TokenId               int                 `json:"token_id"`
	TokenName             string              `json:"token_name"`
	ModelName             string              `json:"model_name"`
	CreatedAt             int64               `json:"created_at"`
	RequestId             string              `json:"request_id,omitempty"`
	UpstreamRequestId     string              `json:"upstream_request_id,omitempty"`
	Method                string              `json:"method"`
	RequestPath           string              `json:"request_path"`
	APIFormat             string              `json:"api_format,omitempty"`
	StatusCode            int                 `json:"status_code"`
	IsStream              bool                `json:"is_stream"`
	ChannelId             int                 `json:"channel_id"`
	Group                 string              `json:"group"`
	RequestContentType    string              `json:"request_content_type"`
	ResponseContentType   string              `json:"response_content_type"`
	RequestSize           int64               `json:"request_size"`
	ResponseSize          int64               `json:"response_size"`
	RequestOmittedReason  string              `json:"request_omitted_reason,omitempty"`
	ResponseOmittedReason string              `json:"response_omitted_reason,omitempty"`
	Redacted              bool                `json:"redacted"`
	SchemaVersion         int                 `json:"schema_version"`
	ParseStatus           string              `json:"parse_status"`
	ParseError            string              `json:"parse_error,omitempty"`
	ItemsStatus           string              `json:"items_status"`
	ItemsError            string              `json:"items_error,omitempty"`
	Usage                 *APIRequestLogUsage `json:"usage,omitempty"`
}

type APIRequestLogQueryParams struct {
	StartTimestamp int64
	EndTimestamp   int64
	ModelName      string
	ModelNames     []string
	Username       string
	Usernames      []string
	TokenName      string
	StartIdx       int
	Num            int
}

type APIRequestLogFilterOptions struct {
	ModelNames []string `json:"model_names"`
	Usernames  []string `json:"usernames"`
}

type APIRequestLogUsage struct {
	LogId            int    `json:"log_id"`
	CreatedAt        int64  `json:"created_at"`
	ModelName        string `json:"model_name"`
	TokenName        string `json:"token_name"`
	Quota            int    `json:"quota"`
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
	TokenUsed        int    `json:"token_used"`
	UseTime          int    `json:"use_time"`
	Content          string `json:"content,omitempty"`
	Other            string `json:"other,omitempty"`
}

type APIRequestLogStorageStatus struct {
	HasTable              bool   `json:"has_table"`
	Count                 int64  `json:"count"`
	LastCreatedAt         int64  `json:"last_created_at,omitempty"`
	LastRequestId         string `json:"last_request_id,omitempty"`
	LastWriteError        string `json:"last_write_error,omitempty"`
	LastWriteErrorAt      int64  `json:"last_write_error_at,omitempty"`
	LogDBDialect          string `json:"log_db_dialect,omitempty"`
	RequestLogDBDialect   string `json:"request_log_db_dialect,omitempty"`
	EnsureMigrationFailed bool   `json:"ensure_migration_failed"`
	AsyncWrite            bool   `json:"async_write"`
	QueueDepth            int    `json:"queue_depth"`
	QueueCapacity         int    `json:"queue_capacity"`
	QueuedItemBytes       int64  `json:"queued_item_bytes"`
	MaxQueueBytes         int64  `json:"max_queue_bytes"`
	QueueDroppedJobs      int64  `json:"queue_dropped_jobs"`
	QueueDroppedItems     int64  `json:"queue_dropped_items"`
	QueueDroppedItemBytes int64  `json:"queue_dropped_item_bytes"`
}

var REQUEST_LOG_DB *gorm.DB

var apiRequestLogEnsureMu sync.Mutex
var apiRequestLogEnsuredDB *gorm.DB
var apiRequestLogEnsured bool
var apiRequestLogLastWriteError string
var apiRequestLogLastWriteErrorAt int64

type apiRequestLogItemWriteJob struct {
	DB    *gorm.DB
	Log   APIRequestLog
	Items []APIRequestLogItem
	Bytes int64
}

var apiRequestLogItemQueueMu sync.Mutex
var apiRequestLogItemQueue chan apiRequestLogItemWriteJob
var apiRequestLogQueuedItemBytes int64
var apiRequestLogQueueDroppedJobs int64
var apiRequestLogQueueDroppedItems int64
var apiRequestLogQueueDroppedItemBytes int64

func InitRequestLogDB() error {
	if strings.TrimSpace(os.Getenv("REQUEST_LOG_SQL_DSN")) == "" {
		REQUEST_LOG_DB = nil
		if common.APIRequestLogEnabled {
			return errors.New("REQUEST_LOG_SQL_DSN is required when API_REQUEST_LOG_ENABLED is true")
		}
		return nil
	}
	db, err := chooseDedicatedRequestLogDB()
	if err != nil {
		return err
	}
	if common.DebugEnabled {
		db = db.Debug()
	}
	REQUEST_LOG_DB = db
	if REQUEST_LOG_DB.Dialector != nil && REQUEST_LOG_DB.Dialector.Name() == "mysql" {
		if err := checkMySQLChineseSupport(REQUEST_LOG_DB); err != nil {
			return err
		}
	}
	sqlDB, err := REQUEST_LOG_DB.DB()
	if err != nil {
		return err
	}
	sqlDB.SetMaxIdleConns(common.GetEnvOrDefault("REQUEST_LOG_SQL_MAX_IDLE_CONNS", common.GetEnvOrDefault("SQL_MAX_IDLE_CONNS", 100)))
	sqlDB.SetMaxOpenConns(common.GetEnvOrDefault("REQUEST_LOG_SQL_MAX_OPEN_CONNS", common.GetEnvOrDefault("SQL_MAX_OPEN_CONNS", 1000)))
	sqlDB.SetConnMaxLifetime(time.Second * time.Duration(common.GetEnvOrDefault("REQUEST_LOG_SQL_MAX_LIFETIME", common.GetEnvOrDefault("SQL_MAX_LIFETIME", 60))))
	if common.GetEnvOrDefaultBool("REQUEST_LOG_DB_READ_ONLY", false) {
		return nil
	}
	if !common.IsMasterNode {
		return nil
	}
	if err := EnsureAPIRequestLogTable(); err != nil {
		return err
	}
	startAPIRequestLogItemWriters()
	return nil
}

func chooseDedicatedRequestLogDB() (*gorm.DB, error) {
	dsn := resolveConfiguredDSN("REQUEST_LOG_SQL_DSN")
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		return gorm.Open(postgres.New(postgres.Config{
			DSN:                  dsn,
			PreferSimpleProtocol: true,
		}), &gorm.Config{PrepareStmt: true})
	}
	if strings.HasPrefix(dsn, "local") {
		return gorm.Open(sqlite.Open(common.SQLitePath), &gorm.Config{PrepareStmt: true})
	}
	return gorm.Open(mysql.Open(ensureMySQLDSNDefaults(dsn)), &gorm.Config{PrepareStmt: true})
}

func requestLogDB() *gorm.DB {
	return REQUEST_LOG_DB
}

func setAPIRequestLogLastWriteError(err error) {
	apiRequestLogEnsureMu.Lock()
	defer apiRequestLogEnsureMu.Unlock()
	if err == nil {
		apiRequestLogLastWriteError = ""
		apiRequestLogLastWriteErrorAt = 0
		return
	}
	apiRequestLogLastWriteError = err.Error()
	apiRequestLogLastWriteErrorAt = common.GetTimestamp()
}

func EnsureAPIRequestLogTable() error {
	db := requestLogDB()
	if db == nil {
		err := errors.New("request log database is not initialized")
		setAPIRequestLogLastWriteError(err)
		return err
	}

	apiRequestLogEnsureMu.Lock()
	if apiRequestLogEnsured && apiRequestLogEnsuredDB == db {
		apiRequestLogEnsureMu.Unlock()
		return nil
	}
	apiRequestLogEnsureMu.Unlock()

	if err := db.AutoMigrate(&APIRequestLog{}, &APIRequestLogItem{}); err != nil {
		setAPIRequestLogLastWriteError(err)
		return err
	}
	if err := EnsureAPIRequestLogMaterializedTables(db); err != nil {
		setAPIRequestLogLastWriteError(err)
		return err
	}
	setAPIRequestLogLastWriteError(nil)

	apiRequestLogEnsureMu.Lock()
	apiRequestLogEnsuredDB = db
	apiRequestLogEnsured = true
	apiRequestLogEnsureMu.Unlock()
	return nil
}

func CreateAPIRequestLog(log *APIRequestLog) error {
	if log == nil || common.IsCallLogExcludedUsername(log.Username) {
		return nil
	}
	if err := EnsureAPIRequestLogTable(); err != nil {
		return err
	}
	normalizeAPIRequestLog(log)
	err := createOrUpdateAPIRequestLog(log)
	setAPIRequestLogLastWriteError(err)
	return err
}

func createOrUpdateAPIRequestLog(log *APIRequestLog) error {
	return createOrUpdateAPIRequestLogWithMaterializer(log, materializeAPIRequestLogTurnForWrite)
}

type apiRequestLogTurnMaterializer func(*gorm.DB, *APIRequestLog, []APIRequestLogItem) error

func createOrUpdateAPIRequestLogWithMaterializer(log *APIRequestLog, materialize apiRequestLogTurnMaterializer) error {
	db := requestLogDB()
	items := log.Items
	log.Items = nil
	hasItems := len(items) > 0
	asyncItems := shouldAsyncAPIRequestLogItems(log, hasItems)
	if hasItems {
		log.ItemsStatus = APIRequestLogItemsPending
		log.ItemsError = ""
	}

	if err := db.Transaction(func(tx *gorm.DB) error {
		var existing APIRequestLog
		err := findExistingAPIRequestLog(tx, log, &existing)
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			if !hasItems && log.ItemsStatus == "" {
				log.ItemsStatus = APIRequestLogItemsEmpty
				log.ItemsError = ""
			}
			if err := tx.Create(log).Error; err != nil {
				return err
			}
		case err != nil:
			return err
		default:
			log.Id = existing.Id
			if !hasItems && log.ItemsStatus == "" {
				log.ItemsStatus = existing.ItemsStatus
				log.ItemsError = existing.ItemsError
			}
			if existing.UsageLogId > 0 && log.UsageLogId == 0 {
				preserveAPIRequestLogUsageFields(log, &existing)
			}
			if err := tx.Model(&APIRequestLog{}).Where("id = ?", log.Id).Updates(log).Error; err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}

	if !hasItems {
		log.Items = nil
		if log.TurnMeta == nil {
			return nil
		}
		return materializeRequestLogTurnOrMarkFailed(db, log, nil, materialize)
	}

	log.Items = normalizeAPIRequestLogItems(log.Id, items)
	if len(log.Items) == 0 {
		log.ItemsStatus = APIRequestLogItemsEmpty
		log.ItemsError = ""
		if err := updateAPIRequestLogItemsStatus(db, log.Id, APIRequestLogItemsEmpty, ""); err != nil {
			return err
		}
		if log.TurnMeta == nil {
			return nil
		}
		return materializeRequestLogTurnOrMarkFailed(db, log, nil, materialize)
	}
	if asyncItems {
		return enqueueAPIRequestLogItems(db, log, log.Items)
	}
	if err := createAPIRequestLogItemsIfMissing(db, log.Id, log.Items); err != nil {
		markAPIRequestLogItemsFailed(db, log, err)
		return err
	}
	return materializeRequestLogTurnAndComplete(db, log, log.Items, materialize)
}

func shouldAsyncAPIRequestLogItems(log *APIRequestLog, hasItems bool) bool {
	return hasItems && common.APIRequestLogAsyncWrite && log != nil && log.Source == APIRequestLogSourceLive
}

func createAPIRequestLogItemsIfMissing(db *gorm.DB, logId int, items []APIRequestLogItem) error {
	return createAPIRequestLogItemsIfMissingWithWriter(db, logId, items, createAPIRequestLogItems)
}

type apiRequestLogItemWriter func(*gorm.DB, []APIRequestLogItem) error

func createAPIRequestLogItemsIfMissingWithWriter(db *gorm.DB, logId int, items []APIRequestLogItem, writer apiRequestLogItemWriter) error {
	if db == nil || logId <= 0 || len(items) == 0 {
		return nil
	}
	if writer == nil {
		return errors.New("request log item writer is required")
	}
	var lastErr error
	for attempt := 0; attempt <= apiRequestLogItemMaxRetry; attempt++ {
		missing, complete, err := missingAPIRequestLogItems(db, logId, items)
		if err != nil {
			return err
		}
		if complete {
			return nil
		}
		if attempt == apiRequestLogItemMaxRetry {
			break
		}
		err = writer(db, missing)
		lastErr = err
		if err != nil && !isRetryableAPIRequestLogDBError(err) {
			return err
		}
		if err != nil {
			time.Sleep(time.Duration(attempt+1) * 200 * time.Millisecond)
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("request log items for log %d remain incomplete after %d attempts", logId, apiRequestLogItemMaxRetry)
	}
	return lastErr
}

func missingAPIRequestLogItems(db *gorm.DB, logId int, expected []APIRequestLogItem) ([]APIRequestLogItem, bool, error) {
	var storedSeqs []int
	if err := db.Model(&APIRequestLogItem{}).Where("log_id = ?", logId).Pluck("seq", &storedSeqs).Error; err != nil {
		return nil, false, err
	}
	expectedCounts := make(map[int]int, len(expected))
	for _, item := range expected {
		expectedCounts[item.Seq]++
	}
	storedCounts := make(map[int]int, len(storedSeqs))
	for _, seq := range storedSeqs {
		storedCounts[seq]++
		if storedCounts[seq] > expectedCounts[seq] {
			return nil, false, fmt.Errorf("request log items for log %d contain unexpected seq %d", logId, seq)
		}
	}
	complete := len(storedSeqs) == len(expected)
	missing := make([]APIRequestLogItem, 0, len(expected)-len(storedSeqs))
	remaining := make(map[int]int, len(storedCounts))
	for seq, count := range storedCounts {
		remaining[seq] = count
	}
	for _, item := range expected {
		if remaining[item.Seq] > 0 {
			remaining[item.Seq]--
			continue
		}
		complete = false
		item.Id = 0
		item.LogId = logId
		missing = append(missing, item)
	}
	return missing, complete, nil
}

func createAPIRequestLogItems(db *gorm.DB, items []APIRequestLogItem) error {
	if db == nil || len(items) == 0 {
		return nil
	}
	return db.Session(&gorm.Session{SkipDefaultTransaction: true}).CreateInBatches(items, apiRequestLogItemBatchSize).Error
}

func enqueueAPIRequestLogItems(db *gorm.DB, log *APIRequestLog, items []APIRequestLogItem) error {
	if log == nil {
		return errors.New("request log is required")
	}
	startAPIRequestLogItemWriters()
	apiRequestLogItemQueueMu.Lock()
	queue := apiRequestLogItemQueue
	apiRequestLogItemQueueMu.Unlock()
	if queue == nil {
		err := errors.New("request log item queue is not initialized")
		log.ItemsStatus = APIRequestLogItemsFailed
		log.ItemsError = err.Error()
		_ = updateAPIRequestLogItemsStatus(db, log.Id, APIRequestLogItemsFailed, err.Error())
		_ = materializeAPIRequestLogTurnWriteFailure(db, log)
		return err
	}
	itemBytes := apiRequestLogItemsByteSize(items)
	if err := reserveAPIRequestLogQueueBytes(itemBytes); err != nil {
		recordAPIRequestLogQueueDrop(len(items), itemBytes)
		_ = updateAPIRequestLogItemsStatus(db, log.Id, APIRequestLogItemsFailed, err.Error())
		_ = materializeAPIRequestLogTurnWriteFailure(db, log)
		return err
	}
	logCopy := *log
	logCopy.Items = nil
	job := apiRequestLogItemWriteJob{DB: db, Log: logCopy, Items: items, Bytes: itemBytes}
	select {
	case queue <- job:
		return nil
	default:
		releaseAPIRequestLogQueueBytes(itemBytes)
		recordAPIRequestLogQueueDrop(len(items), itemBytes)
		err := errors.New("request log item queue is full")
		_ = updateAPIRequestLogItemsStatus(db, log.Id, APIRequestLogItemsFailed, err.Error())
		_ = materializeAPIRequestLogTurnWriteFailure(db, log)
		return err
	}
}

func recordAPIRequestLogQueueDrop(itemCount int, itemBytes int64) {
	atomic.AddInt64(&apiRequestLogQueueDroppedJobs, 1)
	atomic.AddInt64(&apiRequestLogQueueDroppedItems, int64(itemCount))
	atomic.AddInt64(&apiRequestLogQueueDroppedItemBytes, itemBytes)
}

func startAPIRequestLogItemWriters() {
	if !common.APIRequestLogAsyncWrite || common.GetEnvOrDefaultBool("REQUEST_LOG_DB_READ_ONLY", false) {
		return
	}
	apiRequestLogItemQueueMu.Lock()
	defer apiRequestLogItemQueueMu.Unlock()
	if apiRequestLogItemQueue != nil {
		return
	}
	queueSize := common.APIRequestLogQueueSize
	if queueSize < 1 {
		queueSize = 1
	}
	workers := common.APIRequestLogWorkers
	if workers < 1 {
		workers = 1
	}
	apiRequestLogItemQueue = make(chan apiRequestLogItemWriteJob, queueSize)
	for i := 0; i < workers; i++ {
		go apiRequestLogItemWorker(apiRequestLogItemQueue)
	}
}

func apiRequestLogItemWorker(queue <-chan apiRequestLogItemWriteJob) {
	for job := range queue {
		err := createAPIRequestLogItemsIfMissing(job.DB, job.Log.Id, job.Items)
		releaseAPIRequestLogQueueBytes(job.Bytes)
		if err != nil {
			setAPIRequestLogLastWriteError(err)
			_ = updateAPIRequestLogItemsStatus(job.DB, job.Log.Id, APIRequestLogItemsFailed, err.Error())
			_ = materializeAPIRequestLogTurnWriteFailure(job.DB, &job.Log)
			continue
		}
		if err := materializeRequestLogTurnAndComplete(job.DB, &job.Log, job.Items, materializeAPIRequestLogTurnForWrite); err != nil {
			setAPIRequestLogLastWriteError(err)
			continue
		}
	}
}

func materializeRequestLogTurnAndComplete(db *gorm.DB, log *APIRequestLog, items []APIRequestLogItem, materialize apiRequestLogTurnMaterializer) error {
	if err := materializeRequestLogTurnOrMarkFailed(db, log, items, materialize); err != nil {
		return err
	}
	if err := updateAPIRequestLogItemsStatus(db, log.Id, APIRequestLogItemsOK, ""); err != nil {
		wrapped := fmt.Errorf("mark request log items complete: %w", err)
		markAPIRequestLogItemsFailed(db, log, wrapped)
		return wrapped
	}
	log.ItemsStatus = APIRequestLogItemsOK
	log.ItemsError = ""
	return nil
}

func materializeRequestLogTurnOrMarkFailed(db *gorm.DB, log *APIRequestLog, items []APIRequestLogItem, materialize apiRequestLogTurnMaterializer) error {
	if materialize == nil {
		err := errors.New("request log turn materializer is required")
		markAPIRequestLogItemsFailed(db, log, err)
		return err
	}
	if err := materialize(db, log, items); err != nil {
		wrapped := fmt.Errorf("materialize request log turn: %w", err)
		markAPIRequestLogItemsFailed(db, log, wrapped)
		return wrapped
	}
	return nil
}

func markAPIRequestLogItemsFailed(db *gorm.DB, log *APIRequestLog, err error) {
	if log == nil || err == nil {
		return
	}
	log.ItemsStatus = APIRequestLogItemsFailed
	log.ItemsError = err.Error()
	_ = updateAPIRequestLogItemsStatus(db, log.Id, APIRequestLogItemsFailed, err.Error())
}

func materializeAPIRequestLogTurnForWrite(db *gorm.DB, log *APIRequestLog, items []APIRequestLogItem) error {
	if db == nil || log == nil || log.Id <= 0 {
		return nil
	}
	meta := APIRequestLogTurnMeta{Protocol: log.APIFormat}
	if log.TurnMeta != nil {
		meta = *log.TurnMeta
		if meta.Protocol == "" {
			meta.Protocol = log.APIFormat
		}
	}
	_, err := MaterializeAPIRequestLogTurn(db, log, meta, items)
	return err
}

func materializeAPIRequestLogTurnWriteFailure(db *gorm.DB, log *APIRequestLog) error {
	if log == nil {
		return nil
	}
	logCopy := *log
	if log.TurnMeta != nil {
		meta := *log.TurnMeta
		meta.Items = nil
		meta.CompletedAt = 0
		meta.CompletionSignal = ""
		if strings.TrimSpace(meta.SessionId) == "" {
			meta.CompletionStatus = APIRequestLogTurnStatusUnknown
			meta.Attribution = APIRequestLogTurnAttributionUnknown
		} else {
			meta.CompletionStatus = APIRequestLogTurnStatusOpen
		}
		logCopy.TurnMeta = &meta
	}
	return materializeAPIRequestLogTurnForWrite(db, &logCopy, nil)
}

func apiRequestLogItemsByteSize(items []APIRequestLogItem) int64 {
	var size int64
	for _, item := range items {
		size += int64(len(item.Content))
		size += int64(len(item.Phase) + len(item.ItemType) + len(item.Role) + len(item.ContentType))
		size += int64(len(item.ToolCallId) + len(item.Name) + len(item.Source))
	}
	return size
}

func reserveAPIRequestLogQueueBytes(size int64) error {
	if size <= 0 {
		return nil
	}
	maxBytes := int64(common.APIRequestLogMaxQueueBytes)
	next := atomic.AddInt64(&apiRequestLogQueuedItemBytes, size)
	if maxBytes <= 0 || next <= maxBytes {
		return nil
	}
	atomic.AddInt64(&apiRequestLogQueuedItemBytes, -size)
	return errors.New("request log item queue byte limit exceeded")
}

func releaseAPIRequestLogQueueBytes(size int64) {
	if size > 0 {
		atomic.AddInt64(&apiRequestLogQueuedItemBytes, -size)
	}
}

func updateAPIRequestLogItemsStatus(db *gorm.DB, logId int, status string, message string) error {
	if db == nil || logId <= 0 {
		return nil
	}
	return db.Model(&APIRequestLog{}).Where("id = ?", logId).Updates(map[string]interface{}{
		"items_status": status,
		"items_error":  message,
	}).Error
}

func isRetryableAPIRequestLogDBError(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "deadlock") ||
		strings.Contains(text, "lock wait timeout") ||
		strings.Contains(text, "error 1205") ||
		strings.Contains(text, "error 1213")
}

func preserveAPIRequestLogUsageFields(log *APIRequestLog, existing *APIRequestLog) {
	if log == nil || existing == nil {
		return
	}
	log.UsageLogId = existing.UsageLogId
	log.UserId = existing.UserId
	log.Username = existing.Username
	log.TokenId = existing.TokenId
	log.TokenName = existing.TokenName
	log.ModelName = existing.ModelName
	log.CreatedAt = existing.CreatedAt
	log.RequestId = existing.RequestId
	log.UpstreamRequestId = existing.UpstreamRequestId
	log.IsStream = existing.IsStream
	log.ChannelId = existing.ChannelId
	log.Group = existing.Group
	log.Quota = existing.Quota
	log.PromptTokens = existing.PromptTokens
	log.CompletionTokens = existing.CompletionTokens
	log.TokenUsed = existing.TokenUsed
	log.UseTime = existing.UseTime
}

func findExistingAPIRequestLog(tx *gorm.DB, log *APIRequestLog, out *APIRequestLog) error {
	if log.Source != "" && log.SourceId > 0 {
		return tx.Where("source = ? AND source_id = ?", log.Source, log.SourceId).Order("id asc").First(out).Error
	}
	if log.UsageLogId > 0 {
		err := tx.Where("usage_log_id = ?", log.UsageLogId).Order("id asc").First(out).Error
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
	}
	if log.Source == APIRequestLogSourceLive && (log.RequestId != "" || log.UpstreamRequestId != "") {
		query := tx.Where("source = ?", log.Source)
		switch {
		case log.RequestId != "" && log.UpstreamRequestId != "":
			query = query.Where("(request_id = ? OR upstream_request_id = ?)", log.RequestId, log.UpstreamRequestId)
		case log.RequestId != "":
			query = query.Where("request_id = ?", log.RequestId)
		default:
			query = query.Where("upstream_request_id = ?", log.UpstreamRequestId)
		}
		return query.Order("usage_log_id desc, id asc").First(out).Error
	}
	if log.Id > 0 {
		return tx.First(out, log.Id).Error
	}
	return gorm.ErrRecordNotFound
}

func normalizeAPIRequestLog(log *APIRequestLog) {
	if log.Source == "" {
		log.Source = APIRequestLogSourceLive
	}
	if log.CreatedAt == 0 {
		log.CreatedAt = common.GetTimestamp()
	}
	if log.StatusCode == 0 {
		log.StatusCode = 200
	}
	if log.SchemaVersion == 0 {
		log.SchemaVersion = APIRequestLogSchemaVersion
	}
	if log.ParseStatus == "" {
		log.ParseStatus = APIRequestLogParseOK
	}
	if log.TokenUsed == 0 {
		log.TokenUsed = log.PromptTokens + log.CompletionTokens
	}
}

func normalizeAPIRequestLogItems(logId int, items []APIRequestLogItem) []APIRequestLogItem {
	if len(items) == 0 {
		return nil
	}
	out := make([]APIRequestLogItem, 0, len(items))
	for _, item := range items {
		if strings.TrimSpace(string(item.Content)) == "" && item.ItemType != APIRequestLogItemError {
			continue
		}
		item.Id = 0
		item.LogId = logId
		if item.Seq <= 0 {
			item.Seq = len(out) + 1
		}
		item.Content, item.Truncated = truncateAPIRequestLogItemContent(item.Content, item.Truncated)
		out = append(out, item)
	}
	return out
}

func truncateAPIRequestLogItemContent(content APIRequestLogBody, alreadyTruncated bool) (APIRequestLogBody, bool) {
	text := strings.ToValidUTF8(string(content), "\uFFFD")
	maxBytes := common.APIRequestLogMaxItemBytes
	if maxBytes <= 0 || len(text) <= maxBytes {
		return APIRequestLogBody(text), alreadyTruncated
	}
	if len(text) > maxBytes {
		text = text[:maxBytes]
		for len(text) > 0 && !utf8.ValidString(text) {
			text = text[:len(text)-1]
		}
	}
	return APIRequestLogBody(text + "\n[TRUNCATED]"), true
}

func CreateAPIRequestLogFromConsumeLog(c *gin.Context, usageLog *Log) error {
	if !common.APIRequestLogEnabled || usageLog == nil || usageLog.Id <= 0 || usageLog.Type != LogTypeConsume {
		return nil
	}
	return CreateAPIRequestLog(apiRequestLogFromConsumeLog(c, usageLog))
}

func apiRequestLogFromConsumeLog(c *gin.Context, usageLog *Log) *APIRequestLog {
	log := &APIRequestLog{
		Source:            APIRequestLogSourceLive,
		UsageLogId:        usageLog.Id,
		UserId:            usageLog.UserId,
		Username:          usageLog.Username,
		TokenId:           usageLog.TokenId,
		TokenName:         usageLog.TokenName,
		ModelName:         usageLog.ModelName,
		CreatedAt:         usageLog.CreatedAt,
		RequestId:         usageLog.RequestId,
		UpstreamRequestId: usageLog.UpstreamRequestId,
		StatusCode:        200,
		IsStream:          usageLog.IsStream,
		ChannelId:         usageLog.ChannelId,
		Group:             usageLog.Group,
		Quota:             usageLog.Quota,
		PromptTokens:      usageLog.PromptTokens,
		CompletionTokens:  usageLog.CompletionTokens,
		TokenUsed:         usageLog.PromptTokens + usageLog.CompletionTokens,
		UseTime:           usageLog.UseTime,
	}
	if c != nil {
		if c.Request != nil {
			log.Method = c.Request.Method
			log.RequestContentType = c.Request.Header.Get("Content-Type")
			log.RequestSize = c.Request.ContentLength
			if c.Request.URL != nil {
				log.RequestPath = c.Request.URL.Path
			}
		}
		if c.Writer != nil {
			log.StatusCode = c.Writer.Status()
			log.ResponseContentType = c.Writer.Header().Get("Content-Type")
		}
	}
	normalizeAPIRequestLog(log)
	return log
}

func GetAPIRequestLogs(params APIRequestLogQueryParams) (logs []*APIRequestLogListItem, total int64, err error) {
	if err = EnsureAPIRequestLogTable(); err != nil {
		return nil, 0, err
	}
	tx := buildAPIRequestLogsQuery(params)
	if err = tx.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var requestLogs []*APIRequestLog
	if err = tx.Order("id desc").Limit(params.Num).Offset(params.StartIdx).Find(&requestLogs).Error; err != nil {
		return nil, 0, err
	}

	logs = make([]*APIRequestLogListItem, 0, len(requestLogs))
	for _, log := range requestLogs {
		logs = append(logs, apiRequestLogListItemFromLog(log))
	}
	return logs, total, nil
}

func buildAPIRequestLogsQuery(params APIRequestLogQueryParams) *gorm.DB {
	tx := requestLogDB().Model(&APIRequestLog{})
	if len(params.ModelNames) > 0 {
		tx = tx.Where("model_name IN ?", params.ModelNames)
	} else {
		tx = applyLogContainsFilter(tx, "model_name", params.ModelName)
	}
	if len(params.Usernames) > 0 {
		tx = tx.Where("username IN ?", params.Usernames)
	} else {
		tx = applyLogContainsFilter(tx, "username", params.Username)
	}
	tx = applyLogContainsFilter(tx, "token_name", params.TokenName)
	if params.StartTimestamp != 0 {
		tx = tx.Where("created_at >= ?", params.StartTimestamp)
	}
	if params.EndTimestamp != 0 {
		tx = tx.Where("created_at <= ?", params.EndTimestamp)
	}
	return tx
}

func GetAPIRequestLogFilterOptions(limit int) (*APIRequestLogFilterOptions, error) {
	if err := EnsureAPIRequestLogTable(); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 1000 {
		limit = 500
	}
	options := &APIRequestLogFilterOptions{}
	if err := requestLogDB().Model(&APIRequestLog{}).
		Distinct("model_name").
		Where("model_name <> ?", "").
		Order("model_name asc").
		Limit(limit).
		Pluck("model_name", &options.ModelNames).Error; err != nil {
		return nil, err
	}
	if err := requestLogDB().Model(&APIRequestLog{}).
		Distinct("username").
		Where("username <> ?", "").
		Order("username asc").
		Limit(limit).
		Pluck("username", &options.Usernames).Error; err != nil {
		return nil, err
	}
	return options, nil
}

func apiRequestLogListItemFromLog(log *APIRequestLog) *APIRequestLogListItem {
	if log == nil {
		return nil
	}
	item := &APIRequestLogListItem{
		Id:                    log.Id,
		Source:                log.Source,
		SourceId:              log.SourceId,
		UsageLogId:            log.UsageLogId,
		UserId:                log.UserId,
		Username:              log.Username,
		TokenId:               log.TokenId,
		TokenName:             log.TokenName,
		ModelName:             log.ModelName,
		CreatedAt:             log.CreatedAt,
		RequestId:             log.RequestId,
		UpstreamRequestId:     log.UpstreamRequestId,
		Method:                log.Method,
		RequestPath:           log.RequestPath,
		APIFormat:             log.APIFormat,
		StatusCode:            log.StatusCode,
		IsStream:              log.IsStream,
		ChannelId:             log.ChannelId,
		Group:                 log.Group,
		RequestContentType:    log.RequestContentType,
		ResponseContentType:   log.ResponseContentType,
		RequestSize:           log.RequestSize,
		ResponseSize:          log.ResponseSize,
		RequestOmittedReason:  log.RequestOmittedReason,
		ResponseOmittedReason: log.ResponseOmittedReason,
		Redacted:              log.Redacted,
		SchemaVersion:         log.SchemaVersion,
		ParseStatus:           log.ParseStatus,
		ParseError:            log.ParseError,
		ItemsStatus:           log.ItemsStatus,
		ItemsError:            log.ItemsError,
		Usage:                 apiRequestUsageFromRequestLog(log),
	}
	return item
}

func GetAPIRequestLogById(id int) (*APIRequestLog, error) {
	if err := EnsureAPIRequestLogTable(); err != nil {
		return nil, err
	}
	var log APIRequestLog
	if err := requestLogDB().Preload("Items", func(db *gorm.DB) *gorm.DB {
		return db.Order("seq asc, id asc")
	}).First(&log, id).Error; err != nil {
		return nil, err
	}
	log.Usage = apiRequestUsageFromRequestLog(&log)
	return &log, nil
}

func GetAPIRequestLogUsage(requestId string, upstreamRequestId string, includeBody bool) (*APIRequestLogUsage, error) {
	return getAPIRequestLogUsage(0, requestId, upstreamRequestId, includeBody)
}

func getAPIRequestLogUsage(usageLogId int, requestId string, upstreamRequestId string, includeBody bool) (*APIRequestLogUsage, error) {
	if usageLogId > 0 {
		var log Log
		err := LOG_DB.Where("type = ?", LogTypeConsume).First(&log, usageLogId).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		return apiRequestUsageFromLog(&log, includeBody), nil
	}
	if requestId == "" && upstreamRequestId == "" {
		return nil, nil
	}

	var logs []*Log
	tx := LOG_DB.Model(&Log{}).Where("type = ?", LogTypeConsume)
	if requestId != "" && upstreamRequestId != "" {
		tx = tx.Where("(request_id = ? OR upstream_request_id = ?)", requestId, upstreamRequestId)
	} else if requestId != "" {
		tx = tx.Where("request_id = ?", requestId)
	} else {
		tx = tx.Where("upstream_request_id = ?", upstreamRequestId)
	}
	if err := tx.Order("id desc").Limit(1).Find(&logs).Error; err != nil {
		return nil, err
	}
	if len(logs) == 0 {
		return nil, nil
	}
	return apiRequestUsageFromLog(logs[0], includeBody), nil
}

func apiRequestUsageFromLog(log *Log, includeBody bool) *APIRequestLogUsage {
	if log == nil {
		return nil
	}
	usage := &APIRequestLogUsage{
		LogId:            log.Id,
		CreatedAt:        log.CreatedAt,
		ModelName:        log.ModelName,
		TokenName:        log.TokenName,
		Quota:            log.Quota,
		PromptTokens:     log.PromptTokens,
		CompletionTokens: log.CompletionTokens,
		TokenUsed:        log.PromptTokens + log.CompletionTokens,
		UseTime:          log.UseTime,
	}
	if includeBody {
		usage.Content = log.Content
		usage.Other = log.Other
	}
	return usage
}

func apiRequestUsageFromRequestLog(log *APIRequestLog) *APIRequestLogUsage {
	if log == nil {
		return nil
	}
	return &APIRequestLogUsage{
		LogId:            log.UsageLogId,
		CreatedAt:        log.CreatedAt,
		ModelName:        log.ModelName,
		TokenName:        log.TokenName,
		Quota:            log.Quota,
		PromptTokens:     log.PromptTokens,
		CompletionTokens: log.CompletionTokens,
		TokenUsed:        log.TokenUsed,
		UseTime:          log.UseTime,
	}
}

func GetAPIRequestLogStorageStatus() (*APIRequestLogStorageStatus, error) {
	status := &APIRequestLogStorageStatus{
		AsyncWrite:            common.APIRequestLogAsyncWrite,
		QueuedItemBytes:       atomic.LoadInt64(&apiRequestLogQueuedItemBytes),
		MaxQueueBytes:         int64(common.APIRequestLogMaxQueueBytes),
		QueueDroppedJobs:      atomic.LoadInt64(&apiRequestLogQueueDroppedJobs),
		QueueDroppedItems:     atomic.LoadInt64(&apiRequestLogQueueDroppedItems),
		QueueDroppedItemBytes: atomic.LoadInt64(&apiRequestLogQueueDroppedItemBytes),
	}
	apiRequestLogItemQueueMu.Lock()
	if apiRequestLogItemQueue != nil {
		status.QueueDepth = len(apiRequestLogItemQueue)
		status.QueueCapacity = cap(apiRequestLogItemQueue)
	}
	apiRequestLogItemQueueMu.Unlock()
	db := requestLogDB()
	if db == nil {
		status.LastWriteError = "request log database is not initialized"
		status.EnsureMigrationFailed = true
		return status, nil
	}
	if LOG_DB != nil && LOG_DB.Dialector != nil {
		status.LogDBDialect = LOG_DB.Dialector.Name()
	}
	if db.Dialector != nil {
		status.RequestLogDBDialect = db.Dialector.Name()
	}

	status.HasTable = db.Migrator().HasTable(&APIRequestLog{})
	if !status.HasTable {
		return status, nil
	}

	if err := db.Model(&APIRequestLog{}).Count(&status.Count).Error; err != nil {
		return status, err
	}

	var last APIRequestLog
	result := db.Model(&APIRequestLog{}).Select("created_at, request_id").Order("id desc").Limit(1).Find(&last)
	if result.Error != nil {
		return status, result.Error
	}
	if result.RowsAffected == 0 {
		return status, nil
	}
	status.LastCreatedAt = last.CreatedAt
	status.LastRequestId = last.RequestId
	return status, nil
}
