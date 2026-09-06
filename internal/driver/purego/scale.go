//go:build purego

package purego

import (
	"math"
	"os"
	"strconv"

	"fyne.io/fyne/v2"
)

// scaleEnvKey mirrors the glfw driver's FYNE_SCALE variable
// (internal/driver/glfw/scale.go:16) so a purego app honours the same user
// override.
const scaleEnvKey = "FYNE_SCALE"

// windowScale composes the system-detected scale with the user scale the
// same way internal/driver/glfw/window.go calculatedScale does: userScale
// (FYNE_SCALE, then Settings().Scale(), else 1.0) multiplied by detected,
// rounded to one decimal place. A missing or non-positive detected factor
// is treated as 1.0 so a user setting still applies on its own.
func windowScale(detected float32) float32 {
	system := detected
	if system <= 0 {
		system = 1.0
	}
	return calculateScale(userScale(), system)
}

func calculateScale(user, system float32) float32 {
	if user < 0 {
		user = 1.0
	}
	raw := system * user
	return float32(math.Round(float64(raw*10.0))) / 10.0 //revive:disable-line:add-constant
}

func userScale() float32 {
	env := os.Getenv(scaleEnvKey)
	if env != "" && env != "auto" {
		if v, err := strconv.ParseFloat(env, 32); err == nil && v != 0 {
			return float32(v)
		}
		fyne.LogError("purego: invalid "+scaleEnvKey+" value", nil)
	}
	if env != "auto" {
		if app := fyne.CurrentApp(); app != nil {
			if settings := app.Settings(); settings != nil {
				if setting := settings.Scale(); setting > 0 {
					return setting
				}
			}
		}
	}
	return 1.0
}

// detectedScale asks the surface for real screen geometry through the
// optional screenSurface interface and derives a scale from the primary
// screen's DPI the same way internal/driver/glfw/scale.go:20
// (calculateDetectedScale) does. It returns 1.0 when the surface does not
// implement screenSurface (no RandR / wl_output geometry yet).
func detectedScale(s surface) float32 {
	sc, ok := s.(screenSurface)
	if !ok {
		return 1.0
	}
	screens, err := sc.Screens()
	if err != nil || len(screens) == 0 {
		return 1.0
	}
	primary := screens[0]
	for _, sr := range screens {
		if sr.Primary {
			primary = sr
			break
		}
	}
	return scaleFromGeometry(primary.Width, primary.WidthMM)
}

// scaleFromGeometry derives a scale from pixel width and millimetre width
// the same way internal/driver/glfw/scale.go calculateDetectedScale does.
func scaleFromGeometry(widthPx, widthMM int) float32 {
	const (
		baselineDPI = 120.0
		mmPerInch   = 25.4
		maxDPI      = 1000
		minDPI      = 10
	)
	if widthMM <= 0 {
		return 1.0
	}
	dpi := float32(widthPx) / (float32(widthMM) / mmPerInch)
	if dpi > maxDPI || dpi < minDPI {
		return 1.0
	}
	scale := dpi / baselineDPI
	if scale < 1.0 {
		return 1.0
	}
	return scale
}
