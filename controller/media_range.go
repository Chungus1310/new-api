package controller

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/pkg/aimark"
	"github.com/gin-gonic/gin"
	"golang.org/x/sync/singleflight"
)

const (
	markedMP4PlanTTL        = 10 * time.Minute
	markedMP4PlanFailureTTL = time.Minute
	markedMP4PlanMaxEntries = 4096
	markedMP4ProbeTimeout   = 20 * time.Second
	markedMP4ProbeWindow    = 64 << 10
	markedMP4ProbeReads     = 64
)

// mp4RangeFetcher sends one upstream GET with the given Range and, when set, If-Range.
type mp4RangeFetcher func(ctx context.Context, rangeHeader, ifRange string) (*http.Response, error)

// markedMP4Plan describes the marked file a Range request is answered against: the
// upstream bytes with the XMP box inserted at insertAt when mark is set.
type markedMP4Plan struct {
	size        int64
	insertAt    int64
	mark        bool
	contentType string
	etag        string
	expires     time.Time
}

var (
	markedMP4PlansMu sync.Mutex
	markedMP4Plans   = make(map[string]markedMP4Plan)
	markedMP4Probes  singleflight.Group
)

func loadMarkedMP4Plan(ctx context.Context, key string, fetch mp4RangeFetcher) markedMP4Plan {
	now := time.Now()
	markedMP4PlansMu.Lock()
	plan, ok := markedMP4Plans[key]
	markedMP4PlansMu.Unlock()
	if ok && now.Before(plan.expires) {
		return plan
	}
	value, _, _ := markedMP4Probes.Do(key, func() (any, error) {
		probeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), markedMP4ProbeTimeout)
		defer cancel()
		plan, err := probeMarkedMP4(probeCtx, fetch)
		plan.expires = time.Now().Add(markedMP4PlanTTL)
		if err != nil {
			logger.LogWarn(ctx, common.LogText("mp4 range probe failed: %s", common.MaskSensitiveInfo(err.Error())))
			plan = markedMP4Plan{expires: time.Now().Add(markedMP4PlanFailureTTL)}
		}
		storeMarkedMP4Plan(key, plan)
		return plan, nil
	})
	return value.(markedMP4Plan)
}

func storeMarkedMP4Plan(key string, plan markedMP4Plan) {
	markedMP4PlansMu.Lock()
	defer markedMP4PlansMu.Unlock()
	if len(markedMP4Plans) >= markedMP4PlanMaxEntries {
		now := time.Now()
		for k, p := range markedMP4Plans {
			if now.After(p.expires) {
				delete(markedMP4Plans, k)
			}
		}
		for k := range markedMP4Plans {
			if len(markedMP4Plans) < markedMP4PlanMaxEntries {
				break
			}
			delete(markedMP4Plans, k)
		}
	}
	markedMP4Plans[key] = plan
}

func forgetMarkedMP4Plan(key string) {
	markedMP4PlansMu.Lock()
	delete(markedMP4Plans, key)
	markedMP4PlansMu.Unlock()
}

// probeMarkedMP4 learns the upstream size from a Range response and walks the
// top-level box headers with small Range reads. A plan without mark means the
// caller keeps its unmarked behaviour.
func probeMarkedMP4(ctx context.Context, fetch mp4RangeFetcher) (markedMP4Plan, error) {
	resp, err := fetch(ctx, fmt.Sprintf("bytes=0-%d", markedMP4ProbeWindow-1), "")
	if err != nil {
		return markedMP4Plan{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent {
		return markedMP4Plan{}, nil
	}
	start, _, size, ok := parseContentRange(resp.Header.Get("Content-Range"))
	if !ok || start != 0 {
		return markedMP4Plan{}, nil
	}
	window, err := io.ReadAll(io.LimitReader(resp.Body, markedMP4ProbeWindow))
	if err != nil {
		return markedMP4Plan{}, err
	}
	if !aimark.IsMP4(window) {
		return markedMP4Plan{}, nil
	}
	etag := strings.TrimSpace(resp.Header.Get("ETag"))
	if strings.HasPrefix(etag, "W/") {
		etag = ""
	}
	readAt := func(off int64, n int) ([]byte, error) {
		if off < int64(len(window)) {
			return window[off:min(off+int64(n), int64(len(window)))], nil
		}
		part, err := fetch(ctx, fmt.Sprintf("bytes=%d-%d", off, off+int64(n)-1), etag)
		if err != nil {
			return nil, err
		}
		defer part.Body.Close()
		partStart, _, partSize, ok := parseContentRange(part.Header.Get("Content-Range"))
		if part.StatusCode != http.StatusPartialContent || !ok || partStart != off || partSize != size {
			return nil, errors.New("upstream range response does not match the probe")
		}
		return io.ReadAll(io.LimitReader(part.Body, int64(n)))
	}
	insertAt, mark, err := aimark.ScanMP4(size, markedMP4ProbeReads, readAt)
	if err != nil {
		return markedMP4Plan{}, err
	}
	contentType := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(contentType, "video/") {
		contentType = "video/mp4"
	}
	return markedMP4Plan{size: size, insertAt: insertAt, mark: mark, contentType: contentType, etag: etag}, nil
}

// parseContentRange reads "bytes start-end/size" with a known size.
func parseContentRange(value string) (start, end, size int64, ok bool) {
	spec, found := strings.CutPrefix(strings.TrimSpace(value), "bytes ")
	if !found {
		return 0, 0, 0, false
	}
	span, total, found := strings.Cut(spec, "/")
	if !found {
		return 0, 0, 0, false
	}
	first, last, found := strings.Cut(span, "-")
	if !found {
		return 0, 0, 0, false
	}
	var err1, err2, err3 error
	start, err1 = strconv.ParseInt(first, 10, 64)
	end, err2 = strconv.ParseInt(last, 10, 64)
	size, err3 = strconv.ParseInt(total, 10, 64)
	if err1 != nil || err2 != nil || err3 != nil || start < 0 || end < start || size <= end {
		return 0, 0, 0, false
	}
	return start, end, size, true
}

// parseByteRange resolves a single byte range against size. valid is false for a
// header to ignore (malformed or multiple ranges); satisfiable is false for a 416.
func parseByteRange(value string, size int64) (start, end int64, valid, satisfiable bool) {
	spec, found := strings.CutPrefix(strings.TrimSpace(value), "bytes=")
	if !found || strings.Contains(spec, ",") {
		return 0, 0, false, false
	}
	first, last, found := strings.Cut(strings.TrimSpace(spec), "-")
	if !found {
		return 0, 0, false, false
	}
	if first == "" {
		suffix, err := strconv.ParseInt(last, 10, 64)
		if err != nil || suffix < 0 {
			return 0, 0, false, false
		}
		if suffix == 0 {
			return 0, 0, true, false
		}
		return max(size-suffix, 0), size - 1, true, true
	}
	start, err := strconv.ParseInt(first, 10, 64)
	if err != nil || start < 0 {
		return 0, 0, false, false
	}
	end = size - 1
	if last != "" {
		requested, err := strconv.ParseInt(last, 10, 64)
		if err != nil || requested < start {
			return 0, 0, false, false
		}
		end = min(requested, size-1)
	}
	if start >= size {
		return 0, 0, true, false
	}
	return start, end, true, true
}

// serveMarkedMP4Range answers a Range request for an MP4 against the marked file,
// fetching only the requested upstream span and producing the box bytes itself.
// It returns false, having written nothing, whenever the caller should fall back.
func serveMarkedMP4Range(c *gin.Context, key string, fetch mp4RangeFetcher) bool {
	request := c.Request
	rangeHeader := request.Header.Get("Range")
	if rangeHeader == "" || request.Method != http.MethodGet && request.Method != http.MethodHead {
		return false
	}
	plan := loadMarkedMP4Plan(request.Context(), key, fetch)
	if !plan.mark {
		return false
	}
	box := aimark.MP4Box()
	total := plan.size + int64(len(box))
	header := c.Writer.Header()

	status := http.StatusPartialContent
	start, end, valid, satisfiable := parseByteRange(rangeHeader, total)
	if !valid || request.Header.Get("If-Range") != "" {
		status, start, end = http.StatusOK, 0, total-1
	} else if !satisfiable {
		setTaskMediaResponseSecurityHeaders(header)
		header.Set("Content-Range", "bytes */"+strconv.FormatInt(total, 10))
		c.Status(http.StatusRequestedRangeNotSatisfiable)
		c.Writer.WriteHeaderNow()
		return true
	}

	// The marked file is upstream[:at], the box, then upstream[at:], so the
	// requested span maps to one contiguous upstream span around the box.
	at, boxEnd := plan.insertAt, plan.insertAt+int64(len(box))
	upstreamStart, upstreamEnd := start, end
	if start >= at {
		upstreamStart = max(start-int64(len(box)), at)
	}
	if end >= boxEnd {
		upstreamEnd = end - int64(len(box))
	} else if end >= at {
		upstreamEnd = at - 1
	}

	var upstream io.Reader
	if upstreamStart <= upstreamEnd && request.Method == http.MethodGet {
		resp, err := fetch(request.Context(), fmt.Sprintf("bytes=%d-%d", upstreamStart, upstreamEnd), plan.etag)
		if err != nil {
			forgetMarkedMP4Plan(key)
			return false
		}
		defer resp.Body.Close()
		partStart, partEnd, partSize, ok := parseContentRange(resp.Header.Get("Content-Range"))
		if resp.StatusCode != http.StatusPartialContent || !ok || partStart != upstreamStart || partEnd != upstreamEnd || partSize != plan.size {
			forgetMarkedMP4Plan(key)
			return false
		}
		upstream = resp.Body
	}

	setTaskMediaResponseSecurityHeaders(header)
	header.Set("Content-Type", plan.contentType)
	header.Set("Accept-Ranges", "bytes")
	header.Set("Content-Length", strconv.FormatInt(end-start+1, 10))
	if status == http.StatusPartialContent {
		header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, total))
	}
	c.Status(status)
	c.Writer.WriteHeaderNow()
	if request.Method == http.MethodHead {
		return true
	}
	if upstream != nil && upstreamStart < at {
		if _, err := io.CopyN(c.Writer, upstream, min(upstreamEnd, at-1)-upstreamStart+1); err != nil {
			logger.LogError(request.Context(), common.LogText("Failed to stream marked video range: %v", err))
			return true
		}
	}
	if start < boxEnd && end >= at {
		if _, err := c.Writer.Write(box[max(start, at)-at : min(end, boxEnd-1)-at+1]); err != nil {
			return true
		}
	}
	if upstream != nil && upstreamEnd >= at {
		if _, err := io.CopyN(c.Writer, upstream, upstreamEnd-max(upstreamStart, at)+1); err != nil {
			logger.LogError(request.Context(), common.LogText("Failed to stream marked video range: %v", err))
		}
	}
	return true
}
