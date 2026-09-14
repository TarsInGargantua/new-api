package controller

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestResetOwnBalanceAuthenticatedAccountOnly(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "balance.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Log{}))
	oldRedis := common.RedisEnabled
	common.RedisEnabled = false
	t.Cleanup(func() { common.RedisEnabled = oldRedis })
	oldDB, oldLogDB := model.DB, model.LOG_DB
	model.DB, model.LOG_DB = db, db
	t.Cleanup(func() { model.DB, model.LOG_DB = oldDB, oldLogDB })
	users := []model.User{
		{Id: 1, Username: "alice", AffCode: "alice", Quota: 5, BalanceResetAt: model.BalanceResetDay(time.Now())},
		{Id: 2, Username: "bob", AffCode: "bob", Quota: 6},
		{Id: 3, Username: "admin", AffCode: "admin", Quota: 7},
	}
	require.NoError(t, db.Create(&users).Error)
	for _, test := range []struct {
		name   string
		id     int
		body   string
		status int
	}{
		{"missing confirmation", 1, `{}`, http.StatusBadRequest},
		{"wrong username", 1, `{"username":"bob"}`, http.StatusBadRequest},
		{"admin excluded", 3, `{"username":"admin"}`, http.StatusBadRequest},
		{"reset own account despite forged id", 1, `{"username":"alice","user_id":2}`, http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Set("id", test.id)
			ctx.Request = httptest.NewRequest(http.MethodPost, "/api/user/self/reset_balance", strings.NewReader(test.body))
			ResetOwnBalance(ctx)
			require.Equal(t, test.status, recorder.Code)
			var response struct {
				Success bool `json:"success"`
			}
			require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
			require.Equal(t, test.status == http.StatusOK, response.Success)
		})
	}
	var alice, bob, admin model.User
	require.NoError(t, db.First(&alice, 1).Error)
	require.NoError(t, db.First(&bob, 2).Error)
	require.NoError(t, db.First(&admin, 3).Error)
	target, err := model.DailyBalanceQuota()
	require.NoError(t, err)
	require.Equal(t, target, alice.Quota)
	require.Equal(t, 6, bob.Quota)
	require.Equal(t, 7, admin.Quota)
}
