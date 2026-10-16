package common

import (
	"fmt"
	"runtime"
	"strings"
)

const appModulePrefix = "github.com/QuantumNous/new-api/"

// Gin prints the stack one frame per line on stderr, which Loki splits away from the request.
func PanicFrames(limit int) string {
	pcs := make([]uintptr, 64)
	frames := runtime.CallersFrames(pcs[:runtime.Callers(2, pcs)])
	sites := make([]string, 0, limit)
	// Frames above the first gopanic are the recover handler itself.
	panicking := false
	for len(sites) < limit {
		frame, more := frames.Next()
		if frame.Function == "runtime.gopanic" {
			panicking = true
		} else if fn, ok := strings.CutPrefix(frame.Function, appModulePrefix); ok && panicking {
			sites = append(sites, fmt.Sprintf("%s:%d", fn, frame.Line))
		}
		if !more {
			break
		}
	}
	return strings.Join(sites, " <- ")
}
