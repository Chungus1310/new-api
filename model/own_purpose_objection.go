package model

import (
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm/clause"
)

// OwnPurposeObjection records a guest's objection under Art. 21 GDPR to
// passing inputs to providers that use them for their own purposes. A logged-in
// user's objection lives in the user setting instead; for guests the chat BFF
// enforces it.
type OwnPurposeObjection struct {
	Id        int    `json:"-" gorm:"primaryKey"`
	GuestId   string `json:"-" gorm:"type:varchar(64);uniqueIndex:idx_own_purpose_objection_guest;not null"`
	CreatedAt int64  `json:"-" gorm:"bigint;not null"`
}

func GuestObjectsToOwnPurposes(guestId string) (bool, error) {
	var count int64
	err := DB.Model(&OwnPurposeObjection{}).Where("guest_id = ?", guestId).Count(&count).Error
	return count > 0, err
}

func SetGuestOwnPurposeObjection(guestId string, objects bool) error {
	if !objects {
		return DB.Where("guest_id = ?", guestId).Delete(&OwnPurposeObjection{}).Error
	}
	return DB.Clauses(clause.OnConflict{DoNothing: true}).
		Create(&OwnPurposeObjection{GuestId: guestId, CreatedAt: common.GetTimestamp()}).Error
}

// PruneGuestOwnPurposeObjections drops objections whose guest cookie has expired.
func PruneGuestOwnPurposeObjections(now time.Time) (int64, error) {
	res := DB.Where("created_at < ?", now.Add(-guestIdLifetime).Unix()).Delete(&OwnPurposeObjection{})
	return res.RowsAffected, res.Error
}
