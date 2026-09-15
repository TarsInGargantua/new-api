package model

import (
	"fmt"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRoleBalancePolicy(t *testing.T) {
	setupAPIRequestLogTestDB(t)
	before := time.Date(2026, 9, 15, 15, 59, 59, 0, time.UTC)
	target, err := DailyBalanceQuota()
	require.NoError(t, err)
	for _, role := range []int{common.RoleGuestUser, common.RoleTestUser, common.RoleCommonUser, common.RoleAdminUser, common.RoleRootUser} {
		t.Run(fmt.Sprint(role), func(t *testing.T) {
			user := User{Username: fmt.Sprintf("role%d", role), AffCode: fmt.Sprint(role), Role: role, Quota: 17, UsedQuota: 31, RequestCount: 4}
			// The admin username must follow its role, with no name-based exception.
			if role == common.RoleRootUser {
				user.Username = "admin"
			}
			require.NoError(t, DB.Create(&user).Error)
			require.NoError(t, EnsureDailyBalance(user.Id, before))
			expected := 17
			if CanResetBalance(role) {
				expected = target
			}
			actual, err := GetUserById(user.Id, true)
			require.NoError(t, err)
			require.Equal(t, expected, actual.Quota)
			require.NoError(t, changeUserBalance(user.Id, -3, before))
			require.NoError(t, ResetDailyBalances(before.Add(time.Second)))
			require.NoError(t, DB.First(actual, user.Id).Error)
			if !CanResetBalance(role) {
				expected = 14
			}
			require.Equal(t, expected, actual.Quota)
			_, err = ResetOwnBalance(user.Id, user.Username, before.Add(time.Second))
			if CanResetBalance(role) {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, ErrBalanceResetRole)
			}
			require.NoError(t, DB.First(actual, user.Id).Error)
			require.Equal(t, expected, actual.Quota)
			require.Equal(t, 31, actual.UsedQuota)
			require.Equal(t, 4, actual.RequestCount)
		})
	}
}

func TestGuestAndTestCreationNeverGrantsSignupQuota(t *testing.T) {
	setupAPIRequestLogTestDB(t)
	oldNew, oldInvitee, oldInviter, oldRedis := common.QuotaForNewUser, common.QuotaForInvitee, common.QuotaForInviter, common.RedisEnabled
	settings := operation_setting.GetPaymentSetting()
	oldSettings := *settings
	t.Cleanup(func() {
		common.QuotaForNewUser, common.QuotaForInvitee, common.QuotaForInviter, common.RedisEnabled = oldNew, oldInvitee, oldInviter, oldRedis
		*settings = oldSettings
	})
	common.QuotaForNewUser, common.QuotaForInvitee, common.QuotaForInviter, common.RedisEnabled = 999, 888, 0, false
	settings.ComplianceConfirmed = true
	settings.ComplianceTermsVersion = operation_setting.CurrentComplianceTermsVersion
	for _, role := range []int{common.RoleGuestUser, common.RoleTestUser} {
		for _, oauth := range []bool{false, true} {
			user := User{Username: fmt.Sprintf("new%d%t", role, oauth), Role: role, Quota: 777}
			if oauth {
				require.NoError(t, DB.Transaction(func(tx *gorm.DB) error { return user.InsertWithTx(tx, 123) }))
				user.FinalizeOAuthUserCreation(123)
			} else {
				require.NoError(t, user.Insert(123))
			}
			actual, err := GetUserById(user.Id, true)
			require.NoError(t, err)
			require.Equal(t, role, actual.Role)
			require.Zero(t, actual.Quota)
			require.Zero(t, actual.BalanceResetAt)
		}
	}
	user := User{Username: "default-guest"}
	require.NoError(t, user.Insert(0))
	require.Equal(t, common.RoleGuestUser, user.Role)
	require.Zero(t, user.Quota)
}

func TestRoleEditPersistsZeroAndStopsReset(t *testing.T) {
	setupAPIRequestLogTestDB(t)
	oldRedis := common.RedisEnabled
	common.RedisEnabled = false
	t.Cleanup(func() { common.RedisEnabled = oldRedis })
	user := User{Username: "role-edit", Role: common.RoleCommonUser, Quota: 27, UsedQuota: 8, Group: "default"}
	require.NoError(t, DB.Create(&user).Error)
	staleProfile := user
	user.Role = common.RoleGuestUser
	require.NoError(t, user.Edit(false))
	staleProfile.DisplayName = "Updated profile"
	require.NoError(t, staleProfile.Update(false))
	require.NoError(t, ResetDailyBalances(time.Now()))
	actual, err := GetUserById(user.Id, true)
	require.NoError(t, err)
	require.Equal(t, common.RoleGuestUser, actual.Role)
	require.Equal(t, 27, actual.Quota)
	_, err = ResetOwnBalance(user.Id, user.Username, time.Now())
	require.ErrorIs(t, err, ErrBalanceResetRole)
	actual.Role = common.RoleCommonUser
	require.NoError(t, actual.Edit(false))
	require.NoError(t, EnsureDailyBalance(user.Id, time.Now()))
	require.NoError(t, DB.First(actual, user.Id).Error)
	target, err := DailyBalanceQuota()
	require.NoError(t, err)
	require.Equal(t, target, actual.Quota)
	require.Equal(t, 8, actual.UsedQuota)
}

type legacyUserRoleDefault struct {
	User
	Role int `gorm:"type:int;default:1"`
}

func (legacyUserRoleDefault) TableName() string { return "users" }

func TestGuestRoleDefaultMigrationPreservesExistingUsers(t *testing.T) {
	setupAPIRequestLogTestDB(t)
	require.NoError(t, DB.AutoMigrate(&legacyUserRoleDefault{}))
	old := legacyUserRoleDefault{User: User{Username: "legacy", AffCode: "legacy", Quota: 42, UsedQuota: 17}, Role: common.RoleAdminUser}
	require.NoError(t, DB.Create(&old).Error)
	require.NoError(t, DB.AutoMigrate(&User{}))
	var actual User
	require.NoError(t, DB.First(&actual, old.Id).Error)
	require.Equal(t, common.RoleAdminUser, actual.Role)
	require.Equal(t, 42, actual.Quota)
	require.Equal(t, 17, actual.UsedQuota)
	fresh := User{Username: "fresh", AffCode: "fresh"}
	require.NoError(t, DB.Create(&fresh).Error)
	require.Zero(t, fresh.Role)
}
