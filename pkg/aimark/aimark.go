// Package aimark tags generated images as AI-generated in machine-readable form
// (EU AI Act Art. 50(2)): an XMP packet with the IPTC DigitalSourceType
// trainedAlgorithmicMedia, inserted without re-encoding the pixels.
package aimark

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"hash/crc32"
	"regexp"
	"strings"
)

const xmpPacket = `<?xpacket begin="` + "\uFEFF" + `" id="W5M0MpCehiHzreSzNTczkc9d"?>` +
	`<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
	`<rdf:Description rdf:about="" xmlns:Iptc4xmpExt="http://iptc.org/std/Iptc4xmpExt/2008-02-29/" xmlns:xmp="http://ns.adobe.com/xap/1.0/"` +
	` Iptc4xmpExt:DigitalSourceType="http://cv.iptc.org/newscodes/digitalsourcetype/trainedAlgorithmicMedia"` +
	` xmp:CreatorTool="UnoRouter"/>` +
	`</rdf:RDF></x:xmpmeta><?xpacket end="w"?>`

const jpegXMPNamespace = "http://ns.adobe.com/xap/1.0/\x00"

var (
	pngSignature = []byte("\x89PNG\r\n\x1a\n")
	// Marks a provider already wrote (C2PA manifests, IPTC source type), plus our own.
	existingMarks = [][]byte{
		[]byte("trainedAlgorithmicMedia"),
		[]byte("compositeSynthetic"),
		[]byte("c2pa"),
	}
)

// Mark returns the image with the AI-generated XMP packet added. Unsupported formats,
// images that already carry a mark, and images with an XMP packet of their own come
// back unchanged with false.
func Mark(img []byte) ([]byte, bool) {
	for _, m := range existingMarks {
		if bytes.Contains(img, m) {
			return img, false
		}
	}
	switch {
	case bytes.HasPrefix(img, pngSignature):
		return markPNG(img)
	case len(img) > 3 && img[0] == 0xFF && img[1] == 0xD8 && img[2] == 0xFF:
		return markJPEG(img)
	case len(img) > 16 && string(img[0:4]) == "RIFF" && string(img[8:12]) == "WEBP":
		return markWebP(img)
	}
	return img, false
}

func markPNG(img []byte) ([]byte, bool) {
	if bytes.Contains(img, []byte("XML:com.adobe.xmp")) {
		return img, false
	}
	// IHDR is always first: 8 signature + 4 length + 4 type + 13 data + 4 crc.
	const afterIHDR = 33
	if len(img) < afterIHDR || string(img[12:16]) != "IHDR" {
		return img, false
	}
	data := append([]byte("XML:com.adobe.xmp\x00\x00\x00\x00\x00"), xmpPacket...)
	chunk := make([]byte, 0, len(data)+12)
	chunk = binary.BigEndian.AppendUint32(chunk, uint32(len(data)))
	typed := append([]byte("iTXt"), data...)
	chunk = append(chunk, typed...)
	chunk = binary.BigEndian.AppendUint32(chunk, crc32.ChecksumIEEE(typed))

	out := make([]byte, 0, len(img)+len(chunk))
	out = append(out, img[:afterIHDR]...)
	out = append(out, chunk...)
	out = append(out, img[afterIHDR:]...)
	return out, true
}

func markJPEG(img []byte) ([]byte, bool) {
	// Walk the header segments: refuse a second XMP packet, and insert after SOI and
	// any APP0 (JFIF must stay first).
	insertAt := 2
	for pos := 2; pos+4 <= len(img) && img[pos] == 0xFF; {
		marker := img[pos+1]
		if marker == 0xDA || marker == 0xD9 {
			break
		}
		segLen := int(binary.BigEndian.Uint16(img[pos+2 : pos+4]))
		if segLen < 2 || pos+2+segLen > len(img) {
			return img, false
		}
		payload := img[pos+4 : pos+2+segLen]
		if marker == 0xE1 && bytes.HasPrefix(payload, []byte(jpegXMPNamespace)) {
			return img, false
		}
		if marker == 0xE0 && insertAt == pos {
			insertAt = pos + 2 + segLen
		}
		pos += 2 + segLen
	}
	body := append([]byte(jpegXMPNamespace), xmpPacket...)
	seg := []byte{0xFF, 0xE1}
	seg = binary.BigEndian.AppendUint16(seg, uint16(len(body)+2))
	seg = append(seg, body...)

	out := make([]byte, 0, len(img)+len(seg))
	out = append(out, img[:insertAt]...)
	out = append(out, seg...)
	out = append(out, img[insertAt:]...)
	return out, true
}

const (
	webpFlagAlpha = 0x10
	webpFlagXMP   = 0x04
)

func markWebP(img []byte) ([]byte, bool) {
	first := string(img[12:16])
	firstLen := int(binary.LittleEndian.Uint32(img[16:20]))
	if 20+firstLen > len(img) {
		return img, false
	}
	var out []byte
	switch first {
	case "VP8X":
		if img[20]&webpFlagXMP != 0 {
			return img, false
		}
		out = append([]byte(nil), img...)
		out[20] |= webpFlagXMP
	case "VP8 ", "VP8L":
		w, h, alpha, ok := webpSize(first, img[20:20+firstLen])
		if !ok {
			return img, false
		}
		vp8x := make([]byte, 18)
		copy(vp8x, "VP8X")
		binary.LittleEndian.PutUint32(vp8x[4:8], 10)
		vp8x[8] = webpFlagXMP
		if alpha {
			vp8x[8] |= webpFlagAlpha
		}
		putUint24(vp8x[12:15], w-1)
		putUint24(vp8x[15:18], h-1)
		out = make([]byte, 0, len(img)+len(vp8x))
		out = append(out, img[:12]...)
		out = append(out, vp8x...)
		out = append(out, img[12:]...)
	default:
		return img, false
	}
	chunk := []byte("XMP ")
	chunk = binary.LittleEndian.AppendUint32(chunk, uint32(len(xmpPacket)))
	chunk = append(chunk, xmpPacket...)
	if len(xmpPacket)%2 == 1 {
		chunk = append(chunk, 0)
	}
	out = append(out, chunk...)
	binary.LittleEndian.PutUint32(out[4:8], uint32(len(out)-8))
	return out, true
}

func webpSize(kind string, data []byte) (w, h int, alpha, ok bool) {
	if kind == "VP8L" {
		if len(data) < 5 || data[0] != 0x2F {
			return 0, 0, false, false
		}
		bits := binary.LittleEndian.Uint32(data[1:5])
		return int(bits&0x3FFF) + 1, int((bits>>14)&0x3FFF) + 1, (bits>>28)&1 == 1, true
	}
	if len(data) < 10 || data[3] != 0x9D || data[4] != 0x01 || data[5] != 0x2A {
		return 0, 0, false, false
	}
	return int(binary.LittleEndian.Uint16(data[6:8]) & 0x3FFF), int(binary.LittleEndian.Uint16(data[8:10]) & 0x3FFF), false, true
}

func putUint24(b []byte, v int) {
	b[0], b[1], b[2] = byte(v), byte(v>>8), byte(v>>16)
}

// MarkBase64 marks a base64-encoded image. Anything that does not decode to a
// supported image is returned unchanged.
func MarkBase64(s string) (string, bool) {
	enc := base64.StdEncoding
	raw, err := enc.DecodeString(s)
	if err != nil {
		enc = base64.RawStdEncoding
		if raw, err = enc.DecodeString(s); err != nil {
			return s, false
		}
	}
	marked, ok := Mark(raw)
	if !ok {
		return s, false
	}
	return enc.EncodeToString(marked), true
}

var (
	dataURLPattern = regexp.MustCompile(`data:image/(?:png|jpe?g|webp);base64,([A-Za-z0-9+/]+={0,2})`)
	// JSON string fields that carry raw base64 images across the providers we relay
	// (OpenAI images and Responses, Gemini inlineData and Imagen, Runware).
	b64FieldPattern = regexp.MustCompile(`"(b64_json|result|bytesBase64Encoded|imageBase64Data|data|partial_image_b64)"(\s*:\s*")([A-Za-z0-9+/]{256,}={0,2})"`)
	triggers        = [][]byte{
		[]byte("data:image/"), []byte(`"b64_json"`), []byte(`"result"`), []byte(`"bytesBase64Encoded"`),
		[]byte(`"imageBase64Data"`), []byte(`"inlineData"`), []byte(`"inline_data"`), []byte(`"partial_image_b64"`),
	}
)

// RewriteBody marks every image embedded in a response body or stream chunk, either
// as a data URL or as a base64 JSON field. Bodies without such content are returned
// as is without scanning further.
func RewriteBody(body []byte) []byte {
	if !mayContainImage(body) {
		return body
	}
	body = dataURLPattern.ReplaceAllFunc(body, func(m []byte) []byte {
		sub := dataURLPattern.FindSubmatchIndex(m)
		marked, ok := MarkBase64(string(m[sub[2]:sub[3]]))
		if !ok {
			return m
		}
		return append(append([]byte(nil), m[:sub[2]]...), marked...)
	})
	return b64FieldPattern.ReplaceAllFunc(body, func(m []byte) []byte {
		sub := b64FieldPattern.FindSubmatchIndex(m)
		marked, ok := MarkBase64(string(m[sub[6]:sub[7]]))
		if !ok {
			return m
		}
		out := append([]byte(nil), m[:sub[6]]...)
		out = append(out, marked...)
		return append(out, '"')
	})
}

// RewriteString is RewriteBody for string payloads.
func RewriteString(s string) string {
	for _, t := range triggers {
		if strings.Contains(s, string(t)) {
			return string(RewriteBody([]byte(s)))
		}
	}
	return s
}

func mayContainImage(b []byte) bool {
	for _, t := range triggers {
		if bytes.Contains(b, t) {
			return true
		}
	}
	return false
}
