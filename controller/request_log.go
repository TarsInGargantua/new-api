package controller

import (
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
)

type apiRequestLogStatusResponse struct {
	Enabled         bool `json:"enabled"`
	RedactSecrets   bool `json:"redact_secrets"`
	CaptureResponse bool `json:"capture_response"`
	*model.APIRequestLogStorageStatus
}

func GetAPIRequestLogs(c *gin.Context) {
	pageInfo := common.GetPageQuery(c)
	startTimestamp, _ := strconv.ParseInt(c.Query("start_timestamp"), 10, 64)
	endTimestamp, _ := strconv.ParseInt(c.Query("end_timestamp"), 10, 64)
	logs, total, err := model.GetAPIRequestLogs(model.APIRequestLogQueryParams{
		StartTimestamp: startTimestamp,
		EndTimestamp:   endTimestamp,
		ModelName:      c.Query("model_name"),
		Username:       c.Query("username"),
		TokenName:      c.Query("token_name"),
		StartIdx:       pageInfo.GetStartIdx(),
		Num:            pageInfo.GetPageSize(),
	})
	if err != nil {
		common.ApiError(c, err)
		return
	}
	pageInfo.SetTotal(int(total))
	pageInfo.SetItems(logs)
	common.ApiSuccess(c, pageInfo)
}

func GetAPIRequestLog(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	if id <= 0 {
		common.ApiErrorMsg(c, "invalid request log id")
		return
	}
	log, err := model.GetAPIRequestLogById(id)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, log)
}

func GetAPIRequestLogStatus(c *gin.Context) {
	storageStatus, err := model.GetAPIRequestLogStorageStatus()
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, apiRequestLogStatusResponse{
		Enabled:                    common.LogConsumeEnabled,
		RedactSecrets:              common.AuditContentRedactSecrets,
		CaptureResponse:            common.AuditContentCaptureResponse,
		APIRequestLogStorageStatus: storageStatus,
	})
}
