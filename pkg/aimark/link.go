package aimark

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

// LinkRewriter, when set, returns a URL that serves the image behind an http(s) link
// with the mark added, or false to leave the link as it is.
var LinkRewriter func(rawURL string) (string, bool)

var (
	linkTriggers = [][]byte{
		[]byte(`"image_url"`), []byte(`"url"`), []byte("](http"), []byte(`"fileUri"`), []byte(`"file_uri"`),
		[]byte(`"image_generation_call"`), []byte("http"),
	}
	// Responses image_generation_call items whose result is a link instead of base64.
	imageCallPattern       = regexp.MustCompile(`\{[^{}]*"type"\s*:\s*"image_generation_call"[^{}]*\}`)
	imageCallResultPattern = regexp.MustCompile(`"result"\s*:\s*("(?:[^"\\]|\\.)*")`)
	// Text values of chat, Claude, Gemini and Responses payloads, where a model
	// writes links as plain text.
	textFieldPattern = regexp.MustCompile(`"(?:content|text|delta|output_text)"\s*:\s*"((?:[^"\\]|\\.)*)"`)
	bareURLPattern   = regexp.MustCompile(`https?:(?:\\/|/)(?:\\/|/)(?:[^\s"\\<>()\[\]{}'\x60]|\\/|\\u[0-9a-fA-F]{4})+`)
	// Chat completion image parts, as an object or as a bare string.
	imageURLPattern = regexp.MustCompile(`"image_url"\s*:\s*(?:\{\s*"url"\s*:\s*)?("(?:[^"\\]|\\.)*")`)
	// Claude image blocks with a url source.
	claudeImageURLPattern = regexp.MustCompile(`"type"\s*:\s*"image"\s*,\s*"source"\s*:\s*\{[^{}]*?"url"\s*:\s*("(?:[^"\\]|\\.)*")`)
	// Gemini fileData parts; the mime type decides whether the file is an image.
	fileDataPattern      = regexp.MustCompile(`"(?:fileData|file_data)"\s*:\s*\{[^{}]*\}`)
	fileURIPattern       = regexp.MustCompile(`"(?:fileUri|file_uri)"\s*:\s*("(?:[^"\\]|\\.)*")`)
	imageMimeTypePattern = regexp.MustCompile(`"(?:mimeType|mime_type)"\s*:\s*"image/`)
	// Markdown images inside a JSON string, where / and & may arrive escaped.
	markdownImagePattern = regexp.MustCompile(`!\[[^\]"\n]*\]\((https?:(?:[^)\s"\\]|\\/|\\u[0-9a-fA-F]{4})+)\)`)
	dataArrayPattern     = regexp.MustCompile(`"data"\s*:\s*\[`)
	// Keys an images API data item carries; any other key means the array is not images.
	imageItemKeys = []string{
		"url", "b64_json", "revised_prompt", "index", "seed", "width", "height",
		"mime_type", "content_type", "size", "format", "output_format",
	}
)

// RewriteLink returns the LinkRewriter URL for one http(s) image link.
func RewriteLink(rawURL string) string {
	if LinkRewriter == nil || !isHTTPURL(rawURL) {
		return rawURL
	}
	if rewritten, ok := LinkRewriter(rawURL); ok {
		mediaSources.learn(rawURL)
		return rewritten
	}
	return rawURL
}

const (
	mediaSourceTTL        = 7 * 24 * time.Hour
	maxMediaSourceHosts   = 4096
	maxMediaSourceLinks   = 16384
	maxBareLinkCandidates = 64
)

// Files a bare link may name to count as generated media of a known host.
var bareMediaExtensions = []string{".png", ".jpg", ".jpeg", ".webp", ".mp4", ".mov"}

// mediaSourceSet remembers the hosts and links the gateway wrapped as generated
// media in structured output (Markdown images, image parts, data[].url, task
// results). A bare link in text is wrapped only when it names one of those links,
// or a media file on one of those hosts; any other link stays as it is.
type mediaSourceSet struct {
	mu    sync.Mutex
	hosts map[string]time.Time
	links map[[sha256.Size]byte]time.Time
}

var mediaSources = &mediaSourceSet{}

func (m *mediaSourceSet) learn(rawURL string) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Hostname() == "" {
		return
	}
	expires := time.Now().Add(mediaSourceTTL)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.hosts == nil {
		m.hosts = make(map[string]time.Time)
		m.links = make(map[[sha256.Size]byte]time.Time)
	}
	putExpiring(m.hosts, strings.ToLower(parsed.Hostname()), expires, maxMediaSourceHosts)
	putExpiring(m.links, sha256.Sum256([]byte(rawURL)), expires, maxMediaSourceLinks)
}

func putExpiring[K comparable](entries map[K]time.Time, key K, expires time.Time, limit int) {
	if _, ok := entries[key]; !ok && len(entries) >= limit {
		now := time.Now()
		for k, at := range entries {
			if now.After(at) {
				delete(entries, k)
			}
		}
		for k := range entries {
			if len(entries) < limit {
				break
			}
			delete(entries, k)
		}
	}
	entries[key] = expires
}

func (m *mediaSourceSet) empty() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.hosts) == 0
}

func (m *mediaSourceSet) allows(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Hostname() == "" {
		return false
	}
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	if at, ok := m.links[sha256.Sum256([]byte(rawURL))]; ok && now.Before(at) {
		return true
	}
	at, ok := m.hosts[strings.ToLower(parsed.Hostname())]
	if !ok || now.After(at) {
		return false
	}
	return slices.Contains(bareMediaExtensions, strings.ToLower(path.Ext(parsed.Path)))
}

// RewriteLinksIn swaps every JSON string in body that equals one of rawURLs for its
// LinkRewriter URL, matching the string however an encoder escaped it.
func RewriteLinksIn(body []byte, rawURLs []string) []byte {
	for _, rawURL := range rawURLs {
		rewritten := RewriteLink(rawURL)
		if rewritten == rawURL {
			continue
		}
		replacement, err := json.Marshal(rewritten)
		if err != nil {
			continue
		}
		for _, quoted := range quotedJSONForms(rawURL) {
			body = bytes.ReplaceAll(body, quoted, replacement)
		}
	}
	return body
}

// Key fragments under which a task's raw data echoes what the caller sent.
var taskRequestKeyParts = []string{"input", "request", "param", "callback", "reference", "mask", "init", "prompt", "source"}

// TaskMediaLinks lists the http(s) links in a task's raw upstream data that name a
// media file the gateway can mark, such as further outputs, covers or last frames
// next to the result URL. Values under request-side keys stay out, since a
// provider may echo the caller's own input images there.
func TaskMediaLinks(data []byte) []string {
	if !bytes.Contains(data, []byte("http")) {
		return nil
	}
	leaves, ok := jsonLeaves(data)
	if !ok {
		return nil
	}
	var links []string
	for _, leaf := range leaves {
		raw, isString := leaf.value.(string)
		if !isString || !isHTTPURL(raw) || slices.Contains(links, raw) {
			continue
		}
		parsed, err := url.Parse(raw)
		if err != nil || !slices.Contains(bareMediaExtensions, strings.ToLower(path.Ext(parsed.Path))) {
			continue
		}
		requestSide := slices.ContainsFunc(leaf.path, func(key string) bool {
			key = strings.ToLower(key)
			return slices.ContainsFunc(taskRequestKeyParts, func(part string) bool { return strings.Contains(key, part) })
		})
		if !requestSide {
			links = append(links, raw)
		}
	}
	return links
}

func quotedJSONForms(s string) [][]byte {
	var forms [][]byte
	for _, escapeHTML := range []bool{true, false} {
		var buf bytes.Buffer
		encoder := json.NewEncoder(&buf)
		encoder.SetEscapeHTML(escapeHTML)
		if encoder.Encode(s) != nil {
			continue
		}
		quoted := bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
		for _, form := range [][]byte{quoted, bytes.ReplaceAll(quoted, []byte("/"), []byte(`\/`))} {
			if !slices.ContainsFunc(forms, func(f []byte) bool { return bytes.Equal(f, form) }) {
				forms = append(forms, form)
			}
		}
	}
	return forms
}

func isHTTPURL(s string) bool {
	return strings.HasPrefix(s, "https://") || strings.HasPrefix(s, "http://")
}

// KnownLink reports a link the request already contained. The model only copied
// it (a character card sprite, an image the user sent), so it is not generated
// output and stays as it is.
type KnownLink func(rawURL string) bool

// rewriteJSONString swaps a quoted JSON string holding an image link for the
// rewritten link, also quoted.
func rewriteJSONString(quoted []byte, known KnownLink) ([]byte, bool) {
	var raw string
	if json.Unmarshal(quoted, &raw) != nil || !isHTTPURL(raw) || (known != nil && known(raw)) {
		return nil, false
	}
	rewritten := RewriteLink(raw)
	if rewritten == raw {
		return nil, false
	}
	encoded, err := json.Marshal(rewritten)
	return encoded, err == nil
}

// rewriteQuotedLink rewrites the quoted link captured by pattern's first group,
// keeping whatever follows it in the match.
func rewriteQuotedLink(pattern *regexp.Regexp, m []byte, known KnownLink) []byte {
	sub := pattern.FindSubmatchIndex(m)
	encoded, ok := rewriteJSONString(m[sub[2]:sub[3]], known)
	if !ok {
		return m
	}
	out := append([]byte(nil), m[:sub[2]]...)
	out = append(out, encoded...)
	return append(out, m[sub[3]:]...)
}

func rewriteLinks(body []byte, known KnownLink) []byte {
	if bytes.Contains(body, []byte(`"image_url"`)) {
		body = imageURLPattern.ReplaceAllFunc(body, func(m []byte) []byte {
			return rewriteQuotedLink(imageURLPattern, m, known)
		})
	}
	if bytes.Contains(body, []byte(`"source"`)) && bytes.Contains(body, []byte(`"url"`)) {
		body = claudeImageURLPattern.ReplaceAllFunc(body, func(m []byte) []byte {
			return rewriteQuotedLink(claudeImageURLPattern, m, known)
		})
	}
	if bytes.Contains(body, []byte(`"fileUri"`)) || bytes.Contains(body, []byte(`"file_uri"`)) {
		body = fileDataPattern.ReplaceAllFunc(body, func(m []byte) []byte {
			loc := fileURIPattern.FindSubmatchIndex(m)
			if loc == nil || !imageMimeTypePattern.Match(m) || isGeminiFileHandle(m[loc[2]:loc[3]]) {
				return m
			}
			out := append([]byte(nil), m[:loc[0]]...)
			out = append(out, rewriteQuotedLink(fileURIPattern, m[loc[0]:loc[1]], known)...)
			return append(out, m[loc[1]:]...)
		})
	}
	if bytes.Contains(body, []byte("](http")) {
		body = markdownImagePattern.ReplaceAllFunc(body, func(m []byte) []byte {
			sub := markdownImagePattern.FindSubmatchIndex(m)
			quoted := append(append([]byte{'"'}, m[sub[2]:sub[3]]...), '"')
			encoded, ok := rewriteJSONString(quoted, known)
			if !ok {
				return m
			}
			out := append([]byte(nil), m[:sub[2]]...)
			out = append(out, encoded[1:len(encoded)-1]...)
			return append(out, ')')
		})
	}
	if bytes.Contains(body, []byte(`"url"`)) && dataArrayPattern.Match(body) {
		body = rewriteImageItemURLs(body, known)
	}
	if bytes.Contains(body, []byte(`"image_generation_call"`)) {
		body = imageCallPattern.ReplaceAllFunc(body, func(m []byte) []byte {
			loc := imageCallResultPattern.FindSubmatchIndex(m)
			if loc == nil {
				return m
			}
			out := append([]byte(nil), m[:loc[0]]...)
			out = append(out, rewriteQuotedLink(imageCallResultPattern, m[loc[0]:loc[1]], known)...)
			return append(out, m[loc[1]:]...)
		})
	}
	if bytes.Contains(body, []byte("http")) && !mediaSources.empty() {
		body = rewriteBareLinks(body, known)
	}
	return body
}

// rewriteBareLinks wraps plain-text links in text values that mediaSources allows.
// It runs last, so links an earlier pattern wrapped already point at the gateway
// and are left alone.
func rewriteBareLinks(body []byte, known KnownLink) []byte {
	return textFieldPattern.ReplaceAllFunc(body, func(field []byte) []byte {
		sub := textFieldPattern.FindSubmatchIndex(field)
		value := field[sub[2]:sub[3]]
		if !bytes.Contains(value, []byte("http")) {
			return field
		}
		candidates := 0
		rewritten := bareURLPattern.ReplaceAllFunc(value, func(m []byte) []byte {
			candidates++
			if candidates > maxBareLinkCandidates {
				return m
			}
			link := bytes.TrimRight(m, ".,;:!?")
			quoted := append(append([]byte{'"'}, link...), '"')
			var raw string
			if json.Unmarshal(quoted, &raw) != nil || !mediaSources.allows(raw) {
				return m
			}
			encoded, ok := rewriteJSONString(quoted, known)
			if !ok {
				return m
			}
			out := append([]byte(nil), encoded[1:len(encoded)-1]...)
			return append(out, m[len(link):]...)
		})
		if bytes.Equal(rewritten, value) {
			return field
		}
		out := append([]byte(nil), field[:sub[2]]...)
		out = append(out, rewritten...)
		return append(out, field[sub[3]:]...)
	})
}

// isGeminiFileHandle reports a Files API URI, a handle clients pass back to Gemini
// rather than a fetchable image.
func isGeminiFileHandle(quoted []byte) bool {
	var raw string
	if json.Unmarshal(quoted, &raw) != nil {
		return false
	}
	parsed, err := url.Parse(raw)
	return err == nil && strings.EqualFold(parsed.Hostname(), "generativelanguage.googleapis.com")
}

type byteSpan struct{ start, end int }

// rewriteImageItemURLs rewrites data[].url of an images API response. It walks the
// tokens instead of matching text so that url fields of other shapes (search results,
// citations) stay untouched.
func rewriteImageItemURLs(body []byte, known KnownLink) []byte {
	dec := json.NewDecoder(bytes.NewReader(body))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return body
	}
	var spans []byteSpan
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return body
		}
		if key != "data" {
			if skipJSONValue(dec) != nil {
				return body
			}
			continue
		}
		if tok, err := dec.Token(); err != nil || tok != json.Delim('[') {
			return body
		}
		for dec.More() {
			span, err := imageItemURLSpan(dec, body)
			if err != nil {
				return body
			}
			if span.end > 0 {
				spans = append(spans, span)
			}
		}
		if _, err := dec.Token(); err != nil {
			return body
		}
	}
	if len(spans) == 0 {
		return body
	}
	var out bytes.Buffer
	last := 0
	for _, span := range spans {
		encoded, ok := rewriteJSONString(body[span.start:span.end], known)
		if !ok {
			continue
		}
		out.Write(body[last:span.start])
		out.Write(encoded)
		last = span.end
	}
	if last == 0 {
		return body
	}
	out.Write(body[last:])
	return out.Bytes()
}

// imageItemURLSpan reads one data array element and returns the byte span of its
// quoted url value, or a zero span when the element is not an image item.
func imageItemURLSpan(dec *json.Decoder, body []byte) (byteSpan, error) {
	tok, err := dec.Token()
	if err != nil {
		return byteSpan{}, err
	}
	if tok == json.Delim('[') {
		return byteSpan{}, skipJSONRest(dec, 1)
	}
	if tok != json.Delim('{') {
		return byteSpan{}, nil
	}
	var span byteSpan
	imageItem := true
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return byteSpan{}, err
		}
		key, _ := keyTok.(string)
		if key != "url" {
			if !slices.Contains(imageItemKeys, key) {
				imageItem = false
			}
			if err := skipJSONValue(dec); err != nil {
				return byteSpan{}, err
			}
			continue
		}
		before := int(dec.InputOffset())
		value, err := dec.Token()
		if err != nil {
			return byteSpan{}, err
		}
		if _, isDelim := value.(json.Delim); isDelim {
			if err := skipJSONRest(dec, 1); err != nil {
				return byteSpan{}, err
			}
			imageItem = false
			continue
		}
		if _, isString := value.(string); isString {
			end := int(dec.InputOffset())
			if quote := bytes.IndexByte(body[before:end], '"'); quote >= 0 {
				span = byteSpan{start: before + quote, end: end}
			}
		}
	}
	if _, err := dec.Token(); err != nil {
		return byteSpan{}, err
	}
	if !imageItem {
		return byteSpan{}, nil
	}
	return span, nil
}

func skipJSONValue(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if delim, ok := tok.(json.Delim); ok && (delim == '{' || delim == '[') {
		return skipJSONRest(dec, 1)
	}
	return nil
}

func skipJSONRest(dec *json.Decoder, depth int) error {
	for depth > 0 {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		switch tok {
		case json.Delim('{'), json.Delim('['):
			depth++
		case json.Delim('}'), json.Delim(']'):
			depth--
		}
	}
	return nil
}
