//go:build purego

package purego

import (
	"errors"
	"image"
	"net/url"
	"os"
	"testing"
	"time"

	"fyne.io/fyne/v2"
)

type scaleStubSurface struct {
	events  chan surfaceEvent
	screens []ScreenGeometry
}

func (*scaleStubSurface) Present(*image.NRGBA) error { return nil }
func (s *scaleStubSurface) Events() <-chan surfaceEvent {
	if s.events == nil {
		s.events = make(chan surfaceEvent)
	}
	return s.events
}
func (*scaleStubSurface) Resize(int, int) error { return nil }
func (*scaleStubSurface) SetTitle(string) error { return nil }
func (*scaleStubSurface) Close() error          { return nil }
func (s *scaleStubSurface) Screens() ([]ScreenGeometry, error) {
	return s.screens, nil
}

type scaleTestApp struct {
	driver fyne.Driver
}

func (*scaleTestApp) NewWindow(string) fyne.Window { return nil }
func (*scaleTestApp) OpenURL(*url.URL) error       { return nil }
func (*scaleTestApp) Icon() fyne.Resource          { return nil }
func (*scaleTestApp) SetIcon(fyne.Resource)        {}
func (*scaleTestApp) Run()                         {}
func (*scaleTestApp) Quit()                        {}
func (a *scaleTestApp) Driver() fyne.Driver        { return a.driver }
func (*scaleTestApp) UniqueID() string             { return "purego.scale" }
func (*scaleTestApp) SendNotification(*fyne.Notification) {
}
func (*scaleTestApp) ScheduleNotification(*fyne.Notification, time.Time) (*fyne.ScheduledNotification, error) {
	return nil, errors.New("unimplemented")
}
func (*scaleTestApp) CancelScheduledNotification(string) error { return nil }
func (*scaleTestApp) Settings() fyne.Settings                  { return scaleTestSettings{} }
func (*scaleTestApp) Preferences() fyne.Preferences            { return nil }
func (*scaleTestApp) Storage() fyne.Storage                    { return nil }
func (*scaleTestApp) Lifecycle() fyne.Lifecycle                { return nil }
func (*scaleTestApp) Metadata() fyne.AppMetadata               { return fyne.AppMetadata{} }
func (*scaleTestApp) CloudProvider() fyne.CloudProvider        { return nil }
func (*scaleTestApp) SetCloudProvider(fyne.CloudProvider)      {}
func (*scaleTestApp) Clipboard() fyne.Clipboard                { return nil }
func (*scaleTestApp) Cache() fyne.Cache                        { return nil }

type scaleTestSettings struct{}

func (scaleTestSettings) Theme() fyne.Theme                    { return nil }
func (scaleTestSettings) SetTheme(fyne.Theme)                  {}
func (scaleTestSettings) ThemeVariant() fyne.ThemeVariant      { return 0 }
func (scaleTestSettings) Scale() float32                       { return 0 }
func (scaleTestSettings) PrimaryColor() string                 { return "" }
func (scaleTestSettings) AddChangeListener(chan fyne.Settings) {}
func (scaleTestSettings) AddListener(func(fyne.Settings))      {}
func (scaleTestSettings) BuildType() fyne.BuildType            { return fyne.BuildStandard }
func (scaleTestSettings) ShowAnimations() bool                 { return false }

func installScaleApp(t *testing.T, d fyne.Driver) {
	t.Helper()
	prev := fyne.CurrentApp()
	fyne.SetCurrentApp(&scaleTestApp{driver: d})
	t.Cleanup(func() {
		if prev != nil {
			fyne.SetCurrentApp(prev)
		}
	})
}

func fourKThreeHundredMM() []ScreenGeometry {
	return []ScreenGeometry{{
		Width: 3840, Height: 2160,
		WidthMM: 300, HeightMM: 169,
		Primary: true,
	}}
}

func TestSystemScaleForWindowUnshownReturns1(t *testing.T) {
	d := NewDriver()
	installScaleApp(t, d)
	w := d.CreateWindow("unshown")
	if got := fyne.CurrentDevice().SystemScaleForWindow(w); got != 1 {
		t.Fatalf("SystemScaleForWindow on an unshown window = %v, want 1", got)
	}
}

func TestSystemScaleForWindowAfterShowMatchesDetected(t *testing.T) {
	_ = os.Unsetenv(scaleEnvKey)
	d := NewDriver()
	installScaleApp(t, d)
	w := d.CreateWindow("shown-scale").(*window)
	w.Show()
	if w.surface == nil {
		t.Skip("purego: no display server reachable")
	}
	t.Cleanup(func() { w.Close() })

	want := detectedScale(w.surface)
	got := fyne.CurrentDevice().SystemScaleForWindow(w)
	if got != want {
		t.Fatalf("SystemScaleForWindow = %v, want detectedScale %v", got, want)
	}
}

func TestSystemScaleForWindowMatchesDetectedFourK(t *testing.T) {
	_ = os.Unsetenv(scaleEnvKey)
	d := NewDriver()
	installScaleApp(t, d)

	surf := &scaleStubSurface{screens: fourKThreeHundredMM()}
	want := detectedScale(surf)
	if want == 1 {
		t.Fatal("detectedScale for 3840px/300mm = 1, want a HiDPI factor")
	}

	w := newWindow("fourk")
	w.driver = d.(*driverImpl)
	w.surface = surf
	w.shown = true
	w.systemScale = want

	got := fyne.CurrentDevice().SystemScaleForWindow(w)
	if got != want {
		t.Fatalf("SystemScaleForWindow = %v, want detectedScale %v", got, want)
	}
	if got == 1 {
		t.Fatal("SystemScaleForWindow = 1 on forced 4K/300mm geometry")
	}
}
