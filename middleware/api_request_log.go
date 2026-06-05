package middleware

import (
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
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

func APIRequestLogGlobalCapture() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !shouldCaptureAPIRequestLogRequest(c) {
			c.Next()
			return
		}
		if _, exists := c.Get(RouteTagKey); !exists {
			c.Set(RouteTagKey, "relay")
		}
		service.StartAPIRequestLogCapture(c)
		c.Next()
		service.RecordAPIRequestLog(c, nil, nil)
	}
}

func NormalizeOpenAICompatibleRootPath() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c != nil && c.Request != nil && c.Request.URL != nil && isOpenAICompatibleRootRelayPath(c.Request.URL.Path) {
			c.Set(service.APIRequestLogOriginalPathKey, c.Request.URL.Path)
			c.Request.URL.Path = "/v1" + c.Request.URL.Path
			if c.Request.URL.RawPath != "" {
				c.Request.URL.RawPath = "/v1" + c.Request.URL.RawPath
			}
		}
		c.Next()
	}
}

func shouldCaptureAPIRequestLogRequest(c *gin.Context) bool {
	if c == nil || c.Request == nil || c.Request.URL == nil || !common.APIRequestLogEnabled {
		return false
	}
	if c.Request.Method == http.MethodOptions {
		return false
	}
	path := c.Request.URL.Path
	if isRealtimeRelayPath(path) || c.IsWebsocket() {
		return false
	}
	if isDashboardOrAssetPath(path) {
		return false
	}
	return hasAPIRequestCredential(c) && isRelayAPIPath(path)
}

func isDashboardOrAssetPath(path string) bool {
	return hasPathPrefix(path, "/api") ||
		hasPathPrefix(path, "/assets") ||
		hasPathPrefix(path, "/console") ||
		hasPathPrefix(path, "/dashboard")
}

func hasAPIRequestCredential(c *gin.Context) bool {
	if strings.TrimSpace(c.GetHeader("Authorization")) != "" ||
		strings.TrimSpace(c.GetHeader("x-api-key")) != "" ||
		strings.TrimSpace(c.GetHeader("x-goog-api-key")) != "" ||
		strings.TrimSpace(c.GetHeader("mj-api-secret")) != "" {
		return true
	}
	return strings.TrimSpace(c.Query("key")) != ""
}

func isRelayAPIPath(path string) bool {
	if hasPathPrefix(path, "/v1") ||
		hasPathPrefix(path, "/v1beta") ||
		hasPathPrefix(path, "/pg") ||
		hasPathPrefix(path, "/mj") ||
		hasPathPrefix(path, "/suno") ||
		hasPathPrefix(path, "/kling") ||
		hasPathPrefix(path, "/jimeng") {
		return true
	}
	if strings.Contains(path, "/mj/") {
		return true
	}
	return isOpenAICompatibleRootRelayPath(path)
}

func isRealtimeRelayPath(path string) bool {
	return hasPathPrefix(path, "/v1/realtime") || hasPathPrefix(path, "/realtime")
}

func hasPathPrefix(path string, prefix string) bool {
	return path == prefix || strings.HasPrefix(path, prefix+"/")
}

func isOpenAICompatibleRootRelayPath(path string) bool {
	switch {
	case hasPathPrefix(path, "/messages"),
		hasPathPrefix(path, "/completions"),
		hasPathPrefix(path, "/chat/completions"),
		hasPathPrefix(path, "/responses"),
		hasPathPrefix(path, "/edits"),
		hasPathPrefix(path, "/images"),
		hasPathPrefix(path, "/embeddings"),
		hasPathPrefix(path, "/audio"),
		hasPathPrefix(path, "/rerank"),
		hasPathPrefix(path, "/engines"),
		hasPathPrefix(path, "/moderations"),
		hasPathPrefix(path, "/files"),
		hasPathPrefix(path, "/fine-tunes"),
		hasPathPrefix(path, "/video"),
		hasPathPrefix(path, "/videos"):
		return true
	default:
		return false
	}
}
