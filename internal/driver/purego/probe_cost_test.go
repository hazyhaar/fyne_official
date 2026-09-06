//go:build !race

package purego

import "testing"

func TestProbeStageNilHookUnderTwoNanoseconds(t *testing.T) {
	SetFrameProbe(nil)
	res := testing.Benchmark(func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			probeStage(StageF2Start)
		}
	})
	if ns := res.NsPerOp(); ns >= 2 {
		t.Fatalf("probeStage with nil hook costs %d ns/op, want < 2", ns)
	}
}
