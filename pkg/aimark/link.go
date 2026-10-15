package aimark

import (
	"bytes"
	"encoding/json"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

// LinkRewriter, when set, returns a URL that serves the image behind an http(s) link
// with the mark added, or false to leave the link as it is.
var LinkRewriter func(rawURL string) (string, bool)

var (
	linkTriggers = [][]byte{
		[]byte(`"image_url"`), []byte(`"url"`), []byte("](http"), []byte(`"fileUri"`), []byte(`"file_uri"`),
	}
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
		return rewritten
	}
	return rawURL
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

// rewriteJSONString swaps a quoted JSON string holding an image link for the
// rewritten link, also quoted.
func rewriteJSONString(quoted []byte) ([]byte, bool) {
	var raw string
	if json.Unmarshal(quoted, &raw) != nil || !isHTTPURL(raw) {
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
func rewriteQuotedLink(pattern *regexp.Regexp, m []byte) []byte {
	sub := pattern.FindSubmatchIndex(m)
	encoded, ok := rewriteJSONString(m[sub[2]:sub[3]])
	if !ok {
		return m
	}
	out := append([]byte(nil), m[:sub[2]]...)
	out = append(out, encoded...)
	return append(out, m[sub[3]:]...)
}

func rewriteLinks(body []byte) []byte {
	if bytes.Contains(body, []byte(`"image_url"`)) {
		body = imageURLPattern.ReplaceAllFunc(body, func(m []byte) []byte {
			return rewriteQuotedLink(imageURLPattern, m)
		})
	}
	if bytes.Contains(body, []byte(`"source"`)) && bytes.Contains(body, []byte(`"url"`)) {
		body = claudeImageURLPattern.ReplaceAllFunc(body, func(m []byte) []byte {
			return rewriteQuotedLink(claudeImageURLPattern, m)
		})
	}
	if bytes.Contains(body, []byte(`"fileUri"`)) || bytes.Contains(body, []byte(`"file_uri"`)) {
		body = fileDataPattern.ReplaceAllFunc(body, func(m []byte) []byte {
			loc := fileURIPattern.FindSubmatchIndex(m)
			if loc == nil || !imageMimeTypePattern.Match(m) || isGeminiFileHandle(m[loc[2]:loc[3]]) {
				return m
			}
			out := append([]byte(nil), m[:loc[0]]...)
			out = append(out, rewriteQuotedLink(fileURIPattern, m[loc[0]:loc[1]])...)
			return append(out, m[loc[1]:]...)
		})
	}
	if bytes.Contains(body, []byte("](http")) {
		body = markdownImagePattern.ReplaceAllFunc(body, func(m []byte) []byte {
			sub := markdownImagePattern.FindSubmatchIndex(m)
			quoted := append(append([]byte{'"'}, m[sub[2]:sub[3]]...), '"')
			encoded, ok := rewriteJSONString(quoted)
			if !ok {
				return m
			}
			out := append([]byte(nil), m[:sub[2]]...)
			out = append(out, encoded[1:len(encoded)-1]...)
			return append(out, ')')
		})
	}
	if bytes.Contains(body, []byte(`"url"`)) && dataArrayPattern.Match(body) {
		body = rewriteImageItemURLs(body)
	}
	return body
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
func rewriteImageItemURLs(body []byte) []byte {
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
		encoded, ok := rewriteJSONString(body[span.start:span.end])
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
