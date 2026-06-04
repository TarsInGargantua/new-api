package model

import (
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
	Id                    int               `json:"id" gorm:"index:idx_api_request_logs_created_at_id,priority:1"`
	UserId                int               `json:"user_id" gorm:"index"`
	Username              string            `json:"username" gorm:"index;default:''"`
	TokenId               int               `json:"token_id" gorm:"index;default:0"`
	TokenName             string            `json:"token_name" gorm:"index;default:''"`
	ModelName             string            `json:"model_name" gorm:"index;default:''"`
	CreatedAt             int64             `json:"created_at" gorm:"bigint;index:idx_api_request_logs_created_at_id,priority:2"`
	RequestId             string            `json:"request_id,omitempty" gorm:"type:varchar(64);index;default:''"`
	UpstreamRequestId     string            `json:"upstream_request_id,omitempty" gorm:"type:varchar(128);index;default:''"`
	Method                string            `json:"method" gorm:"type:varchar(16);default:''"`
	RequestPath           string            `json:"request_path" gorm:"index;default:''"`
	StatusCode            int               `json:"status_code" gorm:"index;default:0"`
	IsStream              bool              `json:"is_stream"`
	ChannelId             int               `json:"channel_id" gorm:"index;default:0"`
	Group                 string            `json:"group" gorm:"index;default:''"`
	RequestContentType    string            `json:"request_content_type" gorm:"default:''"`
	ResponseContentType   string            `json:"response_content_type" gorm:"default:''"`
	RequestSize           int64             `json:"request_size" gorm:"default:0"`
	ResponseSize          int64             `json:"response_size" gorm:"default:0"`
	RequestOmittedReason  string            `json:"request_omitted_reason,omitempty" gorm:"default:''"`
	ResponseOmittedReason string            `json:"response_omitted_reason,omitempty" gorm:"default:''"`
	Redacted              bool              `json:"redacted"`
	RequestBody           APIRequestLogBody `json:"request_body,omitempty"`
	ResponseBody          APIRequestLogBody `json:"response_body,omitempty"`
	Metadata              APIRequestLogBody `json:"metadata,omitempty"`
}

type APIRequestLogListItem struct {
	Id                    int    `json:"id"`
	UserId                int    `json:"user_id"`
	Username              string `json:"username"`
	TokenId               int    `json:"token_id"`
	TokenName             string `json:"token_name"`
	ModelName             string `json:"model_name"`
	CreatedAt             int64  `json:"created_at"`
	RequestId             string `json:"request_id,omitempty"`
	UpstreamRequestId     string `json:"upstream_request_id,omitempty"`
	Method                string `json:"method"`
	RequestPath           string `json:"request_path"`
	StatusCode            int    `json:"status_code"`
	IsStream              bool   `json:"is_stream"`
	ChannelId             int    `json:"channel_id"`
	Group                 string `json:"group"`
	RequestContentType    string `json:"request_content_type"`
	ResponseContentType   string `json:"response_content_type"`
	RequestSize           int64  `json:"request_size"`
	ResponseSize          int64  `json:"response_size"`
	RequestOmittedReason  string `json:"request_omitted_reason,omitempty"`
	ResponseOmittedReason string `json:"response_omitted_reason,omitempty"`
	Redacted              bool   `json:"redacted"`
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

func CreateAPIRequestLog(log *APIRequestLog) error {
	return LOG_DB.Create(log).Error
}

func GetAPIRequestLogs(params APIRequestLogQueryParams) (logs []*APIRequestLogListItem, total int64, err error) {
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
	return logs, total, err
}

func GetAPIRequestLogById(id int) (*APIRequestLog, error) {
	var log APIRequestLog
	if err := LOG_DB.First(&log, id).Error; err != nil {
		return nil, err
	}
	return &log, nil
}
