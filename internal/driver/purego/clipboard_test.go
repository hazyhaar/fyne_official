//go:build purego

package purego

import (
	"errors"
	"net/url"
	"testing"
	"time"

	"fyne.io/fyne/v2"
)

type clipboardTestApp struct {
	driver fyne.Driver
	clip   fyne.Clipboard
}

func (*clipboardTestApp) NewWindow(string) fyne.Window { return nil }
func (*clipboardTestApp) OpenURL(*url.URL) error       { return nil }
func (*clipboardTestApp) Icon() fyne.Resource          { return nil }
func (*clipboardTestApp) SetIcon(fyne.Resource)        {}
func (*clipboardTestApp) Run()                         {}
func (*clipboardTestApp) Quit()                        {}
func (a *clipboardTestApp) Driver() fyne.Driver        { return a.driver }
func (*clipboardTestApp) UniqueID() string             { return "purego.clipboard" }
func (*clipboardTestApp) SendNotification(*fyne.Notification) {
}
func (*clipboardTestApp) ScheduleNotification(*fyne.Notification, time.Time) (*fyne.ScheduledNotification, error) {
	return nil, errors.New("unimplemented")
}
func (*clipboardTestApp) CancelScheduledNotification(string) error { return nil }
func (*clipboardTestApp) Settings() fyne.Settings                  { return nil }
func (*clipboardTestApp) Preferences() fyne.Preferences            { return nil }
func (*clipboardTestApp) Storage() fyne.Storage                    { return nil }
func (*clipboardTestApp) Lifecycle() fyne.Lifecycle                { return nil }
func (*clipboardTestApp) Metadata() fyne.AppMetadata               { return fyne.AppMetadata{} }
func (*clipboardTestApp) CloudProvider() fyne.CloudProvider        { return nil }
func (*clipboardTestApp) SetCloudProvider(fyne.CloudProvider)      {}
func (a *clipboardTestApp) Clipboard() fyne.Clipboard              { return a.clip }
func (*clipboardTestApp) Cache() fyne.Cache                        { return nil }

func installClipboardApp(t *testing.T, d fyne.Driver, clip fyne.Clipboard) {
	t.Helper()
	prev := fyne.CurrentApp()
	fyne.SetCurrentApp(&clipboardTestApp{driver: d, clip: clip})
	t.Cleanup(func() {
		if prev != nil {
			fyne.SetCurrentApp(prev)
		}
	})
}

// TestAppClipboardMemoryWithoutWindow is the no-window fallback: App.Clipboard
// must round-trip in process memory when no window has been shown.
func TestAppClipboardMemoryWithoutWindow(t *testing.T) {
	d := NewDriver()
	clip := NewClipboard()
	installClipboardApp(t, d, clip)

	const want = "memory clipboard round trip"
	fyne.CurrentApp().Clipboard().SetContent(want)
	if got := fyne.CurrentApp().Clipboard().Content(); got != want {
		t.Fatalf("Clipboard().Content() = %q, want %q", got, want)
	}
}
