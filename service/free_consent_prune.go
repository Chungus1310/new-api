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
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for ; true; <-ticker.C {
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
