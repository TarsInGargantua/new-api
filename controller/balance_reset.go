package controller

import (
	"errors"
	"net/http"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

func ResetOwnBalance(c *gin.Context) {
	var request struct {
		Username string `json:"username"`
	}
	if err := common.DecodeJson(c.Request.Body, &request); err != nil || request.Username == "" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "请输入自己的用户名以确认重置"})
		return
	}
	quota, err := model.ResetOwnBalance(c.GetInt("id"), request.Username, time.Now())
	if errors.Is(err, model.ErrBalanceResetRole) || errors.Is(err, model.ErrBalanceResetUsername) {
		message := "用户名不匹配，请输入自己的用户名"
		if errors.Is(err, model.ErrBalanceResetRole) {
			message = "当前角色不参与余额重置"
		}
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": message})
		return
	}
	if err != nil {
		common.ApiError(c, err)
		return
	}
	model.RecordLog(c.GetInt("id"), model.LogTypeSystem, "用户手动重置余额为当前显示单位的 100")
	common.ApiSuccess(c, gin.H{"quota": quota})
}
