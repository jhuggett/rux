package uirender

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Element is one node of a spec: a component type, its (unresolved) props,
// child element ids, an optional visibility condition and event actions.
type Element struct {
	Type     string
	Props    map[string]any
	Children []string
	Visible  any            // condition, nil = visible
	On       map[string]any // e.g. {"press": ACTION}
}

// Spec is the flat json-render form: a root id, elements by id, and state.
type Spec struct {
	Root     string
	Elements map[string]*Element
	State    map[string]any
}

// Parse accepts the flat form {"root", "elements", "state"} and the nested
// form {"type", "props", "children": [{...}, ...]} (ids e1, e2, ... in
// pre-order, the same ids the server's normalizeSpec assigns). A wrapper
// {"spec": {...}} (as in rux_ui events) is unwrapped. In the flat form a child
// may also be an inline element object. Elements referenced but missing are
// not an error here; Render shows them as error lines.
func Parse(raw []byte) (*Spec, error) {
	var top any
	if err := json.Unmarshal(raw, &top); err != nil {
		return nil, fmt.Errorf("spec isn't valid JSON: %v", err)
	}
	m, ok := top.(map[string]any)
	if !ok {
		return nil, errors.New("spec isn't a JSON object")
	}
	if inner, ok := m["spec"].(map[string]any); ok && m["root"] == nil && m["type"] == nil && m["elements"] == nil {
		m = inner
	}
	p := &parser{spec: &Spec{Elements: map[string]*Element{}}, used: map[string]bool{}}
	if st, ok := m["state"].(map[string]any); ok {
		p.spec.State = st
	} else {
		p.spec.State = map[string]any{}
	}
	root, _ := m["root"].(string)
	if els, ok := m["elements"].(map[string]any); ok {
		ids := make([]string, 0, len(els))
		for id := range els {
			ids = append(ids, id)
			p.used[id] = true
		}
		sort.Strings(ids)
		for _, id := range ids {
			p.spec.Elements[id] = p.element(els[id])
		}
		if root == "" && len(ids) == 1 {
			root = ids[0]
		}
		if root == "" {
			return nil, errors.New(`spec needs "root" naming one of its "elements"`)
		}
		p.spec.Root = root
		return p.spec, nil
	}
	if t, ok := m["type"].(string); ok && t != "" {
		p.spec.Root = p.add(m)
		return p.spec, nil
	}
	return nil, errors.New(`spec needs {"root", "elements"} or a nested {"type", "props", "children"}`)
}

type parser struct {
	spec *Spec
	used map[string]bool
	n    int
}

// add registers an inline element object under a fresh id.
func (p *parser) add(m map[string]any) string {
	var id string
	for {
		p.n++
		id = "e" + strconv.Itoa(p.n)
		if !p.used[id] {
			break
		}
	}
	p.used[id] = true

	p.spec.Elements[id] = p.element(m)
	return id
}

func (p *parser) element(v any) *Element {
	m, _ := v.(map[string]any)
	el := &Element{}
	if m == nil {
		el.Type = "?"
		return el
	}
	el.Type = str(m["type"])
	el.Props, _ = m["props"].(map[string]any)
	switch ch := m["children"].(type) {
	case []any:
		for _, k := range ch {
			switch k := k.(type) {
			case string:
				el.Children = append(el.Children, k)
			case map[string]any:
				el.Children = append(el.Children, p.add(k))
			}
		}
	case string:
		el.Children = []string{ch}
	}
	el.Visible = m["visible"]
	el.On, _ = m["on"].(map[string]any)
	return el
}

// ---- JSON pointer state ----

// ptrKeys splits a JSON pointer like web/ui.js does: "/a/b" → [a b]; a
// string not starting with "/" yields no keys (the whole state).
func ptrKeys(ptr string) []string {
	parts := strings.Split(ptr, "/")
	if len(parts) <= 1 {
		return nil
	}
	out := parts[1:]
	for i, k := range out {
		out[i] = strings.ReplaceAll(strings.ReplaceAll(k, "~1", "/"), "~0", "~")
	}
	return out
}

// GetState reads a JSON pointer ("/a/b", "/list/0") from state; nil if absent.
func GetState(state map[string]any, ptr string) any {
	var o any = state
	if state == nil {
		return nil
	}
	for _, k := range ptrKeys(ptr) {
		switch t := o.(type) {
		case map[string]any:
			o = t[k]
		case []any:
			i, err := strconv.Atoi(k)
			if err != nil || i < 0 || i >= len(t) {
				return nil
			}
			o = t[i]
		default:
			return nil
		}
	}
	return o
}

// SetState writes v at a JSON pointer, creating intermediate maps (and
// replacing non-container values on the way). An index equal to a list's
// length (or "-") appends. An empty pointer is a no-op.
func SetState(state map[string]any, ptr string, v any) {
	keys := ptrKeys(ptr)
	if state == nil || len(keys) == 0 {
		return
	}
	setIn(state, keys, v)
}

func setIn(cur any, keys []string, v any) any {
	if len(keys) == 0 {
		return v
	}
	k, rest := keys[0], keys[1:]
	switch c := cur.(type) {
	case map[string]any:
		c[k] = setIn(c[k], rest, v)
		return c
	case []any:
		if k == "-" {
			return append(c, setIn(nil, rest, v))
		}
		i, err := strconv.Atoi(k)
		if err != nil || i < 0 || i > len(c) {
			return c
		}
		if i == len(c) {
			return append(c, setIn(nil, rest, v))
		}
		c[i] = setIn(c[i], rest, v)
		return c
	}
	return map[string]any{k: setIn(nil, rest, v)}
}

// CloneState deep-copies maps and lists (other values are shared, which is
// fine for JSON values).
func CloneState(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	return cloneAny(m).(map[string]any)
}

func cloneAny(v any) any {
	switch t := v.(type) {
	case map[string]any:
		o := make(map[string]any, len(t))
		for k, x := range t {
			o[k] = cloneAny(x)
		}
		return o
	case []any:
		o := make([]any, len(t))
		for i, x := range t {
			o[i] = cloneAny(x)
		}
		return o
	}
	return v
}

// ---- expressions ----

var templateRE = regexp.MustCompile(`\$\{(/[^}]*)\}`)

// Resolve evaluates expressions ($state, $bindState, $template, $cond)
// anywhere in v against the context's state, like web/ui.js.
func (c *Ctx) Resolve(v any) any {
	return resolveIn(c.State, v, 0)
}

func resolveIn(state map[string]any, v any, depth int) any {
	if depth > 64 {
		return nil
	}
	switch t := v.(type) {
	case []any:
		o := make([]any, len(t))
		for i, x := range t {
			o[i] = resolveIn(state, x, depth+1)
		}
		return o
	case map[string]any:
		if s, ok := t["$state"]; ok && len(t) == 1 {
			return GetState(state, str(s))
		}
		if s, ok := t["$bindState"]; ok {
			return GetState(state, str(s))
		}
		if s, ok := t["$template"]; ok {
			return templateRE.ReplaceAllStringFunc(str(s), func(m string) string {
				return str(GetState(state, m[2:len(m)-1]))
			})
		}
		if cnd, ok := t["$cond"]; ok {
			if condIn(state, cnd, depth+1) {
				return resolveIn(state, t["$then"], depth+1)
			}
			return resolveIn(state, t["$else"], depth+1)
		}
		o := make(map[string]any, len(t))
		for k, x := range t {
			o[k] = resolveIn(state, x, depth+1)
		}
		return o
	}
	return v
}

// Cond evaluates a visibility condition: {"$state": PTR} (truthy) with
// optional "eq"/"neq", {"not": COND}, a list (all), or a literal.
func (c *Ctx) Cond(v any) bool { return condIn(c.State, v, 0) }

func condIn(state map[string]any, v any, depth int) bool {
	if depth > 64 {
		return false
	}
	switch t := v.(type) {
	case nil:
		return true
	case bool:
		return t
	case []any:
		for _, x := range t {
			if !condIn(state, x, depth+1) {
				return false
			}
		}
		return true
	case map[string]any:
		if n, ok := t["not"]; ok {
			return !condIn(state, n, depth+1)
		}
		if p, ok := t["$state"]; ok {
			sv := GetState(state, str(p))
			if eq, ok := t["eq"]; ok {
				return jsonEq(sv, eq)
			}
			if neq, ok := t["neq"]; ok {
				return !jsonEq(sv, neq)
			}
			return truthy(sv)
		}
		return true
	}
	return truthy(v)
}

func jsonEq(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

// truthy is JavaScript truthiness (an empty list counts as false, like the
// web renderer's $state conditions).
func truthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case float64:
		return t != 0 && !math.IsNaN(t)
	case int:
		return t != 0
	case string:
		return t != ""
	case []any:
		return len(t) > 0
	}
	return true
}

// ---- value helpers ----

// str converts a prop value to display text like web/ui.js str(): nil → "",
// objects → compact JSON, numbers like JavaScript. Control characters
// (including ESC, so specs can't inject terminal escapes) are removed and
// tabs become spaces.
func str(v any) string {
	var s string
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		s = t
	case float64:
		s = fmtJSNum(t)
	case int:
		s = strconv.Itoa(t)
	case bool:
		s = strconv.FormatBool(t)
	case json.Number:
		s = t.String()
	default:
		b, err := json.Marshal(t)
		if err != nil {
			s = fmt.Sprint(t)
		} else {
			s = string(b)
		}
	}
	return clean(s)
}

func fmtJSNum(f float64) string {
	if math.IsNaN(f) {
		return "NaN"
	}
	if math.IsInf(f, 0) {
		if f > 0 {
			return "Infinity"
		}
		return "-Infinity"
	}
	if f == math.Trunc(f) && math.Abs(f) < 1e21 {
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}

// clean drops control characters other than newline and turns tabs into
// four spaces.
func clean(s string) string {
	ok := true
	for _, r := range s {
		if r < 0x20 && r != '\n' || r == 0x7f || (r >= 0x80 && r < 0xa0) || r == '\r' {
			ok = false
			break
		}
	}
	if ok {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\t':
			b.WriteString("    ")
		case r == '\n':
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0):
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// num converts a prop to a number (d if it isn't one), like Number(v).
func num(v any, d float64) float64 {
	switch t := v.(type) {
	case float64:
		if math.IsNaN(t) || math.IsInf(t, 0) {
			return d
		}
		return t
	case int:
		return float64(t)
	case json.Number:
		if f, err := t.Float64(); err == nil {
			return f
		}
	case string:
		if f, err := strconv.ParseFloat(strings.TrimSpace(t), 64); err == nil && !math.IsNaN(f) && !math.IsInf(f, 0) {
			return f
		}
	case bool:
		if t {
			return 1
		}
		return 0
	}
	return d
}

func list(v any) []any {
	l, _ := v.([]any)
	return l
}

func obj(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

// firstOf returns the first non-nil prop among keys.
func firstOf(m map[string]any, keys ...string) any {
	for _, k := range keys {
		if v, ok := m[k]; ok && v != nil {
			return v
		}
	}
	return nil
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
