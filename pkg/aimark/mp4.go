package aimark

import (
	"bytes"
	"encoding/binary"
	"math"
)

var (
	xmpUUID  = []byte{0xBE, 0x7A, 0xCF, 0xCB, 0x97, 0xA9, 0x42, 0xE8, 0x9C, 0x71, 0x99, 0x94, 0x91, 0xE3, 0xAF, 0xAC}
	c2paUUID = []byte{0xD8, 0xFE, 0xC3, 0xD6, 0x1B, 0x0E, 0x48, 0x3C, 0x92, 0x97, 0x58, 0x28, 0x87, 0x7E, 0xC4, 0x81}
)

// IsMP4 reports whether the leading bytes start an ISO BMFF file (MP4, MOV) with ftyp.
func IsMP4(head []byte) bool {
	return len(head) >= 8 && string(head[4:8]) == "ftyp"
}

// IsImage reports whether the leading bytes start a PNG, JPEG or WebP image.
func IsImage(head []byte) bool {
	return bytes.HasPrefix(head, pngSignature) ||
		len(head) > 3 && head[0] == 0xFF && head[1] == 0xD8 && head[2] == 0xFF ||
		len(head) >= 12 && string(head[0:4]) == "RIFF" && string(head[8:12]) == "WEBP"
}

// MP4Tracker walks the top-level box headers of an MP4 or MOV stream as its bytes
// pass through Write, so the XMP box can be appended at EOF without buffering.
type MP4Tracker struct {
	pos      uint64
	next     uint64
	header   []byte
	boxes    int
	lastType string
	done     bool
	refuse   bool
}

func mp4HeaderLength(header []byte) int {
	if len(header) < 8 {
		return 8
	}
	n := 8
	if binary.BigEndian.Uint32(header[0:4]) == 1 {
		n = 16
	}
	if string(header[4:8]) == "uuid" {
		n += 16
	}
	return n
}

func (t *MP4Tracker) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 && !t.done {
		if t.pos < t.next {
			skip := uint64(len(p))
			if remaining := t.next - t.pos; skip > remaining {
				skip = remaining
			}
			t.pos += skip
			p = p[skip:]
			continue
		}
		need := mp4HeaderLength(t.header)
		take := min(need-len(t.header), len(p))
		t.header = append(t.header, p[:take]...)
		t.pos += uint64(take)
		p = p[take:]
		if len(t.header) < mp4HeaderLength(t.header) {
			continue
		}
		t.readHeader()
	}
	return n, nil
}

func (t *MP4Tracker) readHeader() {
	header := t.header
	t.header = t.header[:0]
	start := t.pos - uint64(len(header))
	boxType := string(header[4:8])
	size := uint64(binary.BigEndian.Uint32(header[0:4]))
	if size == 1 {
		size = binary.BigEndian.Uint64(header[8:16])
	}
	if t.boxes == 0 && boxType != "ftyp" {
		t.done, t.refuse = true, true
		return
	}
	t.boxes++
	t.lastType = boxType
	switch {
	case boxType == "uuid":
		id := header[len(header)-16:]
		if bytes.Equal(id, c2paUUID) || bytes.Equal(id, xmpUUID) {
			t.done, t.refuse = true, true
			return
		}
	case boxType == "jumb":
		t.done, t.refuse = true, true
		return
	}
	// Size 0 runs to EOF, so anything appended would land inside that box.
	if size == 0 || size < uint64(len(header)) || size > math.MaxInt64-start {
		t.done, t.refuse = true, true
		return
	}
	t.next = start + size
}

// ShouldAppend reports, once the whole file went through Write, whether appending
// MP4Box keeps the file valid: it ended on a box boundary, carries no C2PA manifest
// or XMP box, and does not end in mfra, which readers locate from the file end.
func (t *MP4Tracker) ShouldAppend() bool {
	return !t.refuse && t.boxes > 0 && len(t.header) == 0 && t.pos == t.next && t.lastType != "mfra"
}

// MP4Box returns a top-level uuid box with the AI-generated XMP packet.
func MP4Box() []byte {
	box := binary.BigEndian.AppendUint32(nil, uint32(8+16+len(xmpPacket)))
	box = append(box, "uuid"...)
	box = append(box, xmpUUID...)
	return append(box, xmpPacket...)
}

func markMP4(file []byte) ([]byte, bool) {
	var tracker MP4Tracker
	_, _ = tracker.Write(file)
	if !tracker.ShouldAppend() {
		return file, false
	}
	box := MP4Box()
	out := make([]byte, 0, len(file)+len(box))
	out = append(out, file...)
	return append(out, box...), true
}
