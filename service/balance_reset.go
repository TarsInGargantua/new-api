package service

import (
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

// Run on every replica: the persisted per-user date makes this idempotent.
// A startup pass catches missed midnights; request-time checks cover traffic
// arriving before the scheduled pass completes.
func RunDailyBalanceReset() {
	for {
		now := time.Now()
		err := model.ResetDailyBalances(now)
		if err != nil {
			common.SysError("daily balance reset failed: " + err.Error())
			time.Sleep(time.Minute)
			continue
		}
		next := time.Unix(model.BalanceResetDay(now), 0).Add(24 * time.Hour)
		time.Sleep(time.Until(next))
	}
}
