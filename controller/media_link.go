package controller

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/pkg/aimark"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

const (
	markedMediaImageMaxBytes   = 32 << 20
	mediaLinkHeaderTimeout     = 30 * time.Second
	mediaLinkImageFetchTimeout = 60 * time.Second
)

// copyMarkedMP4 streams an MP4 or MOV body and appends the XMP box once the body
// ended cleanly on a box boundary.
func copyMarkedMP4(w io.Writer, body io.Reader) error {
	var tracker aimark.MP4Tracker
	if _, err := io.Copy(w, io.TeeReader(body, &tracker)); err != nil {
		return err
	}
	if !tracker.ShouldAppend() {
		return nil
	}
	_, err := w.Write(aimark.MP4Box())
	return err
}

func mediaLinkRangeFetcher(rawURL string) mp4RangeFetcher {
	return func(ctx context.Context, rangeHeader, ifRange string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Range", rangeHeader)
		if ifRange != "" {
			req.Header.Set("If-Range", ifRange)
		}
		client := *service.GetSSRFProtectedHTTPClient()
		client.Timeout = 0
		return doTaskMediaRequest(&client, req, mediaLinkHeaderTimeout)
	}
}

// MediaLinkContent serves an upstream image or MP4 video behind a signed media link
// with the AI-generated mark. Whenever that is not possible it redirects to the upstream URL.
func MediaLinkContent(c *gin.Context) {
	rawURL, err := service.VerifyMediaLink(c.Param("token"))
	if err != nil {
		c.Header("Cache-Control", "private, no-store")
		status := http.StatusNotFound
		if errors.Is(err, service.ErrMediaLinkExpired) {
			status = http.StatusGone
		}
		c.JSON(status, gin.H{"error": gin.H{"message": err.Error(), "type": "invalid_request_error"}})
		return
	}
	fallback := func() {
		c.Header("Cache-Control", "private, no-store")
		c.Redirect(http.StatusFound, rawURL)
	}
	if service.ValidateSSRFProtectedFetchURL(rawURL) != nil {
		fallback()
		return
	}
	if c.Request.Header.Get("Range") != "" {
		if serveMarkedMP4Range(c, "media\x00"+rawURL, mediaLinkRangeFetcher(rawURL)) {
			return
		}
		if !isOpenRangeFromStart(c.Request) {
			fallback()
			return
		}
	}

	ctx, cancel := context.WithCancel(c.Request.Context())
	defer cancel()
	imageDeadline := time.AfterFunc(mediaLinkImageFetchTimeout, cancel)
	defer imageDeadline.Stop()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		fallback()
		return
	}
	client := *service.GetSSRFProtectedHTTPClient()
	client.Timeout = 0
	resp, err := doTaskMediaRequest(&client, req, mediaLinkHeaderTimeout)
	if err != nil {
		logger.LogWarn(c.Request.Context(), common.LogText("media link fetch failed: %s", common.MaskSensitiveInfo(err.Error())))
		fallback()
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fallback()
		return
	}

	body := bufio.NewReader(resp.Body)
	head, _ := body.Peek(16)
	header := c.Writer.Header()
	switch {
	case aimark.IsImage(head):
		if resp.ContentLength > markedMediaImageMaxBytes {
			fallback()
			return
		}
		data, err := io.ReadAll(io.LimitReader(body, markedMediaImageMaxBytes+1))
		if err != nil || len(data) > markedMediaImageMaxBytes {
			fallback()
			return
		}
		data, _ = aimark.Mark(data)
		setTaskMediaResponseSecurityHeaders(header)
		header.Set("Cache-Control", "private")
		header.Set("Content-Length", strconv.Itoa(len(data)))
		c.Data(http.StatusOK, http.DetectContentType(data), data)
	case aimark.IsMP4(head):
		imageDeadline.Stop()
		contentType := resp.Header.Get("Content-Type")
		if !strings.HasPrefix(contentType, "video/") {
			contentType = "video/mp4"
		}
		setTaskMediaResponseSecurityHeaders(header)
		header.Set("Cache-Control", "private")
		header.Set("Content-Type", contentType)
		c.Status(http.StatusOK)
		c.Writer.WriteHeaderNow()
		if c.Request.Method == http.MethodHead {
			return
		}
		if err := copyMarkedMP4(c.Writer, body); err != nil {
			logger.LogError(c.Request.Context(), common.LogText("Failed to stream media link: %v", err))
		}
	default:
		fallback()
	}
}
