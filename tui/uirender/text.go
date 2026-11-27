package uirender

import (
	"regexp"
	"strings"

	"github.com/mattn/go-runewidth"
)

// renderText renders light markdown: **bold**, *italic*, `code`, [links](url),
// # headings, - / 1. lists, > quotes, --- rules and ``` fences. Source line
// breaks are kept. mono text is shown verbatim (hard-wrapped).
func renderText(c *Ctx, id string, el *Element, p map[string]any, width int) string {
	text := str(p["text"])
	base := Style{FG: toneColor(str(p["tone"]), cText)}
	if truthy(p["mono"]) {
		var out []string
		for _, l := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
			for _, ch := range breakWord(l, width) {
				out = append(out, c.Paint(base, ch))
			}
		}
		return strings.Join(out, "\n")
	}
	return strings.Join(c.markdown(text, base, width), "\n")
}

var (
	mdHeading = regexp.MustCompile(`^(#{1,6})\s+(.*)$`)
	mdRule    = regexp.MustCompile(`^(-{3,}|\*{3,}|_{3,})$`)
	mdBullet  = regexp.MustCompile(`^(\s*)([-*+]|\d{1,3}[.)])\s+(.*)$`)
	mdQuote   = regexp.MustCompile(`^\s*>\s?(.*)$`)
	mdLink    = regexp.MustCompile(`^\[([^\]]+)\]\(([^)\s]+)\)`)
)

func (c *Ctx) markdown(text string, base Style, width int) []string {
	var out []string
	blank := func() {
		if len(out) > 0 && out[len(out)-1] != "" {
			out = append(out, "")
		}
	}
	fence := false
	for _, line := range strings.Split(text, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "```") {
			fence = !fence
			continue
		}
		if fence {
			for _, ch := range breakWord(strings.TrimRight(line, " "), max(1, width-2)) {
				out = append(out, "  "+c.Paint(Style{FG: cText2}, ch))
			}
			continue
		}
		if t == "" {
			blank()
			continue
		}
		if m := mdHeading.FindStringSubmatch(t); m != nil {
			st := sTitle
			if len(m[1]) == 1 {
				st = Style{FG: cBright, Bold: true}
			}
			out = append(out, c.wrapPieces(parseInline(m[2], st), width, "", "")...)
			continue
		}
		if mdRule.MatchString(t) {
			out = append(out, c.Paint(sLine, rep("─", width)))
			continue
		}
		if m := mdBullet.FindStringSubmatch(line); m != nil {
			indent := min(len(strings.ReplaceAll(m[1], "\t", "    "))/2*2, width/3)
			marker := m[2]
			if marker == "-" || marker == "*" || marker == "+" {
				marker = "•"
			}
			first := rep(" ", indent) + c.Paint(Style{FG: cMid2}, marker) + " "
			cont := rep(" ", indent+runewidth.StringWidth(marker)+1)
			out = append(out, c.wrapPieces(parseInline(m[3], base), width, first, cont)...)
			continue
		}
		if m := mdQuote.FindStringSubmatch(line); m != nil {
			bar := c.Paint(sFaint, "│ ")
			out = append(out, c.wrapPieces(parseInline(m[1], sMuted), width, bar, bar)...)
			continue
		}
		out = append(out, c.wrapPieces(parseInline(t, base), width, "", "")...)
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}

type piece struct {
	t    string
	st   Style
	link string
}

// parseInline splits a markdown line into styled pieces.
func parseInline(s string, base Style) []piece {
	var ps []piece
	var buf strings.Builder
	bold, italic := false, false
	cur := func() Style {
		st := base
		if bold {
			st.FG, st.Bold = cWhite, true
		}
		if italic {
			st.Italic = true
		}
		return st
	}
	flush := func() {
		if buf.Len() > 0 {
			ps = append(ps, piece{t: buf.String(), st: cur()})
			buf.Reset()
		}
	}
	for i := 0; i < len(s); {
		rest := s[i:]
		switch {
		case strings.HasPrefix(rest, "**") || strings.HasPrefix(rest, "__"):
			if bold || strings.Contains(rest[2:], rest[:2]) {
				flush()
				bold = !bold
				i += 2
				continue
			}
		case rest[0] == '`':
			if j := strings.IndexByte(rest[1:], '`'); j >= 0 {
				flush()
				ps = append(ps, piece{t: rest[1 : j+1], st: Style{FG: cText2, BG: 235}})
				i += j + 2
				continue
			}
		case rest[0] == '*':
			if italic || (len(rest) > 1 && rest[1] != ' ' && strings.Contains(rest[1:], "*")) {
				flush()
				italic = !italic
				i++
				continue
			}
		case rest[0] == '[':
			if m := mdLink.FindStringSubmatch(rest); m != nil {
				flush()
				ps = append(ps, piece{t: m[1], st: Style{FG: cMid, Underline: true}, link: m[2]})
				i += len(m[0])
				continue
			}
		}
		buf.WriteByte(s[i])
		i++
	}
	flush()
	return ps
}

// wrapPieces word-wraps styled pieces; first/cont are already-styled line
// prefixes (their widths are subtracted).
func (c *Ctx) wrapPieces(ps []piece, width int, first, cont string) []string {
	type word []piece
	var words []word
	var cw word
	for _, p := range ps {
		for j, part := range strings.Split(p.t, " ") {
			if j > 0 && len(cw) > 0 {
				words = append(words, cw)
				cw = nil
			}
			if part != "" {
				cw = append(cw, piece{part, p.st, p.link})
			}
		}
	}
	if len(cw) > 0 {
		words = append(words, cw)
	}
	render := func(ws []piece) string {
		var b strings.Builder
		for i := 0; i < len(ws); {
			j, txt := i+1, ws[i].t
			for j < len(ws) && ws[j].st == ws[i].st && ws[j].link == ws[i].link {
				txt += ws[j].t
				j++
			}
			s := c.Paint(ws[i].st, txt)
			if ws[i].link != "" {
				s = c.link(ws[i].link, s)
			}
			b.WriteString(s)
			i = j
		}
		return b.String()
	}
	var out []string
	prefix := first
	avail := func() int { return max(1, width-sw(prefix)) }
	var line []piece
	lw := 0
	emit := func() {
		out = append(out, prefix+render(line))
		prefix, line, lw = cont, nil, 0
	}
	for _, w := range words {
		ww := 0
		for _, p := range w {
			ww += runewidth.StringWidth(p.t)
		}
		if lw > 0 && lw+1+ww <= avail() {
			sp := piece{t: " "}
			if last := line[len(line)-1]; last.st == w[0].st && last.link == w[0].link {
				sp.st, sp.link = last.st, last.link
			}
			line = append(line, sp)
			line = append(line, w...)
			lw += 1 + ww
			continue
		}
		if lw > 0 {
			emit()
		}
		if ww <= avail() {
			line = append(line, w...)
			lw = ww
			continue
		}
		// hard-break a word longer than the line
		for _, p := range w {
			for _, r := range p.t {
				rw := runewidth.RuneWidth(r)
				if lw+rw > avail() && lw > 0 {
					emit()
				}
				line = append(line, piece{string(r), p.st, p.link})
				lw += rw
			}
		}
	}
	if len(line) > 0 || len(out) == 0 {
		emit()
	}
	return out
}
