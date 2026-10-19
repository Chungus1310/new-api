package controller

import (
	"cmp"
	"slices"
	"strings"

	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"

	"github.com/go-fuego/fuego"
)

const guestOnlyBFF = "guest consent routes are limited to the chat service"

func listProviderConsents(c dto.FuegoCtx, guest bool) (*dto.Response[dto.ProviderConsentListData], error) {
	userId, guestId, ok := freeConsentSubject(c, guest)
	if !ok {
		return dto.Fail[dto.ProviderConsentListData](guestOnlyBFF)
	}
	active, err := model.ActiveProviderConsents(userId, guestId)
	if err != nil {
		return dto.Fail[dto.ProviderConsentListData](err.Error())
	}
	givenAt := make(map[string]int64, len(active))
	for _, consent := range active {
		givenAt[consent.Alias] = consent.CreatedAt
	}
	catalog := model.ConsentProviders()
	byProvider := make(map[string]model.ConsentProvider, len(catalog))
	for _, p := range catalog {
		byProvider[p.Provider] = p
	}
	item := func(p model.ConsentProvider, status string) dto.ProviderConsentItem {
		models := p.Models
		if models == nil {
			models = []string{}
		}
		label := p.Label
		if label == "" {
			label = model.ProviderLabel(p.Tags, p.Alias)
		}
		entry := dto.ProviderConsentItem{Alias: p.Alias, Label: label, Country: p.Country, Unsafe: p.Unsafe, TrainsOrRetains: p.TrainsOrRetains, ModelCount: len(models), Models: models, Status: status}
		entry.Tags = p.Tags
		if entry.Tags == nil {
			entry.Tags = []string{}
		}
		entry.LaneCount, entry.ActiveLaneCount, entry.Active = p.LaneCount, p.ActiveLaneCount, p.Active
		entry.ChannelCount, entry.ActiveChannelCount = p.ChannelCount, p.ActiveChannelCount
		entry.ModelDetails = make([]dto.ProviderConsentModelItem, 0, len(models))
		for _, name := range models {
			activeChannels := p.ActiveModelChannels[name]
			if activeChannels > 0 {
				entry.ActiveModelCount++
			}
			entry.ModelDetails = append(entry.ModelDetails, dto.ProviderConsentModelItem{Name: name, Active: activeChannels > 0, Channels: p.ModelChannels[name], ActiveChannels: activeChannels})
		}
		if at, ok := givenAt[p.Alias]; ok {
			entry.Status = "given"
			entry.GivenAt = at
		}
		return entry
	}

	modelName := strings.TrimSpace(dto.GinCtx(c).Query("model"))
	if modelName == "" {
		items := make([]dto.ProviderConsentItem, 0, len(catalog))
		for _, p := range catalog {
			items = append(items, item(p, "missing"))
		}
		sortProviderConsentItems(items)
		return dto.Ok(dto.ProviderConsentListData{Providers: items})
	}

	scope := model.RecordedConsentScope(modelName)
	covered := true
	items := []dto.ProviderConsentItem{}
	if scope.Required {
		modelCovered, err := model.ModelConsentProviders(userId, guestId, scope.Model)
		if err != nil {
			return dto.Fail[dto.ProviderConsentListData](err.Error())
		}
		aliases := model.ProviderAliasesFor(scope.Providers)
		for _, provider := range scope.Providers {
			p, ok := byProvider[provider]
			if !ok {
				alias, assigned := aliases[provider]
				if !assigned {
					// Not in the catalog yet, so no alias to consent to: the model stays uncovered.
					covered = false
					continue
				}
				p = model.ConsentProvider{Provider: provider, Alias: alias.Alias, Country: alias.Country}
			}
			status := "missing"
			if modelCovered[provider] {
				status = "model_consent"
			}
			entry := item(p, status)
			if entry.Status == "missing" {
				covered = false
			}
			items = append(items, entry)
		}
	}
	sortProviderConsentItems(items)
	return dto.Ok(dto.ProviderConsentListData{Model: scope.Model, Covered: &covered, Providers: items})
}

// sortProviderConsentItems puts active providers first, then those with the
// most enabled channels, since each channel takes its share of the traffic,
// then models and lanes.
func sortProviderConsentItems(items []dto.ProviderConsentItem) {
	slices.SortStableFunc(items, func(a, b dto.ProviderConsentItem) int {
		if a.Active != b.Active {
			if a.Active {
				return -1
			}
			return 1
		}
		if c := cmp.Compare(b.ActiveChannelCount, a.ActiveChannelCount); c != 0 {
			return c
		}
		if c := cmp.Compare(b.ActiveModelCount, a.ActiveModelCount); c != 0 {
			return c
		}
		if c := cmp.Compare(b.ActiveLaneCount, a.ActiveLaneCount); c != 0 {
			return c
		}
		return strings.Compare(a.Alias, b.Alias)
	})
}

func grantProviderConsents(c fuego.ContextWithBody[dto.ProviderConsentGrantRequest], guest bool) (*dto.Response[dto.ProviderConsentGrantData], error) {
	userId, guestId, ok := freeConsentSubject(c, guest)
	if !ok {
		return dto.Fail[dto.ProviderConsentGrantData](guestOnlyBFF)
	}
	req, err := c.Body()
	if err != nil {
		return dto.Fail[dto.ProviderConsentGrantData]("Invalid parameters")
	}
	if strings.TrimSpace(req.Text) == "" {
		return dto.Fail[dto.ProviderConsentGrantData]("consent needs the text shown")
	}
	modelName := strings.TrimSpace(req.Model)
	if (modelName == "") == (len(req.Aliases) == 0) {
		return dto.Fail[dto.ProviderConsentGrantData]("send either aliases or a model")
	}
	var targets []model.ProviderAlias
	if modelName != "" {
		scope := model.RecordedConsentScope(modelName)
		if !scope.Required {
			return dto.Fail[dto.ProviderConsentGrantData]("this model needs no consent")
		}
		// Assigns aliases to providers that appeared since the last listing.
		model.ConsentProviders()
		active, err := model.ActiveProviderConsents(userId, guestId)
		if err != nil {
			return dto.Fail[dto.ProviderConsentGrantData](err.Error())
		}
		given := make(map[string]bool, len(active))
		for _, consent := range active {
			given[consent.Alias] = true
		}
		aliases := model.ProviderAliasesFor(scope.Providers)
		for _, provider := range scope.Providers {
			alias, ok := aliases[provider]
			if !ok {
				return dto.Fail[dto.ProviderConsentGrantData]("a provider of this model has no alias yet, try again shortly")
			}
			if !given[alias.Alias] {
				targets = append(targets, alias)
			}
		}
	} else {
		seen := map[string]bool{}
		for _, name := range req.Aliases {
			name = strings.TrimSpace(name)
			if seen[name] {
				continue
			}
			seen[name] = true
			alias, ok := model.ProviderAliasByName(name)
			if !ok {
				return dto.Fail[dto.ProviderConsentGrantData]("unknown provider alias " + name)
			}
			targets = append(targets, alias)
		}
	}
	labels := map[string]string{}
	for _, p := range model.ConsentProviders() {
		labels[p.Alias] = p.Label
	}
	granted := make([]string, 0, len(targets))
	var unnamed []string
	for _, alias := range targets {
		granted = append(granted, alias.Alias)
		label := labels[alias.Alias]
		if label == "" {
			label = alias.Alias
		}
		if !strings.Contains(req.Text, alias.Alias) && !strings.Contains(req.Text, label) {
			unnamed = append(unnamed, label)
		}
	}
	if len(unnamed) > 0 {
		return dto.Fail[dto.ProviderConsentGrantData]("the consent text must name every provider granted: " + strings.Join(unnamed, "; "))
	}
	if len(targets) > 0 {
		if err := model.GrantProviderConsents(userId, guestId, targets, req.Locale, req.Text); err != nil {
			return dto.Fail[dto.ProviderConsentGrantData](err.Error())
		}
	}
	return dto.Ok(dto.ProviderConsentGrantData{Granted: granted})
}

func revokeProviderConsent(c fuego.ContextWithBody[dto.ProviderConsentRevokeRequest], guest bool) (*dto.Response[dto.MessageResponse], error) {
	userId, guestId, ok := freeConsentSubject(c, guest)
	if !ok {
		return dto.Fail[dto.MessageResponse](guestOnlyBFF)
	}
	req, err := c.Body()
	if err != nil {
		return dto.Fail[dto.MessageResponse]("Invalid parameters")
	}
	if err := model.RevokeProviderConsent(userId, guestId, strings.TrimSpace(req.Alias)); err != nil {
		return dto.Fail[dto.MessageResponse](err.Error())
	}
	return dto.Ok(dto.MessageResponse{Message: "ok"})
}

func GetProviderConsents(c fuego.ContextNoBody) (*dto.Response[dto.ProviderConsentListData], error) {
	return listProviderConsents(c, false)
}

func GrantProviderConsents(c fuego.ContextWithBody[dto.ProviderConsentGrantRequest]) (*dto.Response[dto.ProviderConsentGrantData], error) {
	return grantProviderConsents(c, false)
}

func RevokeProviderConsent(c fuego.ContextWithBody[dto.ProviderConsentRevokeRequest]) (*dto.Response[dto.MessageResponse], error) {
	return revokeProviderConsent(c, false)
}

func GetGuestProviderConsents(c fuego.ContextNoBody) (*dto.Response[dto.ProviderConsentListData], error) {
	return listProviderConsents(c, true)
}

func GrantGuestProviderConsents(c fuego.ContextWithBody[dto.ProviderConsentGrantRequest]) (*dto.Response[dto.ProviderConsentGrantData], error) {
	return grantProviderConsents(c, true)
}

func RevokeGuestProviderConsent(c fuego.ContextWithBody[dto.ProviderConsentRevokeRequest]) (*dto.Response[dto.MessageResponse], error) {
	return revokeProviderConsent(c, true)
}
