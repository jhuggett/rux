package uirender

import (
	"strings"
)

// Option is one choice of a Choice or Select.
type Option struct {
	Value              any
	Label, Description string
}

// Field is an input the TUI can edit.
type Field struct {
	ID, Type, Label, Placeholder string // Type: Choice|Select|TextInput|Toggle
	Path                         string // JSON pointer from {"$bindState": "/x"} on "value" ("" if unbound)
	Options                      []Option
	Multiple, Multiline          bool
	Value                        any      // current (resolved) value
	Change                       []Action // the element's on.change actions
}

// Action is one action of an "on" handler: setState, submit or any other name.
type Action struct {
	Name   string
	Params map[string]any
}

// Button is a pressable button and what pressing it does.
type Button struct {
	ID, Label, Variant string
	Actions            []Action
}

func parseOptions(v any) []Option {
	var out []Option
	for _, o := range list(v) {
		if m := obj(o); m != nil {
			val := m["value"]
			lbl := str(firstOf(m, "label", "value"))
			out = append(out, Option{Value: val, Label: lbl, Description: str(m["description"])})
		} else {
			out = append(out, Option{Value: o, Label: str(o)})
		}
	}
	return out
}

// bindPath is the $bindState pointer on an element's raw "value" prop.
func bindPath(el *Element) string {
	if m := obj(el.Props["value"]); m != nil {
		if p, ok := m["$bindState"].(string); ok {
			return p
		}
	}
	return ""
}

// parseActions reads an ACTION or a list of them, resolving params against
// state.
func parseActions(v any, state map[string]any) []Action {
	var out []Action
	switch t := v.(type) {
	case []any:
		for _, x := range t {
			out = append(out, parseActions(x, state)...)
		}
	case map[string]any:
		name := str(t["action"])
		if name == "" {
			name = "submit"
		}
		params, _ := resolveIn(state, t["params"], 0).(map[string]any)
		out = append(out, Action{Name: name, Params: params})
	case string:
		if t != "" {
			out = append(out, Action{Name: t})
		}
	}
	return out
}

// Interactive lists the visible inputs and buttons in document order (all
// tabs included). state nil means spec.State.
func Interactive(spec *Spec, state map[string]any) ([]Field, []Button) {
	if spec == nil {
		return nil, nil
	}
	if state == nil {
		state = spec.State
	}
	c := newCtx(spec, Options{State: state})
	var fields []Field
	var buttons []Button
	var walk func(id string, depth int)
	walk = func(id string, depth int) {
		el := spec.Elements[id]
		if el == nil || depth > maxDepth {
			return
		}
		if el.Visible != nil && !c.Cond(el.Visible) {
			return
		}
		p, _ := c.Resolve(map[string]any(el.Props)).(map[string]any)
		switch el.Type {
		case "Choice", "Select", "TextInput", "Toggle":
			f := Field{ID: id, Type: el.Type, Label: str(p["label"]), Placeholder: str(p["placeholder"]),
				Path: bindPath(el), Value: p["value"], Change: parseActions(el.On["change"], state)}
			if el.Type == "Choice" || el.Type == "Select" {
				f.Options = parseOptions(p["options"])
			}
			f.Multiple = el.Type == "Choice" && truthy(p["multiple"])
			f.Multiline = el.Type == "TextInput" && truthy(p["multiline"])
			fields = append(fields, f)
		case "Button":
			b := Button{ID: id, Label: str(p["label"]), Variant: str(p["variant"])}
			if b.Label == "" {
				b.Label = "ok"
			}
			b.Actions = parseActions(el.On["press"], state)
			if len(b.Actions) == 0 && p["action"] != nil {
				b.Actions = parseActions(map[string]any{"action": p["action"], "params": el.Props["params"]}, state)
			}
			if len(b.Actions) == 0 {
				b.Actions = []Action{{Name: "submit"}}
			}
			buttons = append(buttons, b)
		}
		for _, k := range el.Children {
			walk(k, depth+1)
		}
	}
	walk(spec.Root, 0)
	return fields, buttons
}

// ---- rendering ----

// field renders an input with its label; the focused field gets a bright ▌
// gutter (taking 2 columns).
func (c *Ctx) field(id, label string, width int, body func(w int) []string) string {
	focused := id != "" && id == c.Opts.Focus
	w := width
	if focused && width > 6 {
		w = width - 2
	}
	var out []string
	if label != "" {
		st := sMuted
		if focused {
			st = sFocus
		}
		out = append(out, c.paintWrapped(st, label, w)...)
	}
	out = append(out, body(w)...)
	if focused && width > 6 {
		g := c.fg(cBright, "▌") + " "
		for i := range out {
			out[i] = g + out[i]
		}
	}
	return strings.Join(out, "\n")
}

func renderChoice(c *Ctx, id string, el *Element, p map[string]any, width int) string {
	opts := parseOptions(p["options"])
	multiple := truthy(p["multiple"])
	val := p["value"]
	isOn := func(v any) bool {
		if multiple {
			for _, x := range list(val) {
				if jsonEq(x, v) {
					return true
				}
			}
			return false
		}
		return val != nil && jsonEq(val, v)
	}
	return c.field(id, str(p["label"]), width, func(w int) []string {
		marks := func(o Option) (string, Style) {
			on := isOn(o.Value)
			m := "( )"
			if multiple {
				m = "[ ]"
				if on {
					m = "[x]"
				}
			} else if on {
				m = "(•)"
			}
			if on {
				return m, Style{FG: cBright, Bold: true}
			}
			return m, sText
		}
		// inline when there are no descriptions and it fits
		inline, total := true, 0
		for i, o := range opts {
			if o.Description != "" {
				inline = false
			}
			total += 4 + sw(o.Label)
			if i > 0 {
				total += 2
			}
		}
		if inline && total <= w {
			var b strings.Builder
			for i, o := range opts {
				if i > 0 {
					b.WriteString("  ")
				}
				m, st := marks(o)
				mst := Style{FG: cFaint}
				if st.Bold {
					mst = Style{FG: cBright}
				}
				b.WriteString(c.Paint(mst, m) + " " + c.Paint(st, o.Label))
			}
			return []string{b.String()}
		}
		var out []string
		for _, o := range opts {
			m, st := marks(o)
			mst := Style{FG: cFaint}
			if st.Bold {
				mst = Style{FG: cBright}
			}
			for i, l := range wrapPlain(o.Label, max(1, w-4)) {
				pre := "    "
				if i == 0 {
					pre = c.Paint(mst, m) + " "
				}
				out = append(out, pre+c.Paint(st, l))
			}
			if o.Description != "" {
				for _, l := range wrapPlain(o.Description, max(1, w-4)) {
					out = append(out, "    "+c.Paint(sMuted, l))
				}
			}
		}
		return out
	})
}

func renderSelect(c *Ctx, id string, el *Element, p map[string]any, width int) string {
	opts := parseOptions(p["options"])
	return c.field(id, str(p["label"]), width, func(w int) []string {
		var cur *Option
		for i := range opts {
			if p["value"] != nil && jsonEq(opts[i].Value, p["value"]) {
				cur = &opts[i]
				break
			}
		}
		count := c.Paint(sFaint, " ("+itoa(len(opts))+")")
		if sw(count)+4 > w {
			count = ""
		}
		avail := max(1, w-2-sw(count))
		if cur == nil {
			ph := str(p["placeholder"])
			if ph == "" {
				ph = "choose…"
			}
			return []string{c.fg(cMid, "▾") + " " + c.Paint(sFaint, trunc(ph, avail)) + count}
		}
		return []string{c.fg(cMid, "▾") + " " + c.Paint(Style{FG: cBright, Bold: true}, trunc(cur.Label, avail)) + count}
	})
}

func renderTextInput(c *Ctx, id string, el *Element, p map[string]any, width int) string {
	focused := id == c.Opts.Focus
	return c.field(id, str(p["label"]), width, func(w int) []string {
		v := str(p["value"])
		multi := truthy(p["multiline"])
		rows := 1
		if multi {
			rows = clampInt(int(num(p["rows"], 4)), 1, 20)
		}
		inner := innerWidth(w)
		var body []string
		if v == "" {
			ph := str(p["placeholder"])
			if multi {
				body = c.paintWrapped(sFaint, ph, inner)
			} else {
				body = []string{c.Paint(sFaint, trunc(ph, inner))}
			}
		} else if multi {
			body = c.paintWrapped(sText, v, inner)
		} else {
			one := strings.ReplaceAll(v, "\n", " ")
			if sw(one) > inner { // show the end, where typing happens
				one = "…" + string([]rune(one)[len([]rune(one))-max(1, inner-1):])
				one = trunc(one, inner)
			}
			body = []string{c.Paint(sText, one)}
		}
		for len(body) < rows {
			body = append(body, "")
		}
		if multi && len(body) > max(rows, 12) {
			body = append(body[:max(rows, 12)-1], c.Paint(sFaint, "…"))
		}
		border := sBorder
		if focused {
			border = Style{FG: cBright}
		}
		if w < 8 {
			for i, l := range body {
				body[i] = c.fg(cMid, "›") + l
			}
			return body
		}
		return strings.Split(c.box("", Style{}, "", border, body, w), "\n")
	})
}

func renderToggle(c *Ctx, id string, el *Element, p map[string]any, width int) string {
	return c.field(id, "", width, func(w int) []string {
		sw := c.Paint(sMuted, "[off]")
		if truthy(p["value"]) {
			sw = c.Paint(Style{FG: cBright, Bold: true}, "[on ]")
		}
		line := sw
		if l := str(p["label"]); l != "" {
			line += " " + c.Paint(sText, l)
		}
		return []string{line}
	})
}

func renderButton(c *Ctx, id string, el *Element, p map[string]any, width int) string {
	label := str(p["label"])
	if label == "" {
		label = "ok"
	}
	focused := id == c.Opts.Focus
	l, r := "[ ", " ]"
	if focused {
		l, r = "[▸", "◂]"
	}
	text := l + trunc(label, max(1, width-4)) + r
	var st Style
	switch str(p["variant"]) {
	case "primary":
		st = Style{FG: cBlack, BG: cBright, Bold: true}
		if focused {
			st.BG = cWhite
		}
	case "danger":
		st = Style{FG: cErr, Bold: focused}
		if focused {
			st = Style{FG: cBlack, BG: cErr, Bold: true}
		}
	default:
		st = Style{FG: cMid}
		if focused {
			st = Style{FG: cBlack, BG: cMid, Bold: true}
		}
	}
	return c.Paint(st, text)
}
