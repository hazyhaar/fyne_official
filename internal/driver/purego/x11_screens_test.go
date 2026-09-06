//go:build purego && linux

package purego

import "testing"

// TestScreensReportsAtLeastOneScreen checks Screens() against a real xvfb
// server started with `-screen 0 1280x800x24` (the voie's test scenario):
// it must report at least one screen matching that geometry. This test
// failed before x11_screens.go existed (screenSurface had no
// implementation to call).
func TestScreensReportsAtLeastOneScreen(t *testing.T) {
	s := openX11ForTest(t, 32, 32)
	t.Cleanup(func() { _ = s.Close() })

	screens, err := s.Screens()
	if err != nil {
		t.Fatalf("Screens: %v", err)
	}
	if len(screens) == 0 {
		t.Fatal("Screens() returned no screens")
	}

	const wantWidth, wantHeight = 1280, 800
	found := false
	for _, sc := range screens {
		if sc.Width == wantWidth && sc.Height == wantHeight {
			found = true
		}
	}
	if !found {
		t.Fatalf("Screens() = %+v, want one screen matching %dx%d (the -screen 0 %dx%dx24 test server)",
			screens, wantWidth, wantHeight, wantWidth, wantHeight)
	}

	primaryCount := 0
	for _, sc := range screens {
		if sc.Primary {
			primaryCount++
		}
	}
	if primaryCount != 1 {
		t.Fatalf("Screens() reported %d primary screens, want exactly 1", primaryCount)
	}
}
