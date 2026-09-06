//go:build purego && linux

package purego

import (
	"bytes"
	"image"
	"log"
	"os"
	"strings"
	"testing"

	"fyne.io/fyne/v2"
)

func TestPresentClosedConnectionIsReported(t *testing.T) {
	client, server := unixSocketPair(t)
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
	})
	_ = client.Close()
	_ = server.Close()

	s := &x11Surface{conn: client}
	img := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	if err := s.Present(img); err == nil {
		t.Fatal("Present on a closed connection returned no error")
	}

	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	w := newWindow("closed")
	w.surface = s
	w.shown = true
	w.visible = true
	w.canvas.Resize(fyne.NewSize(48, 32))
	w.canvas.SetDirty()
	w.redrawIfDirty()
	if !strings.Contains(buf.String(), "Present") {
		t.Fatalf("redrawIfDirty on a closed connection produced no Present log, got %q", buf.String())
	}
}
