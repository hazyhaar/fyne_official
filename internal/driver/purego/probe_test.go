package purego

import "testing"

func TestProbeStageNilByDefault(t *testing.T) {
	if frameProbe.Load() != nil {
		t.Fatal("frameProbe is non-nil at test start")
	}
	probeStage(StageF2Start)
}

func TestSetFrameProbeDeliversStage(t *testing.T) {
	t.Cleanup(func() { SetFrameProbe(nil) })

	var got []FrameStage
	SetFrameProbe(func(stage FrameStage) {
		got = append(got, stage)
	})
	probeStage(StageF2Start)
	probeStage(StageF4End)

	if len(got) != 2 || got[0] != StageF2Start || got[1] != StageF4End {
		t.Fatalf("got %v, want [%v %v]", got, StageF2Start, StageF4End)
	}

	SetFrameProbe(nil)
	probeStage(StageF3Start)
	if len(got) != 2 {
		t.Fatalf("nil hook still invoked: got %v", got)
	}
}

func BenchmarkProbeStageNilHook(b *testing.B) {
	SetFrameProbe(nil)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		probeStage(StageF2Start)
	}
}
