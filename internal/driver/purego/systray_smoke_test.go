//go:build purego

package purego

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestFyneDemoStartsWithoutPanic(t *testing.T) {
	if testing.Short() {
		t.Skip("long: builds and runs cmd/fyne_demo")
	}
	if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		t.Skip("no display server")
	}

	root := fyneModuleRoot(t)
	bin := filepath.Join(t.TempDir(), "fyne_demo")
	build := exec.Command("go", "build", "-tags", "purego", "-o", bin, "./cmd/fyne_demo") //gosec:disable G204 -- builds this module's fyne_demo.
	build.Dir = root
	build.Env = withCGODisabled(os.Environ())
	out, err := build.CombinedOutput()
	if err != nil {
		t.Fatalf("go build -tags purego ./cmd/fyne_demo: %v\n%s", err, out)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	run := exec.CommandContext(ctx, bin) //gosec:disable G204 -- binary written to t.TempDir above.
	run.Env = withCGODisabled(os.Environ())
	out, err = run.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return
	}
	if err == nil {
		t.Fatal("fyne_demo exited 0 before the 8s deadline")
	}
	t.Fatalf("fyne_demo exited before the deadline: %v\n%s", err, out)
}

func fyneModuleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "../../.."))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("module root %s: %v", root, err)
	}
	return root
}

func withCGODisabled(env []string) []string {
	out := make([]string, 0, len(env)+1)
	for _, e := range env {
		if strings.HasPrefix(e, "CGO_ENABLED=") {
			continue
		}
		out = append(out, e)
	}
	return append(out, "CGO_ENABLED=0")
}
