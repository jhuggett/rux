package uirender

import (
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
)

// codeStyle is the green-ish syntax theme: keywords bright green bold,
// strings pale green, comments grey-green italic, numbers white, functions
// light green, everything else a soft green-grey.
func codeStyle(t chroma.TokenType) Style {
	switch {
	case t.InCategory(chroma.Comment):
		return Style{FG: 65, Italic: true}
	case t.InCategory(chroma.Keyword):
		return Style{FG: cBright, Bold: true}
	case t.InSubCategory(chroma.LiteralString):
		return Style{FG: cText}
	case t.InSubCategory(chroma.LiteralNumber):
		return Style{FG: cWhite}
	case t == chroma.NameFunction || t == chroma.NameFunctionMagic || t == chroma.NameClass:
		return Style{FG: cText2}
	case t.InSubCategory(chroma.NameBuiltin) || t == chroma.NameTag:
		return Style{FG: cBright2}
	case t == chroma.NameAttribute || t == chroma.NameDecorator:
		return Style{FG: cMid}
	case t.InCategory(chroma.Operator) || t.InCategory(chroma.Punctuation):
		return Style{FG: 108}
	case t == chroma.GenericInserted:
		return Style{FG: cBright}
	case t == chroma.GenericDeleted:
		return Style{FG: cErr}
	}
	return Style{FG: cCode}
}

func langOf(name string) string {
	ext := strings.TrimPrefix(path.Ext(name), ".")
	if ext == "" {
		switch path.Base(name) {
		case "Makefile":
			return "make"
		case "Dockerfile":
			return "docker"
		}
	}
	return strings.ToLower(ext)
}

func lexerFor(lang, filename string) chroma.Lexer {
	var l chroma.Lexer
	if lang != "" {
		l = lexers.Get(lang)
	}
	if l == nil && filename != "" {
		l = lexers.Match(path.Base(filename))
	}
	return l
}

// highlightLines splits code into lines of styled text (plain when color is
// off or there is no lexer).
func (c *Ctx) highlightLines(code, lang, filename string) []string {
	src := strings.Split(code, "\n")
	if !c.Opts.Color {
		return src
	}
	l := lexerFor(lang, filename)
	if l == nil {
		out := make([]string, len(src))
		for i, s := range src {
			out[i] = c.Paint(Style{FG: cCode}, s)
		}
		return out
	}
	it, err := chroma.Coalesce(l).Tokenise(nil, code)
	if err != nil {
		return src
	}
	var out []string
	var cur strings.Builder
	for tok := it(); tok != chroma.EOF; tok = it() {
		parts := strings.Split(tok.Value, "\n")
		for i, part := range parts {
			if i > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
			if part != "" {
				cur.WriteString(c.Paint(codeStyle(tok.Type), part))
			}
		}
	}
	out = append(out, cur.String())
	for len(out) < len(src) {
		out = append(out, "")
	}
	return out[:len(src)]
}

func renderCode(c *Ctx, id string, el *Element, p map[string]any, width int) string {
	code := strings.TrimRight(str(p["code"]), "\n")
	filename := str(p["filename"])
	lang := strings.ToLower(str(p["language"]))
	if lang == "" {
		lang = langOf(filename)
	}
	start := int(num(p["startLine"], 1))
	hl := map[int]bool{}
	for _, h := range list(p["highlight"]) {
		hl[int(num(h, -1))] = true
	}
	lines := c.highlightLines(code, lang, filename)
	nw := len(strconv.Itoa(start + len(lines) - 1))
	inner := innerWidth(width)
	var body []string
	for i, l := range lines {
		n := start + i
		mark := " "
		if hl[n] {
			mark = c.fg(cWarn, "▌")
		}
		gutter := mark + c.Paint(sFaint, padLeft(strconv.Itoa(n), nw)+" │ ")
		body = append(body, gutter+trunc(l, max(1, inner-nw-4)))
	}
	title := filename
	right := ""
	if title == "" {
		title = lang
	} else {
		right = lang
	}
	if title == "" {
		title = "code"
	}
	return c.box(title, sTitle, c.Paint(sMuted, right), Style{FG: cDim2}, body, width)
}

// ---- Diff ----

type diffLine struct {
	t    byte // '+', '-', ' '
	o, n int
	text string
}

type diffHunk struct {
	head  string
	lines []diffLine
}

type diffFile struct {
	from, to, status string
	hunks            []*diffHunk
	add, del         int
	binary           bool
}

var (
	diffGitRE  = regexp.MustCompile(`^diff --git a/(.*?) b/(.*)$`)
	diffHunkRE = regexp.MustCompile(`^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@(.*)$`)
)

// parseDiff parses a unified (git) diff into files, like web/ui.js. A patch
// without "diff --git" headers (plain diff -u) is accepted too.
func parseDiff(text string) []*diffFile {
	var files []*diffFile
	var f *diffFile
	var h *diffHunk
	oldN, newN := 0, 0
	all := strings.Split(text, "\n")
	git := strings.HasPrefix(text, "diff --git ") || strings.Contains(text, "\ndiff --git ")
	for i, line := range all {
		line = strings.TrimSuffix(line, "\r")
		if !git && strings.HasPrefix(line, "--- ") && i+1 < len(all) && strings.HasPrefix(all[i+1], "+++ ") {
			f = &diffFile{status: "modified"} // plain diff -u: a new file
			files = append(files, f)
			h = nil
		}
		if strings.HasPrefix(line, "diff --git ") {
			f = &diffFile{status: "modified"}
			if m := diffGitRE.FindStringSubmatch(line); m != nil {
				f.from, f.to = m[1], m[2]
			} else {
				f.to = line[11:]
			}
			files = append(files, f)
			h = nil
			continue
		}
		if f == nil {
			continue
		}
		switch {
		case strings.HasPrefix(line, "new file mode"):
			f.status = "added"
			continue
		case strings.HasPrefix(line, "deleted file mode"):
			f.status = "deleted"
			continue
		case strings.HasPrefix(line, "rename from "):
			f.status, f.from = "renamed", line[12:]
			continue
		case strings.HasPrefix(line, "rename to "):
			f.to = line[10:]
			continue
		case strings.HasPrefix(line, "Binary files"):
			f.binary = true
			continue
		}
		if h == nil && (strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "+++ ")) {
			p := strings.TrimPrefix(strings.TrimPrefix(line[4:], "a/"), "b/")
			if i := strings.IndexByte(p, '\t'); i >= 0 {
				p = p[:i]
			}
			if p != "/dev/null" {
				if line[0] == '+' {
					f.to = p
				} else {
					f.from = p
					if f.to == "" {
						f.to = p
					}
				}
			} else if line[0] == '+' {
				f.status = "deleted"
			} else {
				f.status = "added"
			}
			continue
		}
		if m := diffHunkRE.FindStringSubmatch(line); m != nil {
			oldN, _ = strconv.Atoi(m[1])
			newN, _ = strconv.Atoi(m[2])
			h = &diffHunk{head: line}
			f.hunks = append(f.hunks, h)
			continue
		}
		if h == nil || line == "" {
			continue
		}
		switch line[0] {
		case '+':
			h.lines = append(h.lines, diffLine{t: '+', n: newN, text: line[1:]})
			newN++
			f.add++
		case '-':
			h.lines = append(h.lines, diffLine{t: '-', o: oldN, text: line[1:]})
			oldN++
			f.del++
		case ' ':
			h.lines = append(h.lines, diffLine{t: ' ', o: oldN, n: newN, text: line[1:]})
			oldN++
			newN++
		}
	}
	return files
}

func (f *diffFile) name() string {
	if f.status == "deleted" {
		return f.from
	}
	if f.to == "" {
		return f.from
	}
	return f.to
}

const diffLineLimit = 4000

// renderDiff shows a unified diff: a box titled with the title and totals,
// then per file a header and its hunks with old/new line numbers. view
// "split" is rendered unified too (terminals are narrow).
func renderDiff(c *Ctx, id string, el *Element, p map[string]any, width int) string {
	title := str(p["title"])
	if title == "" {
		title = "diff"
	}
	border := Style{FG: cDim2}
	inner := innerWidth(width)
	msg := func(s string) string {
		return c.box(title, sTitle, c.Paint(sMuted, s), border, nil, width)
	}
	var text string
	if patch, ok := p["patch"].(string); ok {
		text = patch
	} else if src, ok := p["src"].(string); ok && src != "" {
		if c.Opts.Fetch == nil {
			return msg("can't load " + clean(src))
		}
		body, ok := c.Opts.Fetch(src)
		if !ok {
			return msg("loading…")
		}
		text = body
	} else {
		return msg("Diff needs patch or src")
	}
	files := parseDiff(text)
	if len(files) == 0 {
		return msg("no changes")
	}
	add, del := 0, 0
	nmax := 1
	for _, f := range files {
		add += f.add
		del += f.del
		for _, h := range f.hunks {
			for _, l := range h.lines {
				nmax = max(nmax, l.o, l.n)
			}
		}
	}
	plural := "s"
	if len(files) == 1 {
		plural = ""
	}
	right := c.Paint(sMuted, itoa(len(files))+" file"+plural+" ") + c.fg(cBright, "+"+itoa(add)) + " " + c.fg(cErr, "−"+itoa(del))
	nw := len(strconv.Itoa(nmax))
	var body []string
	count := 0
	for fi, f := range files {
		if fi > 0 {
			body = append(body, "")
		}
		stats := c.fg(cBright, "+"+itoa(f.add)) + " " + c.fg(cErr, "−"+itoa(f.del))
		statW := len(itoa(f.add)) + len(itoa(f.del)) + 3
		tag := ""
		switch f.status {
		case "added":
			tag = "new"
		case "deleted":
			tag = "deleted"
		case "renamed":
			tag = "renamed from " + clean(f.from)
		}
		nameW := inner - statW - 1
		name := "▸ " + clean(f.name())
		head := c.Paint(sTitle, trunc(name, max(1, nameW)))
		used := sw(trunc(name, max(1, nameW)))
		if tag != "" && nameW-used > 4 {
			t := trunc(tag, nameW-used-1)
			head += " " + c.Paint(sMuted, t)
			used += 1 + sw(t)
		}
		if inner-used-statW >= 1 {
			head += rep(" ", inner-used-statW) + stats
		}
		body = append(body, head)
		if f.binary {
			body = append(body, c.Paint(sFaint, "binary file"))
			continue
		}
		textW := max(1, inner-2*nw-4)
		for _, h := range f.hunks {
			if count > diffLineLimit {
				break
			}
			body = append(body, c.fg(cHunk, trunc(clean(h.head), inner)))
			for _, l := range h.lines {
				count++
				o, n := "", ""
				if l.t != '+' {
					o = strconv.Itoa(l.o)
				}
				if l.t != '-' {
					n = strconv.Itoa(l.n)
				}
				nums := c.Paint(sFaint, padLeft(o, nw)+" "+padLeft(n, nw)+" ")
				txt := trunc(clean(l.text), textW)
				switch l.t {
				case '+':
					body = append(body, nums+c.fg(cBright, "+ ")+c.fg(cBright2, txt))
				case '-':
					body = append(body, nums+c.fg(cErr, "− ")+c.fg(cErr, txt))
				default:
					body = append(body, nums+"  "+c.fg(cCode, txt))
				}
			}
		}
	}
	if count > diffLineLimit {
		body = append(body, c.Paint(sFaint, "diff truncated; open the raw file for the rest"))
	}
	return c.box(title, sTitle, right, border, body, width)
}

// ---- Image ----

func renderImage(c *Ctx, id string, el *Element, p map[string]any, width int) string {
	src, ok := p["src"].(string)
	if !ok || src == "" {
		return c.errLine("Image needs src", width)
	}
	src = clean(src)
	var pic string
	if c.Opts.Image != nil {
		pic = c.Opts.Image(src, width)
	} else {
		pic = c.Paint(sFaint, trunc("[image: "+src+"]", width))
	}
	out := []string{pic}
	cap := str(p["caption"])
	if cap == "" {
		cap = str(p["alt"])
	}
	if cap != "" {
		out = append(out, c.paintWrapped(sMuted, cap, width)...)
	}
	return strings.Join(out, "\n")
}
