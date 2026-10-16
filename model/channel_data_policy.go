package model

import (
	"strings"
	"sync/atomic"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	kitdto "github.com/QuantumNous/new-api/relaykit/dto"
)

type groupDataPolicySnapshot struct {
	generation uint64
	byGroup    map[string]dto.GroupDataPolicy
}

var (
	groupDataPolicyGeneration atomic.Uint64
	groupDataPolicyCache      atomic.Pointer[groupDataPolicySnapshot]
)

func invalidateGroupDataPolicies() {
	groupDataPolicyGeneration.Add(1)
}

// GroupDataPolicies returns the data-protection profile of every group in
// which at least one channel records a data_policy. The map is shared and must
// not be modified.
func GroupDataPolicies() map[string]dto.GroupDataPolicy {
	if !common.MemoryCacheEnabled {
		var channels []*Channel
		err := DB.Select("id", commonGroupCol, "setting").Find(&channels).Error
		if err != nil {
			common.SysLog("failed to load channel data policies: " + err.Error())
			return map[string]dto.GroupDataPolicy{}
		}
		return mergeGroupDataPolicies(channels)
	}
	// Read the generation first so an invalidation that lands mid-build leaves
	// the stored snapshot stale instead of current.
	generation := groupDataPolicyGeneration.Load()
	if cached := groupDataPolicyCache.Load(); cached != nil && cached.generation == generation {
		return cached.byGroup
	}
	channelSyncLock.RLock()
	channels := make([]*Channel, 0, len(channelsIDM))
	for _, channel := range channelsIDM {
		channels = append(channels, &Channel{Id: channel.Id, Group: channel.Group, Setting: channel.Setting})
	}
	channelSyncLock.RUnlock()
	byGroup := mergeGroupDataPolicies(channels)
	groupDataPolicyCache.Store(&groupDataPolicySnapshot{generation: generation, byGroup: byGroup})
	return byGroup
}

// mergeGroupDataPolicies folds the channels of each group into the most
// conservative profile. A channel without a policy counts as unknown, and a
// group where no channel has one is left out.
func mergeGroupDataPolicies(channels []*Channel) map[string]dto.GroupDataPolicy {
	merged := make(map[string]dto.GroupDataPolicy)
	recorded := make(map[string]bool)
	for _, channel := range channels {
		// Not GetSetting: on a parse error it saves the channel, and the DB path
		// loads only three columns.
		var setting kitdto.ChannelSettings
		if channel.Setting != nil && *channel.Setting != "" {
			_ = common.UnmarshalJsonStr(*channel.Setting, &setting)
		}
		policy := setting.DataPolicy
		current := kitdto.ChannelDataPolicy{}
		if policy != nil {
			current = *policy
		}
		for group := range strings.SplitSeq(channel.Group, ",") {
			if group == "" {
				continue
			}
			if policy != nil {
				recorded[group] = true
			}
			existing, seen := merged[group]
			if !seen {
				merged[group] = dto.GroupDataPolicy{
					SeatCountry:        current.SeatCountry,
					Prompts:            orUnknown(current.Prompts),
					Transfer:           orUnknown(current.Transfer),
					UnsafeThirdCountry: current.UnsafeThirdCountry(),
				}
				continue
			}
			if seatRisk(current.SeatCountry) > seatRisk(existing.SeatCountry) ||
				(seatRisk(current.SeatCountry) == seatRisk(existing.SeatCountry) && current.SeatCountry < existing.SeatCountry) {
				existing.SeatCountry = current.SeatCountry
			}
			if promptsRisk[orUnknown(current.Prompts)] > promptsRisk[existing.Prompts] {
				existing.Prompts = orUnknown(current.Prompts)
			}
			if transferRisk[orUnknown(current.Transfer)] > transferRisk[existing.Transfer] {
				existing.Transfer = orUnknown(current.Transfer)
			}
			existing.UnsafeThirdCountry = existing.UnsafeThirdCountry || current.UnsafeThirdCountry()
			merged[group] = existing
		}
	}
	for group := range merged {
		if !recorded[group] {
			delete(merged, group)
		}
	}
	return merged
}

var promptsRisk = map[string]int{
	kitdto.DataPolicyPromptsNone:    0,
	kitdto.DataPolicyUnknown:        1,
	kitdto.DataPolicyPromptsRetains: 2,
	kitdto.DataPolicyPromptsTrains:  3,
}

var transferRisk = map[string]int{
	kitdto.DataPolicyTransferAdequacy: 0,
	kitdto.DataPolicyTransferDPF:      1,
	kitdto.DataPolicyTransferSCC:      2,
	kitdto.DataPolicyUnknown:          3,
	kitdto.DataPolicyTransferNone:     4,
}

func seatRisk(country string) int {
	switch {
	case country == "":
		return 2
	case kitdto.IsEEACountry(country):
		return 0
	case kitdto.IsEUAdequacyCountry(country):
		return 1
	default:
		return 3
	}
}

func orUnknown(value string) string {
	if value == "" {
		return kitdto.DataPolicyUnknown
	}
	return value
}
