package model

import (
	"errors"
	"sync"

	"github.com/QuantumNous/new-api/common"

	"gorm.io/gorm"
	"gorm.io/gorm/schema"
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
	Id                    int                 `json:"id" gorm:"index:idx_api_request_logs_created_at_id,priority:1"`
	UserId                int                 `json:"user_id" gorm:"index"`
	Username              string              `json:"username" gorm:"index;default:''"`
	TokenId               int                 `json:"token_id" gorm:"index;default:0"`
	TokenName             string              `json:"token_name" gorm:"index;default:''"`
	ModelName             string              `json:"model_name" gorm:"index;default:''"`
	CreatedAt             int64               `json:"created_at" gorm:"bigint;index:idx_api_request_logs_created_at_id,priority:2"`
	RequestId             string              `json:"request_id,omitempty" gorm:"type:varchar(64);index;default:''"`
	UpstreamRequestId     string              `json:"upstream_request_id,omitempty" gorm:"type:varchar(128);index;default:''"`
	Method                string              `json:"method" gorm:"type:varchar(16);default:''"`
	RequestPath           string              `json:"request_path" gorm:"index;default:''"`
	StatusCode            int                 `json:"status_code" gorm:"index;default:0"`
	IsStream              bool                `json:"is_stream"`
	ChannelId             int                 `json:"channel_id" gorm:"index;default:0"`
	Group                 string              `json:"group" gorm:"index;default:''"`
	RequestContentType    string              `json:"request_content_type" gorm:"default:''"`
	ResponseContentType   string              `json:"response_content_type" gorm:"default:''"`
	RequestSize           int64               `json:"request_size" gorm:"default:0"`
	ResponseSize          int64               `json:"response_size" gorm:"default:0"`
	RequestOmittedReason  string              `json:"request_omitted_reason,omitempty" gorm:"default:''"`
	ResponseOmittedReason string              `json:"response_omitted_reason,omitempty" gorm:"default:''"`
	Redacted              bool                `json:"redacted"`
	RequestBody           APIRequestLogBody   `json:"request_body,omitempty"`
	ResponseBody          APIRequestLogBody   `json:"response_body,omitempty"`
	Metadata              APIRequestLogBody   `json:"metadata,omitempty"`
	Usage                 *APIRequestLogUsage `json:"usage,omitempty" gorm:"-"`
}

type APIRequestLogListItem struct {
	Id                    int                 `json:"id"`
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
	Usage                 *APIRequestLogUsage `json:"usage,omitempty" gorm:"-"`
}

type APIRequestLogQueryParams struct {
	StartTimestamp int64
	EndTimestamp   int64
	ModelName      string
	Username       string
	TokenName      string
	StartIdx       int
	Num            int
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
	EnsureMigrationFailed bool   `json:"ensure_migration_failed"`
}

var apiRequestLogEnsureMu sync.Mutex
var apiRequestLogEnsuredDB *gorm.DB
var apiRequestLogEnsured bool
var apiRequestLogLastWriteError string
var apiRequestLogLastWriteErrorAt int64

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
	if LOG_DB == nil {
		err := errors.New("log database is not initialized")
		setAPIRequestLogLastWriteError(err)
		return err
	}

	apiRequestLogEnsureMu.Lock()
	if apiRequestLogEnsured && apiRequestLogEnsuredDB == LOG_DB {
		apiRequestLogEnsureMu.Unlock()
		return nil
	}
	apiRequestLogEnsureMu.Unlock()

	if LOG_DB.Migrator().HasTable(&APIRequestLog{}) {
		apiRequestLogEnsureMu.Lock()
		apiRequestLogEnsuredDB = LOG_DB
		apiRequestLogEnsured = true
		apiRequestLogEnsureMu.Unlock()
		return nil
	}

	if err := LOG_DB.AutoMigrate(&APIRequestLog{}); err != nil {
		setAPIRequestLogLastWriteError(err)
		return err
	}
	setAPIRequestLogLastWriteError(nil)

	apiRequestLogEnsureMu.Lock()
	apiRequestLogEnsuredDB = LOG_DB
	apiRequestLogEnsured = true
	apiRequestLogEnsureMu.Unlock()
	return nil
}

func CreateAPIRequestLog(log *APIRequestLog) error {
	if err := EnsureAPIRequestLogTable(); err != nil {
		return err
	}
	err := LOG_DB.Create(log).Error
	setAPIRequestLogLastWriteError(err)
	return err
}

func GetAPIRequestLogs(params APIRequestLogQueryParams) (logs []*APIRequestLogListItem, total int64, err error) {
	if err := EnsureAPIRequestLogTable(); err != nil {
		return nil, 0, err
	}
	tx := LOG_DB.Model(&APIRequestLog{})
	tx = applyLogContainsFilter(tx, "api_request_logs.model_name", params.ModelName)
	tx = applyLogContainsFilter(tx, "api_request_logs.username", params.Username)
	tx = applyLogContainsFilter(tx, "api_request_logs.token_name", params.TokenName)
	if params.StartTimestamp != 0 {
		tx = tx.Where("api_request_logs.created_at >= ?", params.StartTimestamp)
	}
	if params.EndTimestamp != 0 {
		tx = tx.Where("api_request_logs.created_at <= ?", params.EndTimestamp)
	}

	if err = tx.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	err = tx.Select(
		"id, user_id, username, token_id, token_name, model_name, created_at, request_id, upstream_request_id, method, request_path, status_code, is_stream, channel_id, " +
			logGroupCol + " as " + logGroupCol + ", request_content_type, response_content_type, request_size, response_size, request_omitted_reason, response_omitted_reason, redacted",
	).Order("id desc").Limit(params.Num).Offset(params.StartIdx).Find(&logs).Error
	if err != nil {
		return nil, 0, err
	}
	err = attachAPIRequestLogListUsage(logs)
	return logs, total, err
}

func GetAPIRequestLogById(id int) (*APIRequestLog, error) {
	var log APIRequestLog
	if err := LOG_DB.First(&log, id).Error; err != nil {
		return nil, err
	}
	usage, err := GetAPIRequestLogUsage(log.RequestId, log.UpstreamRequestId, true)
	if err != nil {
		return nil, err
	}
	log.Usage = usage
	return &log, nil
}

func GetAPIRequestLogUsage(requestId string, upstreamRequestId string, includeBody bool) (*APIRequestLogUsage, error) {
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

func attachAPIRequestLogListUsage(logs []*APIRequestLogListItem) error {
	if len(logs) == 0 {
		return nil
	}
	requestIds := make([]string, 0, len(logs))
	upstreamRequestIds := make([]string, 0, len(logs))
	for _, item := range logs {
		if item == nil {
			continue
		}
		if item.RequestId != "" {
			requestIds = append(requestIds, item.RequestId)
		}
		if item.UpstreamRequestId != "" {
			upstreamRequestIds = append(upstreamRequestIds, item.UpstreamRequestId)
		}
	}
	if len(requestIds) == 0 && len(upstreamRequestIds) == 0 {
		return nil
	}

	var usageLogs []*Log
	tx := LOG_DB.Model(&Log{}).Where("type = ?", LogTypeConsume)
	if len(requestIds) > 0 && len(upstreamRequestIds) > 0 {
		tx = tx.Where("(request_id IN ? OR upstream_request_id IN ?)", requestIds, upstreamRequestIds)
	} else if len(requestIds) > 0 {
		tx = tx.Where("request_id IN ?", requestIds)
	} else {
		tx = tx.Where("upstream_request_id IN ?", upstreamRequestIds)
	}
	if err := tx.Order("id desc").Find(&usageLogs).Error; err != nil {
		return err
	}

	byRequestId := make(map[string]*APIRequestLogUsage)
	byUpstreamRequestId := make(map[string]*APIRequestLogUsage)
	for _, usageLog := range usageLogs {
		usage := apiRequestUsageFromLog(usageLog, false)
		if usageLog.RequestId != "" {
			if _, exists := byRequestId[usageLog.RequestId]; !exists {
				byRequestId[usageLog.RequestId] = usage
			}
		}
		if usageLog.UpstreamRequestId != "" {
			if _, exists := byUpstreamRequestId[usageLog.UpstreamRequestId]; !exists {
				byUpstreamRequestId[usageLog.UpstreamRequestId] = usage
			}
		}
	}

	for _, item := range logs {
		if item == nil {
			continue
		}
		if item.RequestId != "" {
			if usage, exists := byRequestId[item.RequestId]; exists {
				item.Usage = usage
				continue
			}
		}
		if item.UpstreamRequestId != "" {
			item.Usage = byUpstreamRequestId[item.UpstreamRequestId]
		}
	}

	return nil
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

func GetAPIRequestLogStorageStatus() (*APIRequestLogStorageStatus, error) {
	status := &APIRequestLogStorageStatus{}
	apiRequestLogEnsureMu.Lock()
	status.LastWriteError = apiRequestLogLastWriteError
	status.LastWriteErrorAt = apiRequestLogLastWriteErrorAt
	apiRequestLogEnsureMu.Unlock()

	if LOG_DB == nil {
		status.LastWriteError = "log database is not initialized"
		status.EnsureMigrationFailed = true
		return status, nil
	}
	if LOG_DB.Dialector != nil {
		status.LogDBDialect = LOG_DB.Dialector.Name()
	}

	status.HasTable = LOG_DB.Migrator().HasTable(&APIRequestLog{})
	if !status.HasTable {
		if err := EnsureAPIRequestLogTable(); err != nil {
			status.EnsureMigrationFailed = true
			status.LastWriteError = err.Error()
			status.LastWriteErrorAt = common.GetTimestamp()
			return status, nil
		}
		status.HasTable = LOG_DB.Migrator().HasTable(&APIRequestLog{})
	}
	if !status.HasTable {
		return status, nil
	}

	if err := LOG_DB.Model(&APIRequestLog{}).Count(&status.Count).Error; err != nil {
		return status, err
	}

	var last APIRequestLog
	result := LOG_DB.Select("created_at, request_id").Order("id desc").Limit(1).Find(&last)
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
