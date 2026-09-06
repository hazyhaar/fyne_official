package purego

import "sync/atomic"

// FrameStage identifies one instrumented boundary of the frame pipeline.
type FrameStage uint8

// Stage identifiers for the frame-probe hook, one pair (Start/End) per
// boundary this driver can observe from the inside: F2 (software paint),
// F3 (NRGBA conversion), F4 (Present, up to the point it hands the frame
// to the socket).
const (
	StageF2Start FrameStage = iota
	StageF2End
	StageF3Start
	StageF3End
	StageF4Start
	StageF4End
)

// frameProbe, when non-nil, is invoked at each instrumented boundary of
// the frame pipeline. It is nil by default; SetFrameProbe installs a
// callback for instrumentation.
var frameProbe atomic.Pointer[func(FrameStage)]

func probeStage(stage FrameStage) {
	if fn := frameProbe.Load(); fn != nil {
		(*fn)(stage)
	}
}

// SetFrameProbe installs an instrumentation callback invoked at each
// instrumented boundary of the frame pipeline (software paint, NRGBA
// conversion, Present). Passing nil clears the hook. The callback runs on
// the redraw path and must not block.
func SetFrameProbe(fn func(FrameStage)) {
	if fn == nil {
		frameProbe.Store(nil)
		return
	}
	frameProbe.Store(&fn)
}
