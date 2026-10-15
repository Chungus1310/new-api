package service

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/pkg/aimark"
	"github.com/QuantumNous/new-api/setting/system_setting"

	"github.com/gin-gonic/gin"
)

const (
	MediaLinkTTL          = 7 * 24 * time.Hour
	mediaLinkAccessDomain = "media-v1\x00"
	maxMediaLinkURLLength = 8 << 10
)

var (
	ErrMediaLinkInvalid = errors.New("media link is invalid")
	ErrMediaLinkExpired = errors.New("media link has expired")
)

func init() {
	aimark.LinkRewriter = BuildMediaLinkURL
}

// wrapMediaLinks is false for the unorouter BFF: it downloads every generated image
// itself, marks it, and refuses to fetch from the gateway host.
func wrapMediaLinks(c *gin.Context) bool {
	return c == nil || !common.GetContextKeyBool(c, constant.ContextKeyBFFRequest)
}

// MarkGeneratedMedia marks the generated media in a response body or stream chunk.
func MarkGeneratedMedia(c *gin.Context, body []byte) []byte {
	return aimark.RewriteBody(body, wrapMediaLinks(c))
}

// MarkGeneratedMediaString is MarkGeneratedMedia for string payloads.
func MarkGeneratedMediaString(c *gin.Context, s string) string {
	return aimark.RewriteString(s, wrapMediaLinks(c))
}

const streamCarryKey = "aimark_stream_carry"

// MarkGeneratedStreamEvent marks one stream event payload. Image links that
// arrive split over several text deltas are held back until complete, so the
// result may start with earlier held text released as events of their own; the
// last element is always this event.
func MarkGeneratedStreamEvent(c *gin.Context, data []byte) []aimark.StreamEvent {
	if c == nil {
		return []aimark.StreamEvent{{Data: MarkGeneratedMedia(c, data)}}
	}
	value, _ := c.Get(streamCarryKey)
	carry, _ := value.(*aimark.StreamCarry)
	if carry == nil {
		carry = &aimark.StreamCarry{}
		c.Set(streamCarryKey, carry)
	}
	events := carry.Process(data)
	for i := range events {
		events[i].Data = MarkGeneratedMedia(c, events[i].Data)
	}
	return events
}

// FlushGeneratedStreamEvents releases the text MarkGeneratedStreamEvent still
// holds, for the end of a stream.
func FlushGeneratedStreamEvents(c *gin.Context) []aimark.StreamEvent {
	if c == nil {
		return nil
	}
	value, _ := c.Get(streamCarryKey)
	carry, _ := value.(*aimark.StreamCarry)
	if carry == nil {
		return nil
	}
	events := carry.Flush()
	for i := range events {
		events[i].Data = MarkGeneratedMedia(c, events[i].Data)
	}
	return events
}

// MarkGeneratedMediaLink wraps one image link in a signed media link.
func MarkGeneratedMediaLink(c *gin.Context, rawURL string) string {
	if !wrapMediaLinks(c) {
		return rawURL
	}
	return aimark.RewriteLink(rawURL)
}

func signMediaLink(payload []byte) []byte {
	mac := hmac.New(sha256.New, []byte(common.CryptoSecret))
	_, _ = mac.Write([]byte(mediaLinkAccessDomain))
	_, _ = mac.Write(payload)
	return mac.Sum(nil)
}

// mediaLinkBase returns the public gateway address media links are served from, or
// nil when none is configured beyond the localhost default.
func mediaLinkBase() *url.URL {
	address := strings.TrimSpace(system_setting.TaskPublicAddress)
	if address == "" {
		address = strings.TrimSpace(system_setting.ServerAddress)
	}
	if ValidateTaskArtifactBaseURL(address) != nil {
		return nil
	}
	base, err := url.Parse(address)
	if err != nil {
		return nil
	}
	host := base.Hostname()
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && (ip.IsLoopback() || ip.IsUnspecified()) {
		return nil
	}
	return base
}

// BuildMediaLinkURL wraps an upstream image link in a signed gateway URL that serves
// the image with the AI-generated mark. Links that cannot or need not be wrapped
// return false.
func BuildMediaLinkURL(rawURL string) (string, bool) {
	if len(rawURL) > maxMediaLinkURLLength || common.CryptoSecret == "" {
		return "", false
	}
	target, err := url.Parse(rawURL)
	if err != nil || (target.Scheme != "http" && target.Scheme != "https") || target.Host == "" || target.User != nil {
		return "", false
	}
	base := mediaLinkBase()
	if base == nil || strings.EqualFold(target.Hostname(), base.Hostname()) {
		return "", false
	}
	if server, err := url.Parse(strings.TrimSpace(system_setting.ServerAddress)); err == nil &&
		strings.EqualFold(target.Hostname(), server.Hostname()) {
		return "", false
	}

	payload := binary.BigEndian.AppendUint64(nil, uint64(time.Now().Add(MediaLinkTTL).Unix()))
	payload = append(payload, rawURL...)
	token := base64.RawURLEncoding.EncodeToString(payload) + "." +
		base64.RawURLEncoding.EncodeToString(signMediaLink(payload))
	return strings.TrimRight(base.String(), "/") + "/v1/media/" + token, true
}

// VerifyMediaLink returns the upstream URL a media link token was issued for.
func VerifyMediaLink(token string) (string, error) {
	encodedPayload, encodedSignature, found := strings.Cut(token, ".")
	if !found || common.CryptoSecret == "" || len(token) > 2*maxMediaLinkURLLength {
		return "", ErrMediaLinkInvalid
	}
	payload, err := base64.RawURLEncoding.Strict().DecodeString(encodedPayload)
	if err != nil || len(payload) <= 8 {
		return "", ErrMediaLinkInvalid
	}
	signature, err := base64.RawURLEncoding.Strict().DecodeString(encodedSignature)
	if err != nil || !hmac.Equal(signature, signMediaLink(payload)) {
		return "", ErrMediaLinkInvalid
	}
	if time.Now().Unix() > int64(binary.BigEndian.Uint64(payload[:8])) {
		return "", ErrMediaLinkExpired
	}
	return string(payload[8:]), nil
}
