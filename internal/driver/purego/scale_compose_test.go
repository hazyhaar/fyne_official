//go:build purego

package purego

import (
	"os"
	"testing"

	"fyne.io/fyne/v2"
)

type composeScaleSettings struct {
	scale float32
}

func (composeScaleSettings) Theme() fyne.Theme                    { return nil }
func (composeScaleSettings) SetTheme(fyne.Theme)                  {}
func (composeScaleSettings) ThemeVariant() fyne.ThemeVariant      { return 0 }
func (s composeScaleSettings) Scale() float32                     { return s.scale }
func (composeScaleSettings) PrimaryColor() string                 { return "" }
func (composeScaleSettings) AddChangeListener(chan fyne.Settings) {}
func (composeScaleSettings) AddListener(func(fyne.Settings))      {}
func (composeScaleSettings) BuildType() fyne.BuildType            { return fyne.BuildStandard }
func (composeScaleSettings) ShowAnimations() bool                 { return false }

type composeScaleApp struct {
	scaleTestApp
	settings fyne.Settings
}

func (a *composeScaleApp) Settings() fyne.Settings { return a.settings }

func installComposeScale(t *testing.T, user float32) {
	t.Helper()
	prev := fyne.CurrentApp()
	prevEnv := os.Getenv(scaleEnvKey)
	_ = os.Unsetenv(scaleEnvKey)
	fyne.SetCurrentApp(&composeScaleApp{settings: composeScaleSettings{scale: user}})
	t.Cleanup(func() {
		if prevEnv == "" {
			_ = os.Unsetenv(scaleEnvKey)
		} else {
			_ = os.Setenv(scaleEnvKey, prevEnv)
		}
		if prev != nil {
			fyne.SetCurrentApp(prev)
		}
	})
}

func TestWindowScaleComposesDetectedAndUser(t *testing.T) {
	installComposeScale(t, 1.5)
	got := windowScale(2.0)
	if got != 3.0 {
		t.Fatalf("windowScale(detected=2.0, user=1.5) = %v, want 3.0", got)
	}
}

func TestWindowScaleUserOnlyMultipliesDetected(t *testing.T) {
	installComposeScale(t, 2.0)
	got := windowScale(1.5)
	if got != 3.0 {
		t.Fatalf("windowScale(detected=1.5, user=2.0) = %v, want 3.0", got)
	}
}

func TestWindowScaleHonoursFYNE_SCALE(t *testing.T) {
	installComposeScale(t, 1.5)
	t.Setenv(scaleEnvKey, "2")
	got := windowScale(1.5)
	if got != 3.0 {
		t.Fatalf("windowScale(detected=1.5, FYNE_SCALE=2) = %v, want 3.0", got)
	}
}
