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
	"github.com/QuantumNous/new-api/pkg/aimark"
	"github.com/QuantumNous/new-api/setting/system_setting"
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
