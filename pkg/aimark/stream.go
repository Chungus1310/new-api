package aimark

import (
	"bytes"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
)

const (
	maxHeldLabel   = 2 << 10
	maxHeldLink    = 8 << 10
	maxHeldDataURL = 32 << 20
)

// StreamEvent is one SSE payload; Type is the event name for formats that send one.
type StreamEvent struct {
	Type string
	Data []byte
}

// StreamCarry holds back streamed text that may be the start of a markdown image
// link until the link is complete, so RewriteBody sees it whole although the
// provider spread it over several deltas. Held text moves into a later delta of
// the same choice or block and is never dropped or reordered: anything else for
// that choice, or the end of the stream, first releases it as a delta of its own.
// OpenAI chat, Claude messages and Gemini events are carried; other payloads pass
// through unchanged. Not safe for concurrent use.
type StreamCarry struct {
	held  map[string]*heldLink
	order []string
}

type linkPhase int

const (
	phaseBang linkPhase = iota
	phaseLabel
	phaseLabelClosed
	phaseTarget
	phaseBody
)

type heldLink struct {
	buf         []byte
	phase       linkPhase
	n           int
	targetStart int
	limit       int
	release     func(text []byte) StreamEvent
}

// textPart is one streamed text value inside an event.
type textPart struct {
	key     string
	start   int
	end     int
	text    string
	final   bool
	release func(text []byte) StreamEvent
}

// Process returns the events to send for one upstream event: releases of held
// text first, then the event itself with its text adjusted.
func (s *StreamCarry) Process(event []byte) []StreamEvent {
	if len(s.order) == 0 && bytes.IndexByte(event, '!') < 0 {
		return []StreamEvent{{Data: event}}
	}
	parts := streamTextParts(event)
	var out []StreamEvent
	for _, key := range append([]string(nil), s.order...) {
		if !slices.ContainsFunc(parts, func(part textPart) bool { return part.key == key }) {
			out = append(out, s.release(key)...)
		}
	}
	if len(parts) == 0 {
		return append(out, StreamEvent{Data: event})
	}
	var rebuilt []byte
	last := 0
	for _, part := range parts {
		held := s.held[part.key]
		if held == nil && strings.IndexByte(part.text, '!') < 0 {
			continue
		}
		emit := s.feed(part.key, part.text, part.release)
		if part.final {
			if held := s.held[part.key]; held != nil {
				emit = append(emit, held.buf...)
				s.drop(part.key)
			}
		}
		if string(emit) == part.text {
			continue
		}
		encoded, err := marshalText(emit)
		if err != nil {
			continue
		}
		rebuilt = append(rebuilt, event[last:part.start]...)
		rebuilt = append(rebuilt, encoded...)
		last = part.end
	}
	if rebuilt == nil {
		return append(out, StreamEvent{Data: event})
	}
	rebuilt = append(rebuilt, event[last:]...)
	return append(out, StreamEvent{Data: rebuilt})
}

// Flush releases all held text, for the end of the stream.
func (s *StreamCarry) Flush() []StreamEvent {
	var out []StreamEvent
	for len(s.order) > 0 {
		out = append(out, s.release(s.order[0])...)
	}
	return out
}

func (s *StreamCarry) release(key string) []StreamEvent {
	held := s.held[key]
	s.drop(key)
	if held == nil || len(held.buf) == 0 {
		return nil
	}
	return []StreamEvent{held.release(held.buf)}
}

func (s *StreamCarry) drop(key string) {
	delete(s.held, key)
	for i, k := range s.order {
		if k == key {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
}

func (s *StreamCarry) hold(key string, release func([]byte) StreamEvent) *heldLink {
	if s.held == nil {
		s.held = make(map[string]*heldLink)
	}
	held := &heldLink{buf: []byte{'!'}, phase: phaseBang, release: release}
	s.held[key] = held
	s.order = append(s.order, key)
	return held
}

// feed runs text through the held link of key and returns what may be sent now.
func (s *StreamCarry) feed(key, text string, release func([]byte) StreamEvent) []byte {
	var emit []byte
	held := s.held[key]
	if held != nil {
		held.release = release
	}
	for i := 0; i < len(text); i++ {
		if held == nil {
			next := strings.IndexByte(text[i:], '!')
			if next < 0 {
				emit = append(emit, text[i:]...)
				break
			}
			emit = append(emit, text[i:i+next]...)
			i += next
			held = s.hold(key, release)
			continue
		}
		ch := text[i]
		held.buf = append(held.buf, ch)
		switch held.step(ch) {
		case stepComplete:
			emit = append(emit, held.buf...)
			s.drop(key)
			held = nil
		case stepBroken:
			emit = append(emit, held.buf[:len(held.buf)-1]...)
			s.drop(key)
			held = nil
			if ch == '!' {
				held = s.hold(key, release)
			} else {
				emit = append(emit, ch)
			}
		}
	}
	return emit
}

type stepResult int

const (
	stepOpen stepResult = iota
	stepComplete
	stepBroken
)

var linkTargetPrefixes = []string{"http://", "https://", "data:image/"}

func (h *heldLink) step(ch byte) stepResult {
	h.n++
	switch h.phase {
	case phaseBang:
		if ch != '[' {
			return stepBroken
		}
		h.phase, h.n = phaseLabel, 0
	case phaseLabel:
		switch {
		case ch == ']':
			h.phase = phaseLabelClosed
		case ch == '"' || ch == '\n' || h.n > maxHeldLabel:
			return stepBroken
		}
	case phaseLabelClosed:
		if ch != '(' {
			return stepBroken
		}
		h.phase, h.targetStart = phaseTarget, len(h.buf)
	case phaseTarget:
		target := string(h.buf[h.targetStart:])
		open := false
		for _, prefix := range linkTargetPrefixes {
			if target == prefix {
				h.phase, h.n, h.limit = phaseBody, 0, maxHeldLink
				if prefix == "data:image/" {
					h.limit = maxHeldDataURL
				}
				return stepOpen
			}
			if len(target) < len(prefix) && prefix[:len(target)] == target {
				open = true
			}
		}
		if !open {
			return stepBroken
		}
	case phaseBody:
		switch {
		case ch == ')':
			return stepComplete
		case ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r' || ch == '"' || h.n > h.limit:
			return stepBroken
		}
	}
	return stepOpen
}

func marshalText(text []byte) ([]byte, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(string(text)); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// jsonLeaf is a scalar value of an event with its path and raw bytes.
type jsonLeaf struct {
	path  []string
	raw   []byte
	value any
	start int
	end   int
}

// streamTextParts finds the text deltas of an OpenAI chat chunk, a Claude
// content_block_delta or a Gemini response, in document order.
func streamTextParts(event []byte) []textPart {
	if len(event) == 0 || event[0] != '{' {
		return nil
	}
	leaves, ok := jsonLeaves(event)
	if !ok {
		return nil
	}
	lookup := func(path ...string) *jsonLeaf {
		for i := range leaves {
			if slices.Equal(leaves[i].path, path) {
				return &leaves[i]
			}
		}
		return nil
	}
	stringAt := func(path ...string) string {
		if leaf := lookup(path...); leaf != nil {
			s, _ := leaf.value.(string)
			return s
		}
		return ""
	}
	rawAt := func(path ...string) []byte {
		if leaf := lookup(path...); leaf != nil {
			return leaf.raw
		}
		return nil
	}

	var parts []textPart
	claudeText := stringAt("type") == "content_block_delta" && stringAt("delta", "type") == "text_delta"
	for _, leaf := range leaves {
		text, isString := leaf.value.(string)
		if !isString {
			continue
		}
		path := leaf.path
		switch {
		case len(path) == 4 && path[0] == "choices" && path[2] == "delta" && path[3] == "content":
			index := rawAt("choices", path[1], "index")
			if index == nil {
				index = []byte(path[1])
			}
			meta := map[string][]byte{}
			for _, name := range []string{"id", "object", "created", "model"} {
				if raw := rawAt(name); raw != nil {
					meta[name] = raw
				}
			}
			parts = append(parts, textPart{
				key: "openai:" + string(index), start: leaf.start, end: leaf.end, text: text,
				final:   stringAt("choices", path[1], "finish_reason") != "",
				release: openAIRelease(meta, index),
			})
		case claudeText && len(path) == 2 && path[0] == "delta" && path[1] == "text":
			index := rawAt("index")
			if index == nil {
				index = []byte("0")
			}
			parts = append(parts, textPart{
				key: "claude:" + string(index), start: leaf.start, end: leaf.end, text: text,
				release: claudeRelease(index),
			})
		case len(path) == 6 && path[0] == "candidates" && path[2] == "content" && path[3] == "parts" && path[5] == "text":
			index := rawAt("candidates", path[1], "index")
			if index == nil {
				index = []byte(path[1])
			}
			thought := false
			if leaf := lookup("candidates", path[1], "content", "parts", path[4], "thought"); leaf != nil {
				thought, _ = leaf.value.(bool)
			}
			key := "gemini:" + string(index)
			if thought {
				key += ":thought"
			}
			parts = append(parts, textPart{
				key: key, start: leaf.start, end: leaf.end, text: text,
				final:   stringAt("candidates", path[1], "finishReason") != "",
				release: geminiRelease(index, thought),
			})
		}
	}
	return parts
}

func openAIRelease(meta map[string][]byte, index []byte) func([]byte) StreamEvent {
	return func(text []byte) StreamEvent {
		encoded, _ := marshalText(text)
		out := []byte{'{'}
		for _, name := range []string{"id", "object", "created", "model"} {
			if raw, ok := meta[name]; ok {
				out = append(out, `"`+name+`":`...)
				out = append(out, raw...)
				out = append(out, ',')
			}
		}
		out = append(out, `"choices":[{"index":`...)
		out = append(out, index...)
		out = append(out, `,"delta":{"content":`...)
		out = append(out, encoded...)
		out = append(out, `},"finish_reason":null}]}`...)
		return StreamEvent{Data: out}
	}
}

func claudeRelease(index []byte) func([]byte) StreamEvent {
	return func(text []byte) StreamEvent {
		encoded, _ := marshalText(text)
		out := append([]byte(`{"type":"content_block_delta","index":`), index...)
		out = append(out, `,"delta":{"type":"text_delta","text":`...)
		out = append(out, encoded...)
		out = append(out, `}}`...)
		return StreamEvent{Type: "content_block_delta", Data: out}
	}
}

func geminiRelease(index []byte, thought bool) func([]byte) StreamEvent {
	return func(text []byte) StreamEvent {
		encoded, _ := marshalText(text)
		out := append([]byte(`{"candidates":[{"content":{"parts":[{"text":`), encoded...)
		if thought {
			out = append(out, `,"thought":true`...)
		}
		out = append(out, `}],"role":"model"},"index":`...)
		out = append(out, index...)
		out = append(out, `}]}`...)
		return StreamEvent{Data: out}
	}
}

// jsonLeaves lists every scalar of a JSON document with its path; array elements
// are named by their position.
func jsonLeaves(body []byte) ([]jsonLeaf, bool) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var leaves []jsonLeaf
	if collectJSONLeaves(dec, body, nil, &leaves) != nil {
		return nil, false
	}
	return leaves, true
}

func collectJSONLeaves(dec *json.Decoder, body []byte, path []string, leaves *[]jsonLeaf) error {
	before := int(dec.InputOffset())
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	switch tok {
	case json.Delim('{'):
		for dec.More() {
			keyTok, err := dec.Token()
			if err != nil {
				return err
			}
			key, _ := keyTok.(string)
			if err := collectJSONLeaves(dec, body, append(path[:len(path):len(path)], key), leaves); err != nil {
				return err
			}
		}
		_, err = dec.Token()
		return err
	case json.Delim('['):
		for i := 0; dec.More(); i++ {
			if err := collectJSONLeaves(dec, body, append(path[:len(path):len(path)], strconv.Itoa(i)), leaves); err != nil {
				return err
			}
		}
		_, err = dec.Token()
		return err
	}
	end := int(dec.InputOffset())
	start := before
	for start < end && strings.IndexByte(":, \t\n\r", body[start]) >= 0 {
		start++
	}
	*leaves = append(*leaves, jsonLeaf{path: path, raw: body[start:end], value: tok, start: start, end: end})
	return nil
}
