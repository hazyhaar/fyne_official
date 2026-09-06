//go:build purego

package purego

import (
	"fmt"
	"os"
	"testing"

	"fyne.io/fyne/v2/internal/async"
)

// TestMain is the regression test for the panic that happened before
// driver.go's init() called async.SetMainGoroutine(): no
// goroutine in this package was ever registered as Fyne's main goroutine,
// so async.IsMainGoroutine() returned false even for the process's actual
// main goroutine and driverImpl.Run()/ShowAndRun() panicked
// unconditionally with "Run() or ShowAndRun() must be called from main
// goroutine" — not just under test, in ordinary application use too (see
// driver.go's init, and internal/driver/glfw/loop.go:30-33 for the pattern
// it mirrors).
//
// This has to live in TestMain, not a regular Test function: go test's
// testing.tRunner starts every individual test on its own new goroutine
// (`go tRunner(...)`), never on the goroutine that ran the package's init()
// functions. TestMain itself runs on that original goroutine, so it is the
// only place in this test binary that can exercise the actual bug — the
// same asymmetry that makes the bug invisible to a naive "does this panic"
// test written as an ordinary Test function.
func TestMain(m *testing.M) {
	if !async.IsMainGoroutine() {
		fmt.Fprintln(os.Stderr, "internal/driver/purego: this goroutine is not registered as Fyne's main goroutine; init() is not calling async.SetMainGoroutine() (see driver.go)")
		os.Exit(1) //nolint:revive // redundant-test-main-exit: this exits before m.Run() ever starts the suite, not the trivial os.Exit(m.Run()) wrapper the rule targets
	}

	d := NewDriver().(*driverImpl)
	d.DoFromGoroutine(func() { d.Quit() }, false)
	d.Run() // must return normally; the pre-fix package panicked here

	os.Exit(m.Run()) //nolint:revive // redundant-test-main-exit: TestMain carries essential setup above; it is not the redundant no-op wrapper the rule targets
}

func newTestDriver() *driverImpl {
	return &driverImpl{
		done:      make(chan struct{}),
		funcQueue: async.NewUnboundedChan[funcCall](),
		wake:      make(chan struct{}, 1),
	}
}
