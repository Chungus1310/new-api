package service

import (
	"fmt"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

// StartFreeConsentPruner runs on the master node only, once a day.
func StartFreeConsentPruner() {
	if !common.IsMasterNode {
		return
	}
	go func() {
		if n, err := model.BackfillFreeConsentProviders(); err != nil {
			common.SysError("free consent provider backfill failed: " + err.Error())
		} else if n > 0 {
			common.SysLog(fmt.Sprintf("recorded the provider set of %d earlier free consents", n))
		}
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for ; true; <-ticker.C {
			if n, err := model.PruneGuestOwnPurposeObjections(time.Now()); err != nil {
				common.SysError("guest objection prune failed: " + err.Error())
			} else if n > 0 {
				common.SysLog(fmt.Sprintf("pruned %d guest objections past the guest cookie lifetime", n))
			}
			n, err := model.PruneFreeConsents(time.Now())
			if err != nil {
				common.SysError("free consent prune failed: " + err.Error())
				continue
			}
			if n > 0 {
				common.SysLog(fmt.Sprintf("pruned %d free consent records past the proof period", n))
			}
		}
	}()
}
