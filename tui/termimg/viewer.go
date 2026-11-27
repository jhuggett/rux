package termimg

import (
	"fmt"
	"image"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"
)

// Viewer is a full-screen image viewer that runs while Bubble Tea has
// released the terminal. It implements tea.ExecCommand; use it with
// tea.Exec(v, func(err error) tea.Msg { ... v.OpenRequested ... }).
type Viewer struct {
	Img      image.Image
	Title    string
	Protocol Protocol
	// OpenRequested is set after Run if the user pressed 'o'.
	OpenRequested bool

	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
}

// NewViewer returns a viewer for img.
func NewViewer(img image.Image, title string, p Protocol) *Viewer {
	return &Viewer{Img: img, Title: title, Protocol: p}
}

func (v *Viewer) SetStdin(r io.Reader)  { v.stdin = r }
func (v *Viewer) SetStdout(w io.Writer) { v.stdout = w }
func (v *Viewer) SetStderr(w io.Writer) { v.stderr = w }

// Run draws the image full screen and waits for one key.
func (v *Viewer) Run() error {
	in, out := v.stdin, v.stdout
	if in == nil {
		in = os.Stdin
	}
	if out == nil {
		out = os.Stdout
	}
	v.OpenRequested = false

	cols, rows := termSize(out)
	cellW, cellH := DefaultCellW, DefaultCellH
	if f, ok := out.(*os.File); ok {
		if cw, ch, ok := cellPixels(f.Fd()); ok {
			cellW, cellH = cw, ch
		}
	}

	if f, ok := in.(*os.File); ok && term.IsTerminal(f.Fd()) {
		if st, err := term.MakeRaw(f.Fd()); err == nil {
			defer term.Restore(f.Fd(), st)
		}
	}

	// alt screen, hide cursor, clear, home
	io.WriteString(out, "\x1b[?1049h\x1b[?25l\x1b[2J\x1b[H")
	defer func() {
		io.WriteString(out, ClearSequence(v.Protocol)+"\x1b[0m\x1b[2J\x1b[?25h\x1b[?1049l")
	}()

	var sb strings.Builder
	sb.WriteString(v.header(cols))

	if v.Img != nil && rows > 1 {
		b := v.Img.Bounds()
		availH := rows - 1
		if v.Protocol == None {
			w, ph := fitPixels(b.Dx(), b.Dy(), cols, availH*2, true)
			art := halfBlockSized(v.Img, w, ph, true)
			left := (cols-w)/2 + 1
			top := 2 + (availH-(ph+1)/2)/2
			for i, line := range strings.Split(art, "\n") {
				fmt.Fprintf(&sb, "\x1b[%d;%dH%s", top+i, left, line)
			}
		} else {
			pw, ph := fitPixels(b.Dx(), b.Dy(), cols*cellW, availH*cellH, true)
			icols := min(cols, (pw+cellW-1)/cellW)
			irows := min(availH, (ph+cellH-1)/cellH)
			seq, err := EncodeCell(v.Protocol, v.Img, cols, availH, cellW, cellH, true)
			if err != nil {
				fmt.Fprintf(&sb, "\x1b[3;1H%v", err)
			} else {
				fmt.Fprintf(&sb, "\x1b[%d;%dH%s", 2+(availH-irows)/2, (cols-icols)/2+1, seq)
			}
		}
	}
	io.WriteString(out, sb.String())

	buf := make([]byte, 64)
	n, err := in.Read(buf)
	if n > 0 && (buf[0] == 'o' || buf[0] == 'O') {
		v.OpenRequested = true
	}
	if err != nil && err != io.EOF {
		return err
	}
	return nil
}

func (v *Viewer) header(cols int) string {
	info := ""
	if v.Img != nil {
		b := v.Img.Bounds()
		info = fmt.Sprintf("%dx%d px", b.Dx(), b.Dy())
	}
	left := strings.TrimSpace(v.Title + "  " + info + "  [" + v.Protocol.String() + "]")
	right := "any key to close · o open externally"
	line := left
	if gap := cols - ansi.StringWidth(left) - ansi.StringWidth(right); gap >= 2 {
		line = left + strings.Repeat(" ", gap) + right
	} else {
		line = left + "  " + right
	}
	line = ansi.Truncate(line, cols, "…")
	return "\x1b[1;1H\x1b[7m" + line + strings.Repeat(" ", max(0, cols-ansi.StringWidth(line))) + "\x1b[0m"
}

func termSize(out io.Writer) (int, int) {
	if f, ok := out.(*os.File); ok {
		if w, h, err := term.GetSize(f.Fd()); err == nil && w > 0 && h > 0 {
			return w, h
		}
	}
	w, _ := strconv.Atoi(os.Getenv("COLUMNS"))
	h, _ := strconv.Atoi(os.Getenv("LINES"))
	if w <= 0 {
		w = 80
	}
	if h <= 0 {
		h = 24
	}
	return w, h
}

// OpenExternal opens path with the OS opener (open on darwin, xdg-open
// otherwise) without waiting for it.
func OpenExternal(path string) error {
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	cmd := exec.Command(name, path)
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("termimg: %s: %w", name, err)
	}
	go cmd.Wait() // reap
	return nil
}

// SaveTemp writes b to a temp file whose extension comes from name (or is
// sniffed from the content) and returns its path.
func SaveTemp(name string, b []byte) (string, error) {
	ext := strings.ToLower(filepath.Ext(name))
	if ext == "" || len(ext) > 6 {
		ext = ""
		switch http.DetectContentType(b) {
		case "image/png":
			ext = ".png"
		case "image/jpeg":
			ext = ".jpg"
		case "image/gif":
			ext = ".gif"
		case "image/webp":
			ext = ".webp"
		}
	}
	base := strings.TrimSuffix(filepath.Base(name), filepath.Ext(name))
	base = strings.Map(func(r rune) rune {
		if r == '*' || r == '/' || r == '\\' || r < ' ' {
			return '_'
		}
		return r
	}, base)
	if base == "" || base == "." {
		base = "image"
	}
	f, err := os.CreateTemp("", "rux-"+base+"-*"+ext)
	if err != nil {
		return "", err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return f.Name(), nil
}
