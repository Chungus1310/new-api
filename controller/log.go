package controller

import (
	"net/http"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/go-fuego/fuego"

	"github.com/gin-gonic/gin"
)

// logFilterFromQuery reads the log search shared by the log list and the log
// export. Username and channel are read only for administrators.
func logFilterFromQuery(c *gin.Context, admin bool) model.LogFilter {
	logType, _ := strconv.Atoi(c.Query("type"))
	startTimestamp, _ := strconv.ParseInt(c.Query("start_timestamp"), 10, 64)
	endTimestamp, _ := strconv.ParseInt(c.Query("end_timestamp"), 10, 64)
	filter := model.LogFilter{
		Type:              logType,
		StartTimestamp:    startTimestamp,
		EndTimestamp:      endTimestamp,
		ModelName:         c.Query("model_name"),
		TokenName:         c.Query("token_name"),
		Group:             c.Query("group"),
		RequestId:         c.Query("request_id"),
		UpstreamRequestId: c.Query("upstream_request_id"),
	}
	filter.SubscriptionPlan = c.Query("subscription_plan")
	if admin {
		filter.Username = c.Query("username")
		filter.DiscordId = c.Query("discord_id")
		filter.Channel, _ = strconv.Atoi(c.Query("channel"))
	}
	return filter
}

// limitLogTimeRange returns the start of a log or dashboard search kept within
// the longest log time range. A missing start reaches back the whole range, and
// time after now does not count toward it.
func limitLogTimeRange(startTimestamp int64, endTimestamp int64) (int64, error) {
	maxRange := operation_setting.GetLogSetting().MaxRangeSeconds()
	end := common.GetTimestamp()
	if endTimestamp != 0 {
		end = min(end, endTimestamp)
	}
	if startTimestamp == 0 {
		return end - maxRange, nil
	}
	if end-startTimestamp > maxRange {
		return 0, common.NewMessage("The time range cannot exceed {{days}} days", map[string]any{"days": maxRange / (24 * 60 * 60)})
	}
	return startTimestamp, nil
}

func GetAllLogs(c fuego.ContextWithParams[dto.GetAllLogsParams]) (*dto.Response[dto.PageData[*model.Log]], error) {
	pageInfo := dto.PageInfo(c)
	p, _ := dto.ParseParams[dto.GetAllLogsParams](c)
	logs, total, err := model.GetAllLogs(model.LogFilter{
		Type:              p.Type,
		StartTimestamp:    p.StartTimestamp,
		EndTimestamp:      p.EndTimestamp,
		ModelName:         p.ModelName,
		Username:          p.Username,
		DiscordId:         p.DiscordId,
		TokenName:         p.TokenName,
		Channel:           p.Channel,
		Group:             p.Group,
		RequestId:         p.RequestID,
		UpstreamRequestId: p.UpstreamRequestID,
		SubscriptionPlan:  p.SubscriptionPlan,
	}, pageInfo.GetStartIdx(), pageInfo.GetPageSize())
	if err != nil {
		return dto.FailPage[*model.Log](err.Error())
	}
	ginCtx := dto.GinCtx(c)
	if ginCtx.GetInt("role") < common.RoleRootUser {
		model.FormatAdminLogs(logs)
	} else {
		model.FormatRootLogs(logs)
	}
	if ginCtx.GetInt("role") < common.RoleAdminUser {
		for _, l := range logs {
			l.Ip = ""
		}
	}
	// Cross-user log browsing: prompts are not stored, but usernames, models,
	// spend and channel routing for every account are.
	recordSensitiveRead(ginCtx, "read.log_search", map[string]interface{}{
		"count":    len(logs),
		"username": p.Username,
	})
	return dto.OkPage(pageInfo, logs, int(total))
}

// Sanity bound only (garbage timestamps), mirroring maxQuotaDateSpan: a bounded
// window keeps the per-user stat aggregation on its covering index instead of
// letting a zero start_timestamp scan the whole logs table.
const maxLogDateSpan = 10 * 365 * 86400

func logSpanTooWide(start, end int64) bool {
	return start != 0 && end != 0 && end-start > maxLogDateSpan
}

func GetUserLogs(c fuego.ContextWithParams[dto.GetUserLogsParams]) (*dto.Response[dto.PageData[*model.Log]], error) {
	pageInfo := dto.PageInfo(c)
	userId := dto.UserID(c)
	p, _ := dto.ParseParams[dto.GetUserLogsParams](c)
	if logSpanTooWide(p.StartTimestamp, p.EndTimestamp) {
		return dto.FailPage[*model.Log]("Time span cannot exceed 10 years")
	}
	filter := model.LogFilter{
		Type:              p.Type,
		StartTimestamp:    p.StartTimestamp,
		EndTimestamp:      p.EndTimestamp,
		ModelName:         p.ModelName,
		TokenName:         p.TokenName,
		Group:             p.Group,
		RequestId:         p.RequestID,
		UpstreamRequestId: p.UpstreamRequestID,
		SubscriptionPlan:  p.SubscriptionPlan,
	}
	if dto.UserRole(c) < common.RoleAdminUser {
		startTimestamp, err := limitLogTimeRange(filter.StartTimestamp, filter.EndTimestamp)
		if err != nil {
			return dto.FailPage[*model.Log](err.Error())
		}
		filter.StartTimestamp = startTimestamp
	}
	logs, total, err := model.GetUserLogs(userId, filter, pageInfo.GetStartIdx(), pageInfo.GetPageSize())
	if err != nil {
		return dto.FailPage[*model.Log](err.Error())
	}
	return dto.OkPage(pageInfo, logs, int(total))
}

// ExportAllLogs streams every user's logs matching the search.
func ExportAllLogs(c *gin.Context) {
	exportLogs(c, true)
}

// ExportUserLogs streams the caller's own logs matching the search.
func ExportUserLogs(c *gin.Context) {
	if c.GetInt("role") < common.RoleAdminUser && !operation_setting.GetLogSetting().UserExportEnabled {
		common.ApiErrorT(c, "Log export is disabled")
		return
	}
	exportLogs(c, false)
}

// exportLogs writes one JSON log per line, newest first, and ends with an API
// response object: success with the log count, or the failure. Each user runs
// one export at a time, and every export stays within the longest log time
// range.
func exportLogs(c *gin.Context, all bool) {
	filter := logFilterFromQuery(c, all)
	startTimestamp, err := limitLogTimeRange(filter.StartTimestamp, filter.EndTimestamp)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	filter.StartTimestamp = startTimestamp

	userId := c.GetInt("id")
	release, ok, err := service.AcquireLogExportLock(userId)
	if err != nil {
		common.SysError(common.LogText("failed to lock log export: %s", err.Error()))
		common.ApiErrorT(c, "Failed to export logs")
		return
	}
	if !ok {
		common.ApiErrorT(c, "Another log export is running. Try again after it finishes.")
		return
	}
	defer release()

	c.Header("Content-Type", "application/x-ndjson; charset=utf-8")
	c.Header("Cache-Control", "no-store")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)

	count := 0
	writeLog := func(log *model.Log) error {
		line, err := common.Marshal(log)
		if err != nil {
			return err
		}
		if _, err := c.Writer.Write(append(line, '\n')); err != nil {
			return err
		}
		count++
		return nil
	}
	ctx := c.Request.Context()
	if all {
		err = model.StreamAllLogs(ctx, filter, c.GetInt("role"), writeLog)
	} else {
		err = model.StreamUserLogs(ctx, userId, filter, writeLog)
	}
	if ctx.Err() != nil {
		return
	}
	// The status is already sent, so the result object becomes the last line.
	if err != nil {
		common.SysError(common.LogText("failed to export logs: %s", err.Error()))
		common.ApiErrorT(c, "Failed to export logs")
		return
	}
	common.ApiSuccess(c, gin.H{"count": count})
}

// Deprecated: SearchAllLogs 已废弃，前端未使用该接口。
func SearchAllLogs(c fuego.ContextNoBody) (dto.MessageResponse, error) {
	return dto.FailMsg("This API has been deprecated")
}

// Deprecated: SearchUserLogs 已废弃，前端未使用该接口。
func SearchUserLogs(c fuego.ContextNoBody) (dto.MessageResponse, error) {
	return dto.FailMsg("This API has been deprecated")
}

// GetLogByRequest resolves one log row by its full request_id. Public because a
// request_id is 62^8 of random suffix on top of a nanosecond timestamp, so it is
// not enumerable and whoever holds one already made that request.
func GetLogByRequest(c fuego.ContextWithParams[dto.GetLogByRequestParams]) (*dto.Response[dto.LogByRequestData], error) {
	p, _ := dto.ParseParams[dto.GetLogByRequestParams](c)
	if p.RequestID == "" {
		return dto.Fail[dto.LogByRequestData]("request_id is required")
	}
	l, err := model.GetLogByRequestId(p.RequestID)
	if err != nil {
		return dto.Fail[dto.LogByRequestData](err.Error())
	}
	// The route is public and request ids ride response headers: without this any
	// caller read any tenant's lane, tokens and charge for a known id.
	if l == nil || l.UserId != dto.UserID(c) || l.UserId == 0 {
		return dto.Ok(dto.LogByRequestData{})
	}
	return dto.Ok(dto.LogByRequestData{
		Channel:          l.ChannelName,
		Quota:            l.Quota,
		PromptTokens:     l.PromptTokens,
		CompletionTokens: l.CompletionTokens,
		UseTime:          l.UseTime,
		ModelName:        l.ModelName,
		Group:            l.Group,
	})
}

func GetLogByKey(c fuego.ContextNoBody) (*dto.Response[[]*model.Log], error) {
	tokenId := dto.TokenID(c)
	if tokenId == 0 {
		return dto.Fail[[]*model.Log]("Invalid token")
	}
	logs, err := model.GetLogByTokenId(tokenId)
	if err != nil {
		return dto.Fail[[]*model.Log](err.Error())
	}
	return dto.Ok(logs)
}

func GetLogsStat(c fuego.ContextWithParams[dto.LogStatParams]) (*dto.Response[dto.LogStatData], error) {
	p, _ := dto.ParseParams[dto.LogStatParams](c)
	stat, err := model.SumUsedQuota(p.Type, p.StartTimestamp, p.EndTimestamp, p.ModelName, p.Username, p.TokenName, p.Channel, p.Group)
	if err != nil {
		return dto.Fail[dto.LogStatData](err.Error())
	}
	return dto.Ok(dto.LogStatData{
		Quota: int64(stat.Quota),
		RPM:   stat.Rpm,
		TPM:   stat.Tpm,
	})
}

func GetLogsSelfStat(c fuego.ContextWithParams[dto.LogSelfStatParams]) (*dto.Response[dto.LogStatData], error) {
	username := dto.GinCtx(c).GetString("username")
	p, _ := dto.ParseParams[dto.LogSelfStatParams](c)
	if logSpanTooWide(p.StartTimestamp, p.EndTimestamp) {
		return dto.Fail[dto.LogStatData]("Time span cannot exceed 10 years")
	}
	startTimestamp := p.StartTimestamp
	if dto.UserRole(c) < common.RoleAdminUser {
		var err error
		if startTimestamp, err = limitLogTimeRange(p.StartTimestamp, p.EndTimestamp); err != nil {
			return dto.Fail[dto.LogStatData](err.Error())
		}
	}
	quotaNum, err := model.SumUsedQuota(p.Type, startTimestamp, p.EndTimestamp, p.ModelName, username, p.TokenName, p.Channel, p.Group)
	if err != nil {
		return dto.Fail[dto.LogStatData](err.Error())
	}
	return dto.Ok(dto.LogStatData{
		Quota: int64(quotaNum.Quota),
		RPM:   quotaNum.Rpm,
		TPM:   quotaNum.Tpm,
	})
}

func DeleteHistoryLogs(c fuego.ContextWithParams[dto.DeleteHistoryLogsParams]) (*dto.Response[int64], error) {
	p, _ := dto.ParseParams[dto.DeleteHistoryLogsParams](c)
	if p.TargetTimestamp == 0 {
		return dto.Fail[int64]("target timestamp is required")
	}
	ctx := c.Request().Context()
	const batchLimit = 100
	var count int64
	for {
		if ctx.Err() != nil {
			return dto.Fail[int64](ctx.Err().Error())
		}
		rowsAffected, err := model.DeleteOldLogBatch(ctx, p.TargetTimestamp, batchLimit)
		if err != nil {
			return dto.Fail[int64](err.Error())
		}
		count += rowsAffected
		if rowsAffected < int64(batchLimit) {
			break
		}
	}
	return dto.Ok(count)
}
