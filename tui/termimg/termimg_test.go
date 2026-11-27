package termimg

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func gradient(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x * 255 / max(1, w-1)), uint8(y * 255 / max(1, h-1)), 128, 255})
		}
	}
	return img
}

func checker(w, h, sq int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if (x/sq+y/sq)%2 == 0 {
				img.Set(x, y, color.White)
			} else {
				img.Set(x, y, color.Black)
			}
		}
	}
	return img
}

func transparent(w, h int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			a := uint8(0)
			if x >= w/2 {
				a = 255
			}
			img.Set(x, y, color.NRGBA{255, 0, 0, a})
		}
	}
	return img
}

func TestFit(t *testing.T) {
	cases := []struct{ iw, ih, mw, mh, w, h int }{
		{100, 100, 80, 24, 48, 24}, // limited by height: 48 px tall -> 48 wide
		{200, 50, 80, 24, 80, 10},  // limited by width: 80x20 px -> 10 rows
		{10, 10, 80, 24, 10, 5},    // no upscale
		{11, 11, 80, 24, 11, 6},    // odd pixel rows round up
		{1000, 1, 80, 24, 80, 1},
		{1, 1000, 80, 24, 1, 24},
		{0, 10, 80, 24, 0, 0},
		{10, 10, 0, 24, 0, 0},
	}
	for _, c := range cases {
		w, h := Fit(c.iw, c.ih, c.mw, c.mh)
		if w != c.w || h != c.h {
			t.Errorf("Fit(%d,%d,%d,%d) = %d,%d want %d,%d", c.iw, c.ih, c.mw, c.mh, w, h, c.w, c.h)
		}
		if w > c.mw || h > c.mh {
			t.Errorf("Fit exceeds box")
		}
	}
}

func TestHalfBlockDimensions(t *testing.T) {
	for _, tc := range []bool{true, false} {
		for _, img := range []image.Image{gradient(300, 200), checker(64, 64, 8), transparent(33, 17)} {
			b := img.Bounds()
			s := HalfBlock(img, 60, 20, tc)
			w, h := Fit(b.Dx(), b.Dy(), 60, 20)
			lines := strings.Split(s, "\n")
			if len(lines) != h {
				t.Fatalf("got %d lines want %d", len(lines), h)
			}
			for i, l := range lines {
				if got := ansi.StringWidth(l); got != w {
					t.Errorf("line %d width %d want %d", i, got, w)
				}
				if !strings.HasSuffix(l, "\x1b[0m") {
					t.Errorf("line %d lacks reset", i)
				}
				if strings.Count(l, "▀") != w {
					t.Errorf("line %d has %d blocks", i, strings.Count(l, "▀"))
				}
			}
			if !tc && strings.Contains(s, ";2;") {
				t.Errorf("256-color output contains truecolor SGR")
			}
			if tc && strings.Contains(s, "38;5;") {
				t.Errorf("truecolor output contains 256 SGR")
			}
		}
	}
	if HalfBlock(gradient(10, 10), 0, 0, true) != "" {
		t.Error("empty box should give empty string")
	}
}

func TestHalfBlockColors(t *testing.T) {
	// top row red, bottom row blue -> one cell fg red bg blue
	img := image.NewRGBA(image.Rect(0, 0, 1, 2))
	img.Set(0, 0, color.RGBA{255, 0, 0, 255})
	img.Set(0, 1, color.RGBA{0, 0, 255, 255})
	s := HalfBlock(img, 10, 10, true)
	if !strings.Contains(s, "38;2;255;0;0") || !strings.Contains(s, "48;2;0;0;255") {
		t.Errorf("unexpected colors: %q", s)
	}
	// fully transparent composites over black
	tr := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	s = HalfBlock(tr, 10, 10, true)
	if !strings.Contains(s, "38;2;0;0;0;48;2;0;0;0") {
		t.Errorf("transparent not black: %q", s)
	}
}

func TestColor256(t *testing.T) {
	cases := []struct {
		r, g, b uint8
		want    int
	}{
		{0, 0, 0, 16},
		{255, 255, 255, 231},
		{255, 0, 0, 196},
		{0, 255, 0, 46},
		{0, 0, 255, 21},
		{128, 128, 128, 244}, // gray ramp 8+10*12=128
		{8, 8, 8, 232},
		{238, 238, 238, 255},
		{95, 135, 175, 16 + 36*1 + 6*2 + 3},
	}
	for _, c := range cases {
		if got := Color256(c.r, c.g, c.b); got != c.want {
			t.Errorf("Color256(%d,%d,%d) = %d want %d", c.r, c.g, c.b, got, c.want)
		}
	}
	for i := 16; i < 256; i++ {
		r, g, b := palette256RGB(i)
		got := Color256(uint8(r), uint8(g), uint8(b))
		gr, gg, gb := palette256RGB(got)
		if gr != r || gg != g || gb != b {
			t.Errorf("palette %d (%d,%d,%d) maps to %d", i, r, g, b, got)
		}
	}
}

func TestDecode(t *testing.T) {
	src := gradient(20, 10)
	var pb, jb, gb bytes.Buffer
	if err := png.Encode(&pb, transparent(20, 10)); err != nil {
		t.Fatal(err)
	}
	if err := jpeg.Encode(&jb, src, nil); err != nil {
		t.Fatal(err)
	}
	if err := gif.Encode(&gb, src, nil); err != nil {
		t.Fatal(err)
	}
	for name, b := range map[string][]byte{"png": pb.Bytes(), "jpeg": jb.Bytes(), "gif": gb.Bytes()} {
		img, f, err := Decode(b)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if f != name {
			t.Errorf("format %q want %q", f, name)
		}
		if img.Bounds().Dx() != 20 || img.Bounds().Dy() != 10 {
			t.Errorf("%s: bounds %v", name, img.Bounds())
		}
	}
	if _, _, err := Decode([]byte("nope")); err == nil {
		t.Error("expected error")
	}
}

var kittyRe = regexp.MustCompile(`\x1b_G([^;]*);([A-Za-z0-9+/=]*)\x1b\\`)

func TestEncodeKitty(t *testing.T) {
	// noisy image so the PNG spans several chunks
	img := image.NewRGBA(image.Rect(0, 0, 400, 300))
	seed := uint32(1)
	for i := range img.Pix {
		seed = seed*1664525 + 1013904223
		img.Pix[i] = uint8(seed >> 24)
	}
	s, err := Encode(Kitty, img, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	ms := kittyRe.FindAllStringSubmatch(s, -1)
	if len(ms) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(ms))
	}
	if strings.Join(kittyRe.FindAllString(s, -1), "") != s {
		t.Fatal("garbage between kitty chunks")
	}
	var payload strings.Builder
	for i, m := range ms {
		if len(m[2]) > 4096 {
			t.Errorf("chunk %d len %d", i, len(m[2]))
		}
		if i < len(ms)-1 && len(m[2])%4 != 0 {
			t.Errorf("chunk %d not a multiple of 4", i)
		}
		want := "m=1"
		if i == len(ms)-1 {
			want = "m=0"
		}
		if !strings.HasSuffix(m[1], want) {
			t.Errorf("chunk %d control %q want %s", i, m[1], want)
		}
		if i == 0 {
			for _, k := range []string{"a=T", "f=100", "q=2", "c=", "r="} {
				if !strings.Contains(m[1], k) {
					t.Errorf("first chunk missing %s: %q", k, m[1])
				}
			}
		} else if m[1] != want {
			t.Errorf("later chunk has extra keys: %q", m[1])
		}
		payload.WriteString(m[2])
	}
	data, err := base64.StdEncoding.DecodeString(payload.String())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := png.Decode(bytes.NewReader(data)); err != nil {
		t.Fatalf("payload not png: %v", err)
	}
	// 400x300 into 640x384 px box -> 400x300 (no upscale) -> 50 cols x 19 rows
	if !strings.Contains(ms[0][1], "c=50,r=19") {
		t.Errorf("cell size: %q", ms[0][1])
	}
	if ClearSequence(Kitty) == "" || ClearSequence(Sixel) != "" {
		t.Error("ClearSequence")
	}
}

func TestEncodeITerm(t *testing.T) {
	s, err := Encode(ITerm2, gradient(160, 160), 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(s, "\x1b]1337;File=") || !strings.HasSuffix(s, "\a") {
		t.Fatalf("bad framing: %q...", s[:min(40, len(s))])
	}
	head, b64, ok := strings.Cut(strings.TrimSuffix(strings.TrimPrefix(s, "\x1b]1337;File="), "\a"), ":")
	if !ok {
		t.Fatal("no ':'")
	}
	for _, k := range []string{"inline=1", "width=20", "height=10", "preserveAspectRatio=1", "size="} {
		if !strings.Contains(head, k) {
			t.Errorf("missing %s in %q", k, head)
		}
	}
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := png.Decode(bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
}

func TestEncodeSixel(t *testing.T) {
	s, err := Encode(Sixel, checker(40, 20, 4), 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(s, "\x1bP") || !strings.HasSuffix(s, "\x1b\\") {
		t.Fatalf("bad framing")
	}
	if !strings.Contains(s, "q\"1;1;40;20") {
		t.Errorf("missing raster attributes")
	}
	if !strings.Contains(s, "#0;2;0;0;0") || !strings.Contains(s, "#215;2;100;100;100") {
		t.Errorf("palette missing black/white: %q", s[:min(80, len(s))])
	}
	// 20 rows -> 4 bands -> 3 '-' separators
	body := s[strings.Index(s, "q")+1 : len(s)-2]
	if strings.Count(body, "-") != 3 {
		t.Errorf("bands: %d", strings.Count(body, "-"))
	}
	if !strings.Contains(body, "!4") {
		t.Errorf("expected RLE runs of 4 in checkerboard")
	}
	if strings.ContainsAny(body, "\x1b\a") {
		t.Error("stray control chars")
	}
}

func TestSixelRLE(t *testing.T) {
	var sb strings.Builder
	writeSixelRLE(&sb, []byte("~~~~~~@@@AAAA????"))
	if got := sb.String(); got != "!6~@@@!4A" {
		t.Errorf("rle = %q", got)
	}
}

func TestEncodeNone(t *testing.T) {
	s, err := Encode(None, gradient(10, 10), 80, 24)
	if err != nil || !strings.Contains(s, "▀") {
		t.Fatalf("none: %v %q", err, s)
	}
}

func TestDetect(t *testing.T) {
	clear := func() {
		for _, k := range []string{"RUX_TUI_GRAPHICS", "TMUX", "TERM", "TERM_PROGRAM", "KITTY_WINDOW_ID", "LC_TERMINAL", "GHOSTTY_RESOURCES_DIR"} {
			t.Setenv(k, "")
		}
	}
	cases := []struct {
		env  map[string]string
		want Protocol
	}{
		{map[string]string{}, None},
		{map[string]string{"TERM": "xterm-256color"}, None},
		{map[string]string{"KITTY_WINDOW_ID": "1"}, Kitty},
		{map[string]string{"TERM": "xterm-kitty"}, Kitty},
		{map[string]string{"TERM_PROGRAM": "ghostty"}, Kitty},
		{map[string]string{"TERM_PROGRAM": "WezTerm"}, ITerm2},
		{map[string]string{"TERM_PROGRAM": "iTerm.app"}, ITerm2},
		{map[string]string{"LC_TERMINAL": "iTerm2"}, ITerm2},
		{map[string]string{"TERM_PROGRAM": "vscode"}, ITerm2},
		{map[string]string{"TERM": "foot"}, Sixel},
		{map[string]string{"TERM": "xterm-sixel"}, Sixel},
		{map[string]string{"TERM": "xterm-kitty", "TMUX": "/tmp/x"}, None},
		{map[string]string{"TERM": "xterm-kitty", "TMUX": "/tmp/x", "RUX_TUI_GRAPHICS": "kitty"}, Kitty},
		{map[string]string{"TERM": "xterm-kitty", "RUX_TUI_GRAPHICS": "none"}, None},
		{map[string]string{"RUX_TUI_GRAPHICS": "iterm"}, ITerm2},
		{map[string]string{"RUX_TUI_GRAPHICS": "sixel"}, Sixel},
	}
	for i, c := range cases {
		clear()
		for k, v := range c.env {
			t.Setenv(k, v)
		}
		if got := Detect(); got != c.want {
			t.Errorf("case %d %v: got %v want %v", i, c.env, got, c.want)
		}
	}
	if Kitty.String() != "kitty" || None.String() != "none" {
		t.Error("String")
	}
}

func TestViewerRun(t *testing.T) {
	for _, p := range []Protocol{None, Kitty, ITerm2, Sixel} {
		v := NewViewer(gradient(50, 30), "pic.png", p)
		var out bytes.Buffer
		v.SetStdin(strings.NewReader("o"))
		v.SetStdout(&out)
		if err := v.Run(); err != nil {
			t.Fatal(err)
		}
		if !v.OpenRequested {
			t.Error("OpenRequested not set")
		}
		s := out.String()
		if !strings.HasPrefix(s, "\x1b[?1049h") || !strings.HasSuffix(s, "\x1b[?1049l") {
			t.Errorf("%v: alt screen not entered/left", p)
		}
		if !strings.Contains(s, "pic.png") || !strings.Contains(s, "50x30 px") {
			t.Errorf("%v: header missing", p)
		}
		v.SetStdin(strings.NewReader("q"))
		v.Run()
		if v.OpenRequested {
			t.Error("OpenRequested should reset")
		}
	}
}

func TestSaveTemp(t *testing.T) {
	var pb bytes.Buffer
	png.Encode(&pb, gradient(2, 2))
	for _, name := range []string{"shot.png", "noext", ""} {
		p, err := SaveTemp(name, pb.Bytes())
		if err != nil {
			t.Fatal(err)
		}
		defer os.Remove(p)
		if filepath.Ext(p) != ".png" {
			t.Errorf("%q -> %s", name, p)
		}
		b, _ := os.ReadFile(p)
		if !bytes.Equal(b, pb.Bytes()) {
			t.Error("content mismatch")
		}
	}
}
