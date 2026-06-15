package middleware

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func setupAPIRequestLogMiddlewareTest(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.APIRequestLog{}); err != nil {
		t.Fatal(err)
	}

	oldLogDB := model.LOG_DB
	oldEnabled := common.APIRequestLogEnabled
	oldCaptureResponse := common.APIRequestLogCaptureResponse
	oldRedactSecrets := common.APIRequestLogRedactSecrets
	model.LOG_DB = db
	common.APIRequestLogEnabled = true
	common.APIRequestLogCaptureResponse = true
	common.APIRequestLogRedactSecrets = true
	t.Cleanup(func() {
		model.LOG_DB = oldLogDB
		common.APIRequestLogEnabled = oldEnabled
		common.APIRequestLogCaptureResponse = oldCaptureResponse
		common.APIRequestLogRedactSecrets = oldRedactSecrets
	})
	return db
}

func TestAPIRequestLogGlobalCaptureRecordsTokenRelayRequest(t *testing.T) {
	db := setupAPIRequestLogMiddlewareTest(t)

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(BodyStorageCleanup(), APIRequestLogGlobalCapture())
	router.POST("/v1/chat/completions", func(c *gin.Context) {
		c.Set("id", 2)
		c.Set("username", "alice")
		c.Set("token_id", 9)
		c.Set("token_name", "prod-token")
		c.Set("original_model", "gpt-5.5")
		c.JSON(http.StatusOK, gin.H{"id": "chatcmpl-test"})
	})

	body := bytes.NewBufferString(`{"model":"gpt-5.5","messages":[{"role":"user","content":"hello"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", body)
	req.Header.Set("Authorization", "Bearer sk-test")
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", recorder.Code)
	}

	var logs []model.APIRequestLog
	if err := db.Find(&logs).Error; err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected one request log, got %d", len(logs))
	}
	if logs[0].RequestPath != "/v1/chat/completions" || logs[0].ModelName != "gpt-5.5" {
		t.Fatalf("unexpected request log: %+v", logs[0])
	}
	if !strings.Contains(string(logs[0].RequestBody), "hello") {
		t.Fatalf("expected request body to be captured, got %s", logs[0].RequestBody)
	}
	if !strings.Contains(string(logs[0].ResponseBody), "chatcmpl-test") {
		t.Fatalf("expected response body to be captured, got %s", logs[0].ResponseBody)
	}
}

func TestAPIRequestLogGlobalCaptureRecordsModelList(t *testing.T) {
	db := setupAPIRequestLogMiddlewareTest(t)

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(BodyStorageCleanup(), APIRequestLogGlobalCapture())
	router.GET("/v1/models", func(c *gin.Context) {
		c.Set("id", 3)
		c.Set("username", "bob")
		c.Set("token_id", 12)
		c.Set("token_name", "sdk-token")
		c.JSON(http.StatusOK, gin.H{"data": []gin.H{{"id": "gpt-test"}}})
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer sk-test")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", recorder.Code)
	}

	var logs []model.APIRequestLog
	if err := db.Find(&logs).Error; err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected one request log, got %d", len(logs))
	}
	if logs[0].RequestPath != "/v1/models" || logs[0].TokenName != "sdk-token" {
		t.Fatalf("unexpected model list request log: %+v", logs[0])
	}
	if !strings.Contains(string(logs[0].ResponseBody), "gpt-test") {
		t.Fatalf("expected model list response to be captured, got %s", logs[0].ResponseBody)
	}
}

func TestAPIRequestLogGlobalCaptureKeepsRootAliasOriginalPath(t *testing.T) {
	db := setupAPIRequestLogMiddlewareTest(t)

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(BodyStorageCleanup(), APIRequestLogGlobalCapture(), NormalizeOpenAICompatibleRootPath())
	router.POST("/chat/completions", func(c *gin.Context) {
		if c.Request.URL.Path != "/v1/chat/completions" {
			t.Fatalf("expected normalized path, got %s", c.Request.URL.Path)
		}
		c.Set("id", 4)
		c.Set("username", "carol")
		c.Set("token_id", 13)
		c.Set("token_name", "root-base-url")
		c.Set("original_model", "gpt-root")
		c.JSON(http.StatusOK, gin.H{"id": "chatcmpl-root"})
	})

	body := bytes.NewBufferString(`{"model":"gpt-root","messages":[{"role":"user","content":"hello"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/chat/completions", body)
	req.Header.Set("Authorization", "Bearer sk-test")
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", recorder.Code)
	}

	var logs []model.APIRequestLog
	if err := db.Find(&logs).Error; err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected one request log, got %d", len(logs))
	}
	if logs[0].RequestPath != "/chat/completions" {
		t.Fatalf("expected original request path to be logged, got %s", logs[0].RequestPath)
	}
}

func TestAPIRequestLogGlobalCaptureSkipsDashboardAPI(t *testing.T) {
	db := setupAPIRequestLogMiddlewareTest(t)

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(BodyStorageCleanup(), APIRequestLogGlobalCapture())
	router.GET("/api/status", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"success": true})
	})

	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	req.Header.Set("Authorization", "Bearer dashboard-access-token")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", recorder.Code)
	}

	var count int64
	if err := db.Model(&model.APIRequestLog{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("expected no dashboard request logs, got %d", count)
	}
}

func TestNormalizeOpenAICompatibleRootPath(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(NormalizeOpenAICompatibleRootPath())
	router.POST("/chat/completions", func(c *gin.Context) {
		if c.Request.URL.Path != "/v1/chat/completions" {
			t.Fatalf("expected normalized path, got %s", c.Request.URL.Path)
		}
		c.Status(http.StatusNoContent)
	})
	router.GET("/models", func(c *gin.Context) {
		if c.Request.URL.Path != "/models" {
			t.Fatalf("expected models frontend path to remain unchanged, got %s", c.Request.URL.Path)
		}
		c.Status(http.StatusNoContent)
	})

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/chat/completions", nil))
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", recorder.Code)
	}

	recorder = httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/models", nil))
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", recorder.Code)
	}
}
