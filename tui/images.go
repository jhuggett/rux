package tui

import (
	"context"
	"fmt"
	"image"
	"os"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/jhuggett/rux/tui/termimg"
)

// imageCache fetches assets once, decodes images and caches their half-block
// renderings per size. Fetches happen in the background; when one finishes
// the UI is told to redraw.
type imageCache struct {
	src  Source
	post func(any)
	mu   sync.Mutex
	m    map[string]*imgEntry
	tc   bool // truecolor half-blocks
}

type imgEntry struct {
	loading  bool
	raw      []byte
	mime     string
	img      image.Image
	err      error
	rendered map[[2]int]string
}

type imageLoadedMsg struct{ url string }

func newImageCache(src Source, post func(msg tea.Msg)) *imageCache {
	ct := os.Getenv("COLORTERM")
	return &imageCache{src: src, m: map[string]*imgEntry{}, post: func(a any) { post(a) },
		tc: ct == "truecolor" || ct == "24bit"}
}

// load starts fetching url if needed and returns the entry (maybe loading).
func (c *imageCache) load(url string, decode bool) *imgEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.m[url]
	if e != nil {
		return e
	}
	e = &imgEntry{loading: true, rendered: map[[2]int]string{}}
	c.m[url] = e
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		b, mime, err := c.src.Asset(ctx, url)
		var img image.Image
		if err == nil && decode {
			img, _, err = termimg.Decode(b)
		}
		c.mu.Lock()
		e.loading, e.raw, e.mime, e.img, e.err = false, b, mime, img, err
		c.mu.Unlock()
		c.post(imageLoadedMsg{url})
	}()
	return e
}

// block renders an image to fit maxW x maxH cells; while loading it returns
// a placeholder of a plausible size.
func (c *imageCache) block(url string, maxW, maxH int) string {
	e := c.load(url, true)
	c.mu.Lock()
	defer c.mu.Unlock()
	if e.loading {
		return sFaint.Render("  ⣿ loading image…")
	}
	if e.err != nil || e.img == nil {
		return sErr.Render(fmt.Sprintf("  ✗ image: %v", e.err))
	}
	key := [2]int{maxW, maxH}
	if s, ok := e.rendered[key]; ok {
		return s
	}
	s := termimg.HalfBlock(e.img, maxW, maxH, c.tc)
	e.rendered[key] = s
	return s
}

// get returns a loaded entry or nil.
func (c *imageCache) get(url string) *imgEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e := c.m[url]; e != nil && !e.loading {
		return e
	}
	return nil
}

func (c *imageCache) size(url string) (int, int) {
	if e := c.get(url); e != nil && e.img != nil {
		b := e.img.Bounds()
		return b.Dx(), b.Dy()
	}
	return 0, 0
}

// text fetches a text asset (e.g. a Diff src); ok=false while loading.
func (c *imageCache) text(url string) (string, bool) {
	e := c.load(url, false)
	c.mu.Lock()
	defer c.mu.Unlock()
	if e.loading {
		return "", false
	}
	if e.err != nil {
		return "couldn't load: " + e.err.Error(), true
	}
	return string(e.raw), true
}

func safeName(name string) string {
	name = strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r < 32 {
			return '-'
		}
		return r
	}, name)
	if name == "" {
		name = "file"
	}
	return name
}
