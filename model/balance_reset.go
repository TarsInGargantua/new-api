package model

import (
	"errors"
	"math"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// The policy follows Beijing midnight, independently of the host's timezone.
var balanceResetLocation = time.FixedZone("Asia/Shanghai", 8*60*60)

var ErrBalanceResetRole = errors.New("this role is excluded from balance resets")

func CanResetBalance(role int) bool {
	return role == common.RoleCommonUser || role == common.RoleAdminUser || role == common.RoleRootUser
}

func balanceResetRoles() []int {
	return []int{common.RoleCommonUser, common.RoleAdminUser, common.RoleRootUser}
}

var ErrBalanceResetUsername = errors.New("username confirmation does not match")

func BalanceResetDay(now time.Time) int64 {
	local := now.In(balanceResetLocation)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, balanceResetLocation).Unix()
}

// DailyBalanceQuota represents 100 in the site's current display unit.
func DailyBalanceQuota() (int, error) {
	if operation_setting.GetQuotaDisplayType() == operation_setting.QuotaDisplayTypeTokens {
		return 100, nil
	}
	rate := operation_setting.GetUsdToCurrencyRate(operation_setting.USDExchangeRate)
	quota := math.Round(100 * common.QuotaPerUnit / rate)
	if rate <= 0 || math.IsNaN(quota) || math.IsInf(quota, 0) || quota < 1 || quota >= float64(math.MaxInt) {
		return 0, errors.New("invalid currency configuration for balance reset")
	}
	return int(quota), nil
}

func resetDailyBalances(query *gorm.DB, now time.Time) error {
	quota, err := DailyBalanceQuota()
	if err != nil {
		return err
	}
	return query.Where("role IN ? AND balance_reset_at < ?", balanceResetRoles(), BalanceResetDay(now)).
		Updates(map[string]interface{}{"quota": quota, "balance_reset_at": BalanceResetDay(now)}).Error
}

// Persist the day on each user so restarts and multiple replicas cannot reset
// the same day's spent balance a second time. Soft-deleted users are excluded.
func ResetDailyBalances(now time.Time) error {
	return resetDailyBalances(DB.Model(&User{}), now)
}

func EnsureDailyBalance(userID int, now time.Time) error {
	return resetDailyBalances(DB.Model(&User{}).Where("id = ?", userID), now)
}

// One-time, explicitly requested normalization for the 2026-09-14 rollout.
// Persist the claim in the same transaction as the balances, so restarting or
// starting another replica cannot overwrite subsequent spending again.
func NormalizeBalancesSeptember2026(now time.Time) error {
	quota, err := DailyBalanceQuota()
	if err != nil {
		return err
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		claim := Option{Key: "migration.balance_normalization_20260914_v1", Value: common.GetUUID()}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&claim).Error; err != nil {
			return err
		}
		var saved Option
		if err := tx.Where(&Option{Key: claim.Key}).First(&saved).Error; err != nil {
			return err
		}
		// Compare the unique claim rather than RowsAffected, whose no-op upsert
		// semantics depend on the MySQL client's found-rows configuration.
		if saved.Value != claim.Value {
			return nil
		}
		return tx.Model(&User{}).Where("role IN ?", balanceResetRoles()).
			Updates(map[string]interface{}{"quota": quota, "balance_reset_at": BalanceResetDay(now)}).Error
	})
}

func ResetOwnBalance(userID int, username string, now time.Time) (int, error) {
	quota, err := DailyBalanceQuota()
	if err != nil {
		return 0, err
	}
	err = DB.Transaction(func(tx *gorm.DB) error {
		// SQLite ignores SELECT FOR UPDATE. Acquire its write lock before the
		// username read so a concurrent relay cannot invalidate our snapshot
		// and make the later write fail with SQLITE_BUSY_SNAPSHOT.
		if tx.Dialector.Name() == "sqlite" {
			if err := tx.Model(&User{}).Where("id = ?", userID).
				UpdateColumn("balance_reset_at", gorm.Expr("balance_reset_at")).Error; err != nil {
				return err
			}
		}
		var user User
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id", "username", "role").First(&user, userID).Error; err != nil {
			return err
		}
		if !CanResetBalance(user.Role) {
			return ErrBalanceResetRole
		}
		if username != user.Username {
			return ErrBalanceResetUsername
		}
		// The authenticated ID, never a client-supplied ID, selects the account.
		result := tx.Model(&User{}).Where("id = ? AND username = ? AND role IN ?", userID, username, balanceResetRoles()).
			Updates(map[string]interface{}{"quota": quota, "balance_reset_at": BalanceResetDay(now)})
		return result.Error
	})
	return quota, err
}

// Wallet changes are synchronous and atomic with the daily reset. Deferring
// wallet deltas in a process-local batch would replay yesterday's spending
// after a reset. Usage, token and channel counters may still be batched.
func changeUserBalance(userID int, delta int, now time.Time) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		if err := resetDailyBalances(tx.Model(&User{}).Where("id = ?", userID), now); err != nil {
			return err
		}
		return tx.Model(&User{}).Where("id = ?", userID).
			Update("quota", gorm.Expr("quota + ?", delta)).Error
	})
}

// New accounts start with today's allowance before their first dashboard visit.
func (user *User) initializeDailyBalance() error {
	if !common.IsValidateRole(user.Role) {
		return errors.New("invalid user role")
	}
	user.Quota = 0
	user.BalanceResetAt = 0
	if !CanResetBalance(user.Role) {
		return nil
	}
	quota, err := DailyBalanceQuota()
	if err != nil {
		return err
	}
	user.Quota = quota
	user.BalanceResetAt = BalanceResetDay(time.Now())
	return nil
}
