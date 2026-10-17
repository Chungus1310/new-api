package controller

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	perfmetrics "github.com/QuantumNous/new-api/pkg/perf_metrics"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/go-fuego/fuego"
	"github.com/samber/lo"
	"golang.org/x/sync/singleflight"
)

func GetPerfMetricsSummary(c fuego.ContextWithParams[dto.GetPerfMetricsSummaryParams]) (*dto.Response[perfmetrics.SummaryAllResult], error) {
	p, err := dto.ParseParams[dto.GetPerfMetricsSummaryParams](c)
	if err != nil {
		return dto.Fail[perfmetrics.SummaryAllResult](err.Error())
	}
	hours := p.Hours
	if hours <= 0 {
		hours = 24
	}

	recentModels := dto.GinCtx(c).QueryArray("recent_model")
	if len(recentModels) > perfmetrics.MaxRecentModels {
		return dto.Fail[perfmetrics.SummaryAllResult]("Invalid parameters")
	}
	activeGroups := append(lo.Keys(ratio_setting.GetGroupRatioCopy()), "auto")
	sorted := slices.Clone(recentModels)
	slices.Sort(sorted)
	key := strconv.Itoa(hours) + "|" + strings.Join(sorted, ",")
	result, err := cachedPublicPerfSummary(key, func() (perfmetrics.SummaryAllResult, error) {
		return perfmetrics.QuerySummaryAll(hours, activeGroups, recentModels...)
	})
	if err != nil {
		return dto.Fail[perfmetrics.SummaryAllResult](err.Error())
	}
	return dto.Ok(result)
}

type publicPerfSummaryItem struct {
	expiresAt time.Time
	data      perfmetrics.SummaryAllResult
}

var (
	publicPerfSummaryMu     sync.Mutex
	publicPerfSummaryCache  = map[string]publicPerfSummaryItem{}
	publicPerfSummaryBuilds singleflight.Group
)

// cachedPublicPerfSummary serves the public summary for a minute per query: each call
// was up to 51 uncached aggregations, open to anonymous callers.
func cachedPublicPerfSummary(key string, build func() (perfmetrics.SummaryAllResult, error)) (perfmetrics.SummaryAllResult, error) {
	publicPerfSummaryMu.Lock()
	if item, ok := publicPerfSummaryCache[key]; ok && time.Now().Before(item.expiresAt) {
		publicPerfSummaryMu.Unlock()
		return item.data, nil
	}
	publicPerfSummaryMu.Unlock()
	built, err, _ := publicPerfSummaryBuilds.Do(key, func() (any, error) {
		data, err := build()
		if err != nil {
			return nil, err
		}
		publicPerfSummaryMu.Lock()
		// Keys are caller-chosen model lists; drop everything rather than grow forever.
		if len(publicPerfSummaryCache) >= 512 {
			clear(publicPerfSummaryCache)
		}
		publicPerfSummaryCache[key] = publicPerfSummaryItem{expiresAt: time.Now().Add(time.Minute), data: data}
		publicPerfSummaryMu.Unlock()
		return data, nil
	})
	if err != nil {
		return perfmetrics.SummaryAllResult{}, err
	}
	data, ok := built.(perfmetrics.SummaryAllResult)
	if !ok {
		return perfmetrics.SummaryAllResult{}, fmt.Errorf("unexpected perf summary result %T", built)
	}
	return data, nil
}

func GetPerfMetrics(c fuego.ContextWithParams[dto.GetPerfMetricsParams]) (*dto.Response[perfmetrics.QueryResult], error) {
	p, err := dto.ParseParams[dto.GetPerfMetricsParams](c)
	if err != nil {
		return dto.Fail[perfmetrics.QueryResult](err.Error())
	}
	if p.Model == "" {
		return dto.Fail[perfmetrics.QueryResult]("model is required")
	}
	hours := p.Hours
	if hours <= 0 {
		hours = 24
	}

	result, err := perfmetrics.Query(perfmetrics.QueryParams{
		Model:         p.Model,
		Group:         p.Group,
		Hours:         hours,
		AllowedGroups: append(lo.Keys(ratio_setting.GetGroupRatioCopy()), "auto"),
	})
	if err != nil {
		return dto.Fail[perfmetrics.QueryResult](err.Error())
	}

	attachGroupUptime(&result, p.Model, hours)
	return dto.Ok(result)
}

// Uptime comes from channel transition history, not from traffic, so it answers
// a different question than success_rate: a group nobody called still has an
// uptime. A failure here leaves the field nil rather than failing the request,
// since the latency and success columns are still worth serving.
func attachGroupUptime(result *perfmetrics.QueryResult, modelName string, hours int) {
	if len(result.Groups) == 0 {
		return
	}
	since := time.Now().Unix() - int64(hours)*3600
	uptime, err := model.GroupUptimeForModel(modelName, since)
	if err != nil {
		common.SysLog("perf metrics: group uptime failed: " + err.Error())
		return
	}
	for i := range result.Groups {
		if pct, ok := uptime[result.Groups[i].Group]; ok {
			result.Groups[i].UptimePercent = &pct
		}
	}
}
