package controller

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestUserRoleCreationAndEdit(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "roles.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Log{}, &model.Token{}))
	oldDB, oldLog, oldRedis := model.DB, model.LOG_DB, common.RedisEnabled
	model.DB, model.LOG_DB, common.RedisEnabled = db, db, false
	oldReg, oldPass, oldEmail := common.RegisterEnabled, common.PasswordRegisterEnabled, common.EmailVerificationEnabled
	common.RegisterEnabled, common.PasswordRegisterEnabled, common.EmailVerificationEnabled = true, true, false
	t.Cleanup(func() {
		model.DB, model.LOG_DB, common.RedisEnabled = oldDB, oldLog, oldRedis
		common.RegisterEnabled, common.PasswordRegisterEnabled, common.EmailVerificationEnabled = oldReg, oldPass, oldEmail
	})
	call := func(handler gin.HandlerFunc, body string, operatorRole int) bool {
		rec := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(rec)
		ctx.Set("role", operatorRole)
		ctx.Request = httptest.NewRequest(http.MethodPost, "/api/user/", strings.NewReader(body))
		handler(ctx)
		var result struct {
			Success bool `json:"success"`
		}
		require.NoError(t, common.Unmarshal(rec.Body.Bytes(), &result), rec.Body.String())
		return result.Success
	}
	require.True(t, call(Register, `{"username":"registered","password":"secure-test-pass","role":100,"quota":99999}`, 0))
	var registered model.User
	require.NoError(t, db.Where("username = ?", "registered").First(&registered).Error)
	require.Equal(t, common.RoleGuestUser, registered.Role)
	require.Zero(t, registered.Quota)
	require.True(t, call(CreateUser, `{"username":"created","password":"secure-test-pass"}`, common.RoleAdminUser))
	var created model.User
	require.NoError(t, db.Where("username = ?", "created").First(&created).Error)
	require.Zero(t, created.Role)
	require.Zero(t, created.Quota)
	require.False(t, call(CreateUser, `{"username":"invalid","password":"secure-test-pass","role":9}`, common.RoleRootUser))
	require.False(t, call(CreateUser, `{"username":"elevated","password":"secure-test-pass","role":10}`, common.RoleAdminUser))
	user := model.User{Id: 100, Username: "editable", Role: common.RoleCommonUser, AffCode: "edit", Quota: 123}
	require.NoError(t, db.Create(&user).Error)
	require.True(t, call(UpdateUser, `{"id":100,"username":"editable","display_name":"updated"}`, common.RoleAdminUser))
	require.NoError(t, db.First(&user, 100).Error)
	require.Equal(t, common.RoleCommonUser, user.Role)
	for _, body := range []string{`{"id":100,"username":"editable","role":2}`, `{"id":100,"username":"editable","role":0}`} {
		require.True(t, call(UpdateUser, body, common.RoleAdminUser))
	}
	require.NoError(t, db.First(&user, 100).Error)
	require.Zero(t, user.Role)
	require.Equal(t, 123, user.Quota)
	require.False(t, call(UpdateUser, `{"id":100,"username":"editable","role":10}`, common.RoleAdminUser))
	require.False(t, call(UpdateUser, `{"id":100,"username":"editable","role":9}`, common.RoleRootUser))
	require.NoError(t, db.Model(&user).Update("role", common.RoleRootUser).Error)
	require.False(t, call(UpdateUser, `{"id":100,"username":"editable","role":0}`, common.RoleRootUser))
}
