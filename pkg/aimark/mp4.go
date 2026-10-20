package aimark

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
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
// pass through Write, to find where the XMP box goes without buffering: at EOF, or
// in front of a fragmented file's mfra, which readers find through the mfro size at
// the file end and whose fragment offsets all point before it.
type MP4Tracker struct {
	pos    uint64
	next   uint64
	header []byte
	boxes  int
	mfraAt uint64
	mfra   bool
	done   bool
	refuse bool
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
	case boxType == "mfra":
		t.mfraAt, t.mfra, t.done = start, true, true
		return
	}
	// Size 0 runs to EOF, so anything appended would land inside that box.
	if size == 0 || size < uint64(len(header)) || size > math.MaxInt64-start {
		t.done, t.refuse = true, true
		return
	}
	t.next = start + size
}

// InsertOffset reports, once the whole file went through Write, where the XMP box
// goes: in front of the first top-level mfra, or at EOF when the file ended on a
// box boundary. False means the file stays as it is (a C2PA manifest, an XMP box
// of its own, or a layout the walk cannot vouch for).
func (t *MP4Tracker) InsertOffset() (uint64, bool) {
	if t.refuse || t.boxes == 0 {
		return 0, false
	}
	if t.mfra {
		return t.mfraAt, true
	}
	return t.pos, len(t.header) == 0 && t.pos == t.next
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
	at, ok := tracker.InsertOffset()
	if !ok {
		return file, false
	}
	box := MP4Box()
	out := make([]byte, 0, len(file)+len(box))
	out = append(out, file[:at]...)
	out = append(out, box...)
	return append(out, file[at:]...), true
}

// MP4Writer copies an MP4 or MOV stream to W with the XMP box at the offset
// MP4Tracker settles on. It holds back at most one box header; Close writes what is
// still held and, when the box belongs at EOF, the box.
type MP4Writer struct {
	W        io.Writer
	tracker  MP4Tracker
	held     []byte
	inserted bool
}

func (w *MP4Writer) Write(p []byte) (int, error) {
	n := len(p)
	t := &w.tracker
	for len(p) > 0 {
		if t.done || t.pos < t.next {
			take := len(p)
			if !t.done {
				take = int(min(uint64(take), t.next-t.pos))
			}
			_, _ = t.Write(p[:take])
			if _, err := w.W.Write(p[:take]); err != nil {
				return n - len(p), err
			}
			p = p[take:]
			continue
		}
		take := min(mp4HeaderLength(t.header)-len(t.header), len(p))
		w.held = append(w.held, p[:take]...)
		_, _ = t.Write(p[:take])
		p = p[take:]
		if len(t.header) > 0 {
			continue
		}
		if t.mfra && !w.inserted {
			w.inserted = true
			if _, err := w.W.Write(MP4Box()); err != nil {
				return n - len(p), err
			}
		}
		if _, err := w.W.Write(w.held); err != nil {
			return n - len(p), err
		}
		w.held = w.held[:0]
	}
	return n, nil
}

// Close finishes the copy once the source hit EOF; it does not close W.
func (w *MP4Writer) Close() error {
	if len(w.held) > 0 {
		if _, err := w.W.Write(w.held); err != nil {
			return err
		}
		w.held = nil
	}
	if w.inserted {
		return nil
	}
	if _, ok := w.tracker.InsertOffset(); !ok {
		return nil
	}
	w.inserted = true
	_, err := w.W.Write(MP4Box())
	return err
}

// A largesize header with a uuid extended type.
const mp4MaxHeaderLength = 32

// ScanMP4 finds InsertOffset for a file of the given size by reading only its
// top-level box headers through readAt, so box bodies are never downloaded.
// It gives up with an error after maxReads reads.
func ScanMP4(size int64, maxReads int, readAt func(off int64, n int) ([]byte, error)) (int64, bool, error) {
	if size <= 0 {
		return 0, false, nil
	}
	var tracker MP4Tracker
	for reads := 0; !tracker.done && tracker.pos < uint64(size); reads++ {
		if reads >= maxReads {
			return 0, false, errors.New("too many MP4 boxes")
		}
		want := min(mp4MaxHeaderLength, size-int64(tracker.pos))
		chunk, err := readAt(int64(tracker.pos), int(want))
		if err != nil {
			return 0, false, err
		}
		if len(chunk) == 0 {
			return 0, false, io.ErrUnexpectedEOF
		}
		_, _ = tracker.Write(chunk)
		if len(tracker.header) == 0 && tracker.pos < tracker.next {
			tracker.pos = min(tracker.next, uint64(size))
		}
	}
	at, ok := tracker.InsertOffset()
	return int64(at), ok, nil
}
