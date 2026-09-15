package middleware

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestGuestTestAuthenticationAndRoleRevocation(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "auth.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}))
	oldDB := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = oldDB })
	user := model.User{Id: 1, Username: "roles", Role: common.RoleAdminUser, Status: common.UserStatusEnabled}
	require.NoError(t, db.Create(&user).Error)
	for _, role := range []int{common.RoleGuestUser, common.RoleTestUser, common.RoleAdminUser} {
		engine := gin.New()
		engine.Use(sessions.Sessions("roles", cookie.NewStore([]byte("role-auth-test-secret"))))
		engine.GET("/login", func(c *gin.Context) {
			s := sessions.Default(c)
			s.Set("id", 1)
			s.Set("username", "roles")
			s.Set("role", role)
			s.Set("status", common.UserStatusEnabled)
			require.NoError(t, s.Save())
		})
		engine.GET("/self", UserAuth(), func(c *gin.Context) { c.Status(http.StatusNoContent) })
		engine.GET("/admin", AdminAuth(), func(c *gin.Context) { c.Status(http.StatusNoContent) })
		login := httptest.NewRecorder()
		engine.ServeHTTP(login, httptest.NewRequest("GET", "/login", nil))
		request := func(path string, authenticated bool) int {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest("GET", path, nil)
			req.Header.Set("New-Api-User", "1")
			if authenticated {
				for _, cookie := range login.Result().Cookies() {
					req.AddCookie(cookie)
				}
			}
			engine.ServeHTTP(rec, req)
			return rec.Code
		}
		require.Equal(t, http.StatusNoContent, request("/self", true))
		require.Equal(t, http.StatusUnauthorized, request("/self", false))
		require.NoError(t, db.Model(&user).Update("role", role).Error)
		if role == common.RoleAdminUser {
			require.Equal(t, http.StatusNoContent, request("/admin", true))
			require.NoError(t, db.Model(&user).Update("role", common.RoleGuestUser).Error)
		}
		require.NotEqual(t, http.StatusNoContent, request("/admin", true))
	}
}
