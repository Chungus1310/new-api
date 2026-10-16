package model

import (
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

// FreeConsent documents a consent to one :free model under Art. 4 No. 11 GDPR.
// Text is the dialog exactly as shown, because the consent is only provable
// together with what the person agreed to. Exactly one of UserId and GuestId is
// set; a guest is a browser identified by the chat BFF's guest cookie.
type FreeConsent struct {
	Id        int    `json:"id" gorm:"primaryKey"`
	UserId    int    `json:"user_id" gorm:"index:idx_free_consent_user;not null;default:0"`
	GuestId   string `json:"-" gorm:"type:varchar(64);index:idx_free_consent_guest;not null;default:''"`
	Model     string `json:"model" gorm:"type:varchar(255);not null"`
	Locale    string `json:"locale" gorm:"type:varchar(16);not null;default:''"`
	Text      string `json:"-" gorm:"type:text;not null"`
	CreatedAt int64  `json:"created_at" gorm:"bigint;not null"`
	RevokedAt int64  `json:"revoked_at" gorm:"bigint;not null;default:0"`
}

const freeConsentCacheTTL = 30 * time.Second

type freeConsentEntry struct {
	models   map[string]bool
	business bool
	expires  time.Time
}

// Per replica: a revoke reaches the other replicas within the TTL.
var freeConsentCache sync.Map

func freeConsentSubject(userId int, guestId string) string {
	if guestId != "" {
		return "g:" + guestId
	}
	return "u:" + strconv.Itoa(userId)
}

func freeConsentScope(userId int, guestId string) (string, any) {
	if guestId != "" {
		return "guest_id = ? AND revoked_at = 0", guestId
	}
	return "user_id = ? AND guest_id = '' AND revoked_at = 0", userId
}

// ActiveFreeConsents lists the subject's unrevoked consents, newest first.
func ActiveFreeConsents(userId int, guestId string) ([]FreeConsent, error) {
	where, arg := freeConsentScope(userId, guestId)
	var consents []FreeConsent
	err := DB.Where(where, arg).Order("created_at desc").Find(&consents).Error
	return consents, err
}

type FreeAccess int

const (
	FreeAccessAllowed FreeAccess = iota
	FreeAccessNeedsConsent
	FreeAccessBusiness
)

// FreeAccessFor is the relay-path check; a lookup error denies.
func FreeAccessFor(userId int, guestId string, modelName string) FreeAccess {
	subject := freeConsentSubject(userId, guestId)
	entry, ok := loadFreeConsentEntry(subject)
	// Only a cached yes is trusted: a consent just given on another replica must
	// not be refused for the rest of this replica's TTL.
	if !ok || (!entry.business && !entry.models[modelName]) {
		var err error
		entry, err = buildFreeConsentEntry(userId, guestId)
		if err != nil {
			common.SysError("failed to load free consents: " + err.Error())
			return FreeAccessNeedsConsent
		}
		freeConsentCache.Store(subject, entry)
	}
	if entry.business {
		return FreeAccessBusiness
	}
	if !entry.models[modelName] {
		return FreeAccessNeedsConsent
	}
	return FreeAccessAllowed
}

func loadFreeConsentEntry(subject string) (freeConsentEntry, bool) {
	cached, ok := freeConsentCache.Load(subject)
	if !ok {
		return freeConsentEntry{}, false
	}
	entry, ok := cached.(freeConsentEntry)
	if !ok || time.Now().After(entry.expires) {
		return freeConsentEntry{}, false
	}
	return entry, true
}

func buildFreeConsentEntry(userId int, guestId string) (freeConsentEntry, error) {
	entry := freeConsentEntry{models: map[string]bool{}, expires: time.Now().Add(freeConsentCacheTTL)}
	if guestId == "" {
		var customerType string
		if err := DB.Model(&User{}).Where("id = ?", userId).Select("customer_type").Scan(&customerType).Error; err != nil {
			return entry, err
		}
		entry.business = customerType == CustomerTypeBusiness
	}
	consents, err := ActiveFreeConsents(userId, guestId)
	if err != nil {
		return entry, err
	}
	for _, consent := range consents {
		entry.models[consent.Model] = true
	}
	return entry, nil
}

// GrantFreeConsent records a new consent; an earlier one for the same model is
// revoked so each model has at most one active record.
func GrantFreeConsent(userId int, guestId string, modelName, locale, text string) error {
	if (userId == 0) == (guestId == "") {
		return errors.New("free consent needs exactly one of user and guest")
	}
	now := common.GetTimestamp()
	where, arg := freeConsentScope(userId, guestId)
	err := DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&FreeConsent{}).Where(where, arg).Where("model = ?", modelName).Update("revoked_at", now).Error; err != nil {
			return err
		}
		return tx.Create(&FreeConsent{UserId: userId, GuestId: guestId, Model: modelName, Locale: locale, Text: text, CreatedAt: now}).Error
	})
	freeConsentCache.Delete(freeConsentSubject(userId, guestId))
	return err
}

// RevokeFreeConsent ends the consent for one model; the record stays as proof.
func RevokeFreeConsent(userId int, guestId string, modelName string) error {
	where, arg := freeConsentScope(userId, guestId)
	err := DB.Model(&FreeConsent{}).Where(where, arg).Where("model = ?", modelName).Update("revoked_at", common.GetTimestamp()).Error
	freeConsentCache.Delete(freeConsentSubject(userId, guestId))
	return err
}

// freeConsentProofYears is how long a consent record outlives the consent, as
// the privacy policy states, so it can still be proven.
const freeConsentProofYears = 3

// A guest id lives in a one-year cookie set before any consent, so a guest's
// consent ends at the latest a year after it was given.
const guestIdLifetime = 365 * 24 * time.Hour

// PruneFreeConsents deletes records whose consent ended more than three years
// ago: revoked, the account deleted, or the guest cookie expired.
func PruneFreeConsents(now time.Time) (int64, error) {
	cutoff := now.AddDate(-freeConsentProofYears, 0, 0)
	res := DB.Where(
		"(revoked_at > 0 AND revoked_at < ?)"+
			" OR (revoked_at = 0 AND guest_id <> '' AND created_at < ?)"+
			" OR (revoked_at = 0 AND user_id > 0 AND user_id IN (SELECT id FROM users WHERE deleted_at IS NOT NULL AND deleted_at < ?))"+
			" OR (revoked_at = 0 AND user_id > 0 AND created_at < ? AND NOT EXISTS (SELECT 1 FROM users WHERE users.id = free_consents.user_id))",
		cutoff.Unix(), cutoff.Add(-guestIdLifetime).Unix(), cutoff, cutoff.Unix(),
	).Delete(&FreeConsent{})
	return res.RowsAffected, res.Error
}
