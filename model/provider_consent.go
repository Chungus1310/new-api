package model

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	kitdto "github.com/QuantumNous/new-api/relaykit/dto"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ProviderAlias is the public code name of a provider, its country plus a
// sequence per country (CN-07). Provider is internal and never leaves the
// gateway. Rows are never deleted, so an alias is never reused.
type ProviderAlias struct {
	Id        int    `json:"-" gorm:"primaryKey"`
	Provider  string `json:"-" gorm:"type:varchar(255);uniqueIndex:uk_provider_alias_provider;not null"`
	Alias     string `json:"alias" gorm:"type:varchar(16);uniqueIndex:uk_provider_alias_alias;not null"`
	Country   string `json:"country" gorm:"type:varchar(8);index:idx_provider_alias_country;not null;default:''"`
	Seq       int    `json:"-" gorm:"not null;default:0"`
	CreatedAt int64  `json:"created_at" gorm:"bigint;not null"`
}

// ProviderConsent documents a consent to one provider, kept and pruned like
// FreeConsent. Text is the dialog exactly as shown.
type ProviderConsent struct {
	Id        int    `json:"id" gorm:"primaryKey"`
	UserId    int    `json:"user_id" gorm:"index:idx_provider_consent_user;not null;default:0"`
	GuestId   string `json:"-" gorm:"type:varchar(64);index:idx_provider_consent_guest;not null;default:''"`
	Alias     string `json:"alias" gorm:"type:varchar(16);not null"`
	Country   string `json:"country" gorm:"type:varchar(8);not null;default:''"`
	Locale    string `json:"locale" gorm:"type:varchar(16);not null;default:''"`
	Text      string `json:"-" gorm:"type:text;not null"`
	CreatedAt int64  `json:"created_at" gorm:"bigint;not null"`
	RevokedAt int64  `json:"revoked_at" gorm:"bigint;not null;default:0"`
}

// unknownAliasCountry prefixes the alias of a provider whose seat is not recorded.
const unknownAliasCountry = "XX"

type providerAliasIndex struct {
	byProvider map[string]ProviderAlias
	byAlias    map[string]ProviderAlias
}

var (
	providerAliasCache    atomic.Pointer[providerAliasIndex]
	providerAliasLoadedAt atomic.Int64
	providerAliasMu       sync.Mutex
)

func loadProviderAliases() (*providerAliasIndex, error) {
	var rows []ProviderAlias
	if err := DB.Find(&rows).Error; err != nil {
		return nil, err
	}
	index := &providerAliasIndex{byProvider: make(map[string]ProviderAlias, len(rows)), byAlias: make(map[string]ProviderAlias, len(rows))}
	for _, row := range rows {
		index.byProvider[row.Provider] = row
		index.byAlias[row.Alias] = row
	}
	providerAliasCache.Store(index)
	providerAliasLoadedAt.Store(time.Now().Unix())
	return index, nil
}

// providerAliases is the cached index; a miss reloads it at most every 10s,
// since another replica may have assigned the alias.
func providerAliases(missing ...string) *providerAliasIndex {
	index := providerAliasCache.Load()
	stale := index == nil
	if !stale {
		for _, p := range missing {
			if _, ok := index.byProvider[p]; !ok {
				stale = time.Now().Unix()-providerAliasLoadedAt.Load() >= 10
				break
			}
		}
	}
	if !stale {
		return index
	}
	loaded, err := loadProviderAliases()
	if err != nil {
		common.SysError("failed to load provider aliases: " + err.Error())
		if index == nil {
			return &providerAliasIndex{byProvider: map[string]ProviderAlias{}, byAlias: map[string]ProviderAlias{}}
		}
		return index
	}
	return loaded
}

// AssignProviderAliases gives every consent-requiring provider without an alias
// the next free one of its country. Safe on several replicas: a lost race on
// the unique alias is retried with the next sequence.
func AssignProviderAliases(providers map[string]string) error {
	pendingIn := func(index *providerAliasIndex) []string {
		var pending []string
		for p := range providers {
			if _, ok := index.byProvider[p]; !ok {
				pending = append(pending, p)
			}
		}
		return pending
	}
	pending := pendingIn(providerAliases())
	if len(pending) > 0 {
		index, err := loadProviderAliases()
		if err != nil {
			return err
		}
		pending = pendingIn(index)
	}
	if len(pending) == 0 {
		return nil
	}
	providerAliasMu.Lock()
	defer providerAliasMu.Unlock()
	slices.Sort(pending)
	for _, p := range pending {
		country := strings.ToUpper(strings.TrimSpace(providers[p]))
		if len(country) != 2 {
			country = unknownAliasCountry
		}
		for attempt := 0; ; attempt++ {
			var existing int64
			if err := DB.Model(&ProviderAlias{}).Where("provider = ?", p).Count(&existing).Error; err != nil {
				return err
			}
			if existing > 0 {
				break
			}
			var maxSeq int
			if err := DB.Model(&ProviderAlias{}).Where("country = ?", country).Select("COALESCE(MAX(seq), 0)").Scan(&maxSeq).Error; err != nil {
				return err
			}
			row := ProviderAlias{Provider: p, Alias: fmt.Sprintf("%s-%02d", country, maxSeq+1), Country: country, Seq: maxSeq + 1, CreatedAt: common.GetTimestamp()}
			res := DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected > 0 || attempt >= 5 {
				break
			}
		}
	}
	_, err := loadProviderAliases()
	return err
}

// ConsentProvider is one provider that some model needs a consent for.
// Provider is internal and must never reach an API response.
type ConsentProvider struct {
	Provider        string
	Alias           string
	Country         string
	Unsafe          bool
	TrainsOrRetains bool
	Models          []string
	// Lane tags of the provider's channels, as they appear in group names.
	Tags            []string
	LaneCount       int
	ActiveLaneCount int
	// Models with an enabled channel of this provider.
	ActiveModels map[string]bool
	Active       bool
}

type consentProviderCatalog struct {
	builtAt   time.Time
	providers []ConsentProvider
}

var (
	consentProviderCache atomic.Pointer[consentProviderCatalog]
	consentProviderMu    sync.Mutex
)

const consentProviderTTL = 60 * time.Second

// ConsentProviders lists every provider a model in the catalog needs a consent
// for, offline models included, with aliases assigned. Shared and cached.
func ConsentProviders() []ConsentProvider {
	if cached := consentProviderCache.Load(); cached != nil && time.Since(cached.builtAt) < consentProviderTTL {
		return cached.providers
	}
	consentProviderMu.Lock()
	defer consentProviderMu.Unlock()
	if cached := consentProviderCache.Load(); cached != nil && time.Since(cached.builtAt) < consentProviderTTL {
		return cached.providers
	}
	state := groupDataPolicyState()
	pricing := GetPricing()
	var offline []string
	for _, p := range pricing {
		if len(p.EnableGroup) == 0 {
			offline = append(offline, p.ModelName)
		}
	}
	listed := map[string][]string{}
	if len(offline) > 0 {
		listed = ListedModelGroups(offline)
	}
	byProvider := map[string]*ConsentProvider{}
	for _, p := range pricing {
		groups := p.EnableGroup
		if len(groups) == 0 {
			groups = listed[p.ModelName]
		}
		scope := consentScope(state, p.ModelName, groups)
		if !scope.Required {
			continue
		}
		for _, provider := range scope.Providers {
			entry := byProvider[provider]
			if entry == nil {
				entry = &ConsentProvider{Provider: provider}
				byProvider[provider] = entry
			}
			entry.Models = append(entry.Models, p.ModelName)
		}
	}
	fillConsentProviderFacts(state, byProvider)
	fillConsentProviderLanes(byProvider)
	countries := make(map[string]string, len(byProvider))
	for p, entry := range byProvider {
		countries[p] = entry.Country
	}
	if err := AssignProviderAliases(countries); err != nil {
		common.SysError("failed to assign provider aliases: " + err.Error())
	}
	index := providerAliases()
	out := make([]ConsentProvider, 0, len(byProvider))
	for p, entry := range byProvider {
		alias, ok := index.byProvider[p]
		if !ok {
			continue
		}
		entry.Alias = alias.Alias
		if entry.Country == "" && alias.Country != unknownAliasCountry {
			entry.Country = alias.Country
		}
		slices.Sort(entry.Models)
		out = append(out, *entry)
	}
	slices.SortFunc(out, func(a, b ConsentProvider) int { return strings.Compare(a.Alias, b.Alias) })
	consentProviderCache.Store(&consentProviderCatalog{builtAt: time.Now(), providers: out})
	return out
}

func fillConsentProviderFacts(state *groupDataPolicySnapshot, byProvider map[string]*ConsentProvider) {
	for _, recipients := range state.recipients {
		for _, r := range recipients.ownPurpose {
			if entry := byProvider[r.provider]; entry != nil {
				entry.TrainsOrRetains = true
				entry.Unsafe = entry.Unsafe || r.unsafe
				if entry.Country == "" {
					entry.Country = r.country
				}
			}
		}
		for _, r := range recipients.unknown {
			if entry := byProvider[r.provider]; entry != nil {
				entry.Unsafe = entry.Unsafe || r.unsafe
				if entry.Country == "" {
					entry.Country = r.country
				}
			}
		}
	}
}

// fillConsentProviderLanes counts each provider's lanes (channel tags) and
// marks lanes and models that have an enabled channel.
func fillConsentProviderLanes(byProvider map[string]*ConsentProvider) {
	var channels []*Channel
	if common.MemoryCacheEnabled {
		channelSyncLock.RLock()
		for _, channel := range channelsIDM {
			channels = append(channels, channel)
		}
		channelSyncLock.RUnlock()
	} else if err := DB.Select("id", "tag", "status", "models", "setting").Find(&channels).Error; err != nil {
		common.SysError("failed to load channels for consent providers: " + err.Error())
		return
	}
	lanes := map[string]map[string]bool{}
	for _, channel := range channels {
		var setting kitdto.ChannelSettings
		if channel.Setting != nil && *channel.Setting != "" {
			_ = common.UnmarshalJsonStr(*channel.Setting, &setting)
		}
		provider := ""
		if setting.DataPolicy != nil {
			provider = setting.DataPolicy.Provider
		}
		if provider == "" {
			provider = "channel:" + strconv.Itoa(channel.Id)
		}
		entry := byProvider[provider]
		if entry == nil {
			continue
		}
		enabled := channel.Status == common.ChannelStatusEnabled
		lane := "#" + strconv.Itoa(channel.Id)
		if channel.Tag != nil && strings.TrimSpace(*channel.Tag) != "" {
			lane = strings.TrimSpace(*channel.Tag)
		}
		if lanes[provider] == nil {
			lanes[provider] = map[string]bool{}
		}
		lanes[provider][lane] = lanes[provider][lane] || enabled
		if !enabled {
			continue
		}
		entry.Active = true
		if entry.ActiveModels == nil {
			entry.ActiveModels = map[string]bool{}
		}
		for _, m := range channel.GetModels() {
			entry.ActiveModels[strings.TrimSpace(m)] = true
		}
	}
	for provider, entry := range byProvider {
		entry.Tags = []string{}
		for lane, active := range lanes[provider] {
			entry.LaneCount++
			if active {
				entry.ActiveLaneCount++
			}
			if !strings.HasPrefix(lane, "#") {
				entry.Tags = append(entry.Tags, lane)
			}
		}
		slices.Sort(entry.Tags)
	}
}

// ProviderAliasesFor maps internal providers to their aliases; a provider
// without one yet is left out.
func ProviderAliasesFor(providers []string) map[string]ProviderAlias {
	index := providerAliases(providers...)
	out := make(map[string]ProviderAlias, len(providers))
	for _, p := range providers {
		if alias, ok := index.byProvider[p]; ok {
			out[p] = alias
		}
	}
	return out
}

// ProviderAliasByName resolves a public alias.
func ProviderAliasByName(alias string) (ProviderAlias, bool) {
	index := providerAliases()
	row, ok := index.byAlias[alias]
	if !ok && time.Now().Unix()-providerAliasLoadedAt.Load() >= 10 {
		if loaded, err := loadProviderAliases(); err == nil {
			row, ok = loaded.byAlias[alias]
		}
	}
	return row, ok
}

// ActiveProviderConsents lists the subject's unrevoked provider consents.
func ActiveProviderConsents(userId int, guestId string) ([]ProviderConsent, error) {
	where, arg := freeConsentScope(userId, guestId)
	var consents []ProviderConsent
	err := DB.Where(where, arg).Order("created_at desc").Find(&consents).Error
	return consents, err
}

// GrantProviderConsents records one consent per alias in one transaction, each
// with the shown text; an earlier active consent for the same alias is revoked.
func GrantProviderConsents(userId int, guestId string, aliases []ProviderAlias, locale, text string) error {
	if (userId == 0) == (guestId == "") {
		return errors.New("provider consent needs exactly one of user and guest")
	}
	if len(aliases) == 0 {
		return errors.New("no provider to consent to")
	}
	now := common.GetTimestamp()
	where, arg := freeConsentScope(userId, guestId)
	err := DB.Transaction(func(tx *gorm.DB) error {
		for _, alias := range aliases {
			if err := tx.Model(&ProviderConsent{}).Where(where, arg).Where("alias = ?", alias.Alias).Update("revoked_at", now).Error; err != nil {
				return err
			}
			if err := tx.Create(&ProviderConsent{UserId: userId, GuestId: guestId, Alias: alias.Alias, Country: alias.Country, Locale: locale, Text: text, CreatedAt: now}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	freeConsentCache.Delete(freeConsentSubject(userId, guestId))
	return err
}

// RevokeProviderConsent ends the consent for one alias; the record stays as proof.
func RevokeProviderConsent(userId int, guestId string, alias string) error {
	where, arg := freeConsentScope(userId, guestId)
	err := DB.Model(&ProviderConsent{}).Where(where, arg).Where("alias = ?", alias).Update("revoked_at", common.GetTimestamp()).Error
	freeConsentCache.Delete(freeConsentSubject(userId, guestId))
	return err
}

// PruneProviderConsents applies the proof period of PruneFreeConsents.
func PruneProviderConsents(now time.Time) (int64, error) {
	cutoff := now.AddDate(-freeConsentProofYears, 0, 0)
	res := DB.Where(
		"(revoked_at > 0 AND revoked_at < ?)"+
			" OR (revoked_at = 0 AND guest_id <> '' AND created_at < ?)"+
			" OR (revoked_at = 0 AND user_id > 0 AND user_id IN (SELECT id FROM users WHERE deleted_at IS NOT NULL AND deleted_at < ?))"+
			" OR (revoked_at = 0 AND user_id > 0 AND created_at < ? AND NOT EXISTS (SELECT 1 FROM users WHERE users.id = provider_consents.user_id))",
		cutoff.Unix(), cutoff.Add(-guestIdLifetime).Unix(), cutoff, cutoff.Unix(),
	).Delete(&ProviderConsent{})
	return res.RowsAffected, res.Error
}

// ModelConsentProviders is the union of the provider sets the subject's active
// model consents for this model covered.
func ModelConsentProviders(userId int, guestId string, modelName string) (map[string]bool, error) {
	where, arg := freeConsentScope(userId, guestId)
	var consents []FreeConsent
	if err := DB.Where(where, arg).Where("model = ?", modelName).Find(&consents).Error; err != nil {
		return nil, err
	}
	covered := map[string]bool{}
	for _, consent := range consents {
		for _, p := range consent.providerSet() {
			covered[p] = true
		}
	}
	return covered, nil
}
