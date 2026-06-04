package middleware

import (
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

func APIRequestLogCapture() gin.HandlerFunc {
	return func(c *gin.Context) {
		service.StartAPIRequestLogCapture(c)
		c.Next()
		service.RecordAPIRequestLog(c, nil, nil)
	}
}
