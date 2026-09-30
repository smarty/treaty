package filesystem

import (
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/smarty/treaty/internal/app"
)

// The map's meaning rides on its signal colors, and a theme is the easiest
// place to break it quietly: a violation that looks like an addition. Every
// default theme must keep its signals apart and its text readable.

const (
	signalGap   = 15.0 // CIEDE2000 between any two signals
	safeGap     = 20.0 // CIEDE2000 under each color vision deficiency
	severityGap = 12.0
)

var (
	signals     = []string{"added", "changed", "violation", "planned", "selection"}
	safeSignals = []string{"added", "changed", "violation", "planned"}

	// Machado, Oliveira and Fernandes (2009) at full severity, in linear RGB.
	deficiencies = map[string][3][3]float64{
		"protanopia":   {{0.152286, 1.052583, -0.204868}, {0.114503, 0.786281, 0.099216}, {-0.003882, -0.048116, 1.051998}},
		"deuteranopia": {{0.367322, 0.860646, -0.227968}, {0.280085, 0.672501, 0.047413}, {-0.011820, 0.042940, 0.968881}},
		"tritanopia":   {{1.255528, -0.076749, -0.178779}, {-0.078411, 0.930809, 0.147602}, {0.004733, 0.691367, 0.303900}},
	}
)

func TestDefaultThemesKeepMeaningsApart(t *testing.T) {
	themes, err := NewThemes("").Themes()
	if err != nil {
		t.Fatal(err)
	}

	light := themeByID(themes, "light")
	for _, theme := range themes {
		t.Run(theme.ID, func(t *testing.T) {
			for token := range light.Colors {
				if _, ok := theme.Colors[token]; !ok {
					t.Errorf("missing token %s", token)
				}
			}

			c := theme.Colors
			strict := strings.HasPrefix(theme.ID, "high-contrast")
			inkMin, mutedMin := 4.5, 3.0
			if strict {
				inkMin, mutedMin = 7, 4.5
			}

			for _, surface := range []string{"bg", "panel"} {
				if ratio := contrastRatio(c["ink"], c[surface]); ratio < inkMin {
					t.Errorf("ink on %s: contrast %.1f, want %.1f", surface, ratio, inkMin)
				}
			}

			if ratio := contrastRatio(c["muted"], c["panel"]); ratio < mutedMin {
				t.Errorf("muted text: contrast %.1f, want %.1f", ratio, mutedMin)
			}

			if ratio := contrastRatio(c["selection"], c["panel"]); ratio < 3 {
				t.Errorf("selection labels: contrast %.1f, want 3", ratio)
			}

			for _, signal := range append(signals, "high", "medium") {
				if ratio := contrastRatio(c[signal], c["panel"]); ratio < 2.2 {
					t.Errorf("%s against the map: contrast %.1f, want 2.2", signal, ratio)
				}
			}

			apart(t, c, signals, "", signalGap)
			apart(t, c, []string{"high", "medium", "low"}, "", severityGap)
			if strings.HasPrefix(theme.ID, "color-blind") {
				for deficiency := range deficiencies {
					apart(t, c, safeSignals, deficiency, safeGap)
				}
			}
		})
	}
}

// apart reports every pair of tokens closer than gap, as seen with a color
// vision deficiency, or with typical vision when deficiency is empty.
func apart(t *testing.T, colors map[string]string, tokens []string, deficiency string, gap float64) {
	t.Helper()
	for i, a := range tokens {
		for _, b := range tokens[i+1:] {
			if difference := deltaE2000(seen(colors[a], deficiency), seen(colors[b], deficiency)); difference < gap {
				t.Errorf("%s and %s are %.1f apart %s, want %.0f", a, b, difference, orTypical(deficiency), gap)
			}
		}
	}
}

func contrastRatio(a, b string) float64 {
	la, lb := luminance(linearRGB(a)), luminance(linearRGB(b))
	return (math.Max(la, lb) + 0.05) / (math.Min(la, lb) + 0.05)
}

func deltaE2000(a, b [3]float64) float64 {
	l1, a1, b1 := a[0], a[1], a[2]
	l2, a2, b2 := b[0], b[1], b[2]
	cBar := (math.Hypot(a1, b1) + math.Hypot(a2, b2)) / 2
	g := 0.5 * (1 - math.Sqrt(math.Pow(cBar, 7)/(math.Pow(cBar, 7)+math.Pow(25, 7))))
	a1p, a2p := a1*(1+g), a2*(1+g)
	c1p, c2p := math.Hypot(a1p, b1), math.Hypot(a2p, b2)
	h1p, h2p := hueDegrees(b1, a1p), hueDegrees(b2, a2p)
	dL, dC := l2-l1, c2p-c1p
	dh := h2p - h1p
	switch {
	case c1p*c2p == 0:
		dh = 0
	case dh > 180:
		dh -= 360
	case dh < -180:
		dh += 360
	}

	dH := 2 * math.Sqrt(c1p*c2p) * math.Sin(radians(dh/2))
	lBar, cBarP := (l1+l2)/2, (c1p+c2p)/2
	hBar := (h1p + h2p) / 2
	if math.Abs(h1p-h2p) > 180 {
		hBar += 180
	}

	if c1p*c2p == 0 {
		hBar = h1p + h2p
	}

	tt := 1 - 0.17*math.Cos(radians(hBar-30)) + 0.24*math.Cos(radians(2*hBar)) + 0.32*math.Cos(radians(3*hBar+6)) - 0.20*math.Cos(radians(4*hBar-63))
	sL := 1 + 0.015*(lBar-50)*(lBar-50)/math.Sqrt(20+(lBar-50)*(lBar-50))
	sC, sH := 1+0.045*cBarP, 1+0.015*cBarP*tt
	dTheta := 30 * math.Exp(-math.Pow((hBar-275)/25, 2))
	rT := -math.Sin(radians(2*dTheta)) * 2 * math.Sqrt(math.Pow(cBarP, 7)/(math.Pow(cBarP, 7)+math.Pow(25, 7)))
	return math.Sqrt(math.Pow(dL/sL, 2) + math.Pow(dC/sC, 2) + math.Pow(dH/sH, 2) + rT*(dC/sC)*(dH/sH))
}

func hueDegrees(b, a float64) float64 {
	return math.Mod(math.Atan2(b, a)*180/math.Pi+360, 360)
}

// lab converts linear RGB to CIELAB under D65.
func lab(rgb [3]float64) [3]float64 {
	x := (0.4124*rgb[0] + 0.3576*rgb[1] + 0.1805*rgb[2]) / 0.95047
	y := 0.2126*rgb[0] + 0.7152*rgb[1] + 0.0722*rgb[2]
	z := (0.0193*rgb[0] + 0.1192*rgb[1] + 0.9505*rgb[2]) / 1.08883
	f := func(v float64) float64 {
		if v > 0.008856 {
			return math.Cbrt(v)
		}

		return 7.787*v + 16.0/116
	}

	return [3]float64{116*f(y) - 16, 500 * (f(x) - f(y)), 200 * (f(y) - f(z))}
}

func linearRGB(hex string) (result [3]float64) {
	hex = strings.TrimPrefix(hex, "#")
	if len(hex) == 3 {
		hex = string([]byte{hex[0], hex[0], hex[1], hex[1], hex[2], hex[2]})
	}

	for i := range 3 {
		value, _ := strconv.ParseUint(hex[2*i:2*i+2], 16, 8)
		c := float64(value) / 255
		if c <= 0.04045 {
			result[i] = c / 12.92
		} else {
			result[i] = math.Pow((c+0.055)/1.055, 2.4)
		}
	}

	return result
}

func luminance(rgb [3]float64) float64 {
	return 0.2126*rgb[0] + 0.7152*rgb[1] + 0.0722*rgb[2]
}

func orTypical(deficiency string) string {
	if deficiency == "" {
		return "with typical vision"
	}

	return "with " + deficiency
}

func radians(degrees float64) float64 {
	return degrees * math.Pi / 180
}

// seen is how a color appears, in CIELAB, with a color vision deficiency.
func seen(hex, deficiency string) [3]float64 {
	rgb := linearRGB(hex)
	matrix, ok := deficiencies[deficiency]
	if !ok {
		return lab(rgb)
	}

	var result [3]float64
	for i := range 3 {
		result[i] = math.Min(1, math.Max(0, matrix[i][0]*rgb[0]+matrix[i][1]*rgb[1]+matrix[i][2]*rgb[2]))
	}

	return lab(result)
}

func themeByID(themes []app.Theme, id string) app.Theme {
	for _, theme := range themes {
		if theme.ID == id {
			return theme
		}
	}

	return app.Theme{}
}
