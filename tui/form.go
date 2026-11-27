package tui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jhuggett/rux/tui/uirender"
)

// formState answers an interactive rux ui block (Choice, Select, TextInput,
// Toggle, Button): the spec is rendered live with the focused element
// highlighted, and a button press POSTs /api/threads/{id}/ui/{ui}/answer.
type formState struct {
	uiID    string
	spec    *uirender.Spec
	state   map[string]any
	fields  []uirender.Field
	buttons []uirender.Button
	cur     int // index over fields then buttons
	opt     int // option cursor for multiple choice
	edit    lineEditor
	editing string // field id being typed into
	scroll  int
}

func newForm(id string, spec *uirender.Spec, state map[string]any) *formState {
	f := &formState{uiID: id, spec: spec, state: state}
	f.refresh()
	return f
}

func (f *formState) refresh() {
	f.fields, f.buttons = uirender.Interactive(f.spec, f.state)
	f.cur = min(f.cur, max(len(f.fields)+len(f.buttons)-1, 0))
}

func (f *formState) focusID() string {
	if f.cur < len(f.fields) {
		return f.fields[f.cur].ID
	}
	if b := f.cur - len(f.fields); b < len(f.buttons) {
		return f.buttons[b].ID
	}
	return ""
}

func optIndex(opts []uirender.Option, v any) int {
	for i, o := range opts {
		if fmt.Sprint(o.Value) == fmt.Sprint(v) {
			return i
		}
	}
	return -1
}

func (tv *threadView) formKey(m *model, k tea.KeyMsg) tea.Cmd {
	f := tv.form
	n := len(f.fields) + len(f.buttons)
	var field *uirender.Field
	if f.cur < len(f.fields) {
		field = &f.fields[f.cur]
	}
	if field != nil && field.Type == "TextInput" && field.Path != "" {
		if f.editing != field.ID {
			f.editing = field.ID
			f.edit.clear()
			f.edit.insert([]rune(fmt.Sprint(orEmpty(uirender.GetState(f.state, field.Path)))))
		}
		switch k.String() {
		case "up", "down", "esc", "enter", "shift+tab":
		default:
			if f.edit.key(k, field.Multiline) {
				uirender.SetState(f.state, field.Path, f.edit.String())
				return nil
			}
		}
	}
	switch k.String() {
	case "esc":
		tv.form = nil
		return nil
	case "up", "shift+tab", "k":
		f.cur = (f.cur - 1 + n) % max(n, 1)
		f.opt, f.editing = 0, ""
	case "down", "j":
		f.cur = (f.cur + 1) % max(n, 1)
		f.opt, f.editing = 0, ""
	case "left", "right", "h", "l", " ":
		if field == nil || field.Path == "" {
			return nil
		}
		d := 1
		if s := k.String(); s == "left" || s == "h" {
			d = -1
		}
		switch field.Type {
		case "Toggle":
			v, _ := uirender.GetState(f.state, field.Path).(bool)
			uirender.SetState(f.state, field.Path, !v)
		case "Choice", "Select":
			if len(field.Options) == 0 {
				return nil
			}
			if field.Multiple {
				if k.String() == " " {
					cur, _ := uirender.GetState(f.state, field.Path).([]any)
					val := field.Options[f.opt].Value
					var next []any
					found := false
					for _, c := range cur {
						if fmt.Sprint(c) == fmt.Sprint(val) {
							found = true
							continue
						}
						next = append(next, c)
					}
					if !found {
						next = append(next, val)
					}
					uirender.SetState(f.state, field.Path, next)
				} else {
					f.opt = (f.opt + d + len(field.Options)) % len(field.Options)
				}
			} else {
				i := optIndex(field.Options, uirender.GetState(f.state, field.Path))
				i = (i + d + len(field.Options)) % len(field.Options)
				uirender.SetState(f.state, field.Path, field.Options[i].Value)
			}
		}
	case "enter":
		if field != nil {
			f.cur = (f.cur + 1) % max(n, 1)
			f.editing = ""
			return nil
		}
		b := f.buttons[f.cur-len(f.fields)]
		return tv.press(m, b)
	}
	f.refresh()
	return nil
}

func orEmpty(v any) any {
	if v == nil {
		return ""
	}
	return v
}

func (tv *threadView) press(m *model, b uirender.Button) tea.Cmd {
	f := tv.form
	acts := b.Actions
	if len(acts) == 0 {
		acts = []uirender.Action{{Name: "press", Params: map[string]any{"button": b.Label}}}
	}
	var send []uirender.Action
	for _, a := range acts {
		if a.Name == "setState" {
			if p, ok := a.Params["statePath"].(string); ok {
				uirender.SetState(f.state, p, a.Params["value"])
			}
			continue
		}
		send = append(send, a)
	}
	tv.uiState[f.uiID] = uirender.CloneState(f.state)
	tv.dirty = true
	if len(send) == 0 {
		f.refresh()
		return nil
	}
	tv.form = nil
	tv.uiAnswered[f.uiID] = send[0].Name
	id, ui, state := tv.id, f.uiID, uirender.CloneState(f.state)
	return m.do("answered: "+send[0].Name, func(ctx context.Context) error {
		for _, a := range send {
			if err := m.src.AnswerUI(ctx, id, ui, a.Name, a.Params, state); err != nil {
				return err
			}
		}
		return nil
	})
}

func (tv *threadView) viewForm(m *model, c *canvas) {
	f := tv.form
	w := min(m.w-8, 90)
	body := uirender.Render(f.spec, uirender.Options{Width: w - 4, Color: true, State: f.state, Focus: f.focusID(),
		Image: func(src string, iw int) string { return tv.images.block(src, min(iw, 60), 12) }})
	lines := strings.Split(body, "\n")
	var status string
	if f.cur < len(f.fields) {
		fl := f.fields[f.cur]
		status = sPale.Render("▸ "+cmpOr(fl.Label, fl.Type)) + sFaint.Render(" · "+map[string]string{
			"Choice": cond(fl.Multiple, "←→ move, space toggles", "←→ choose"), "Select": "←→ choose", "Toggle": "space flips", "TextInput": "type to edit",
		}[fl.Type])
		if fl.Multiple && len(fl.Options) > 0 {
			status += sWhite.Render("  [" + fl.Options[min(f.opt, len(fl.Options)-1)].Label + "]")
		}
	} else if len(f.buttons) > 0 {
		status = sPale.Render("▸ button "+f.buttons[f.cur-len(f.fields)].Label) + sFaint.Render(" · enter presses it")
	}
	maxBody := m.h - 12
	if len(lines) > maxBody {
		lines = lines[:maxBody]
	}
	content := append([]string{""}, prefixAll(lines, " ")...)
	content = append(content, "", " "+status)
	h := len(content) + 2
	x, y := (m.w-w)/2, max((m.h-h)/2, 2)
	c.fill(x-1, y-1, w+2, h+2)
	c.blit(x, y, box(w, h, sBright, sWhite.Render("answer")+sFaint.Render(" · "+f.uiID), content), 0)
}
