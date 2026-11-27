package uirender

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

var update = flag.Bool("update", false, "rewrite testdata/*.golden")

func TestMain(m *testing.M) {
	time.Local = time.UTC
	os.Exit(m.Run())
}

type testCfg struct {
	Focus string         `json:"focus"`
	Tabs  map[string]int `json:"tabs"`
	State map[string]any `json:"state"`
}

func testOpts(t *testing.T, raw []byte, width int, color bool) Options {
	var meta struct {
		Test testCfg `json:"_test"`
	}
	json.Unmarshal(raw, &meta)
	return Options{
		Width: width,
		Color: color,
		Focus: meta.Test.Focus,
		Tabs:  meta.Test.Tabs,
		State: meta.Test.State,
		Now:   time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC),
		Image: func(src string, w int) string {
			return fmt.Sprintf("▛%s▜\n▌ picture %s (%d cols)\n▙%s▟", strings.Repeat("▀", 20), src, w, strings.Repeat("▄", 20))
		},
		Fetch: func(src string) (string, bool) {
			if src == "/api/threads/t/assets/pending.diff" {
				return "", false
			}
			b, err := os.ReadFile(filepath.Join("testdata", filepath.Base(src)))
			if err != nil {
				t.Fatalf("fetch %s: %v", src, err)
			}
			return string(b), true
		},
	}
}

func checkWidth(t *testing.T, name string, out string, width int, color bool) {
	t.Helper()
	for i, l := range strings.Split(out, "\n") {
		if w := ansi.StringWidth(l); w > width {
			t.Errorf("%s (width %d, color %v) line %d is %d cells: %q", name, width, color, i+1, w, l)
		}
		if !color && strings.ContainsRune(l, '\x1b') {
			t.Errorf("%s: escape code with Color=false on line %d: %q", name, i+1, l)
		}
	}
}

func TestGolden(t *testing.T) {
	files, _ := filepath.Glob("testdata/*.json")
	if len(files) == 0 {
		t.Fatal("no testdata")
	}
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".json")
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			spec, err := Parse(raw)
			if err != nil {
				t.Fatal(err)
			}
			got := Render(spec, testOpts(t, raw, 80, false)) + "\n"
			checkWidth(t, name, got, 80, false)
			golden := strings.TrimSuffix(f, ".json") + ".golden"
			if *update {
				os.WriteFile(golden, []byte(got), 0o644)
			} else {
				want, err := os.ReadFile(golden)
				if err != nil {
					t.Fatalf("%v (run go test -update)", err)
				}
				if string(want) != got {
					t.Errorf("output differs from %s (go test -update to accept):\n%s", golden, got)
				}
			}
			for _, w := range []int{80, 47, 33, 19, 12, 5, 1} {
				for _, color := range []bool{true, false} {
					out := Render(spec, testOpts(t, raw, w, color))
					checkWidth(t, name, out, w, color)
				}
			}
			if Render(spec, testOpts(t, raw, 80, true)) == got[:len(got)-1] && !strings.Contains(name, "unknown") {
				// most specs should get some styling
				t.Logf("%s: color output has no styling", name)
			}
		})
	}
}

func TestBadInput(t *testing.T) {
	specs := []string{
		`{"root": "a", "elements": {"a": {"type": "Card", "children": ["nope"]}}}`,
		`{"root": "a", "elements": {"a": {"type": "Stack", "children": ["a"]}}}`,
		`{"root": "x", "elements": {"a": {"type": "Text"}}}`,
		`{"type": "Table", "props": {"columns": 5, "rows": "x"}}`,
		`{"type": "Chart", "props": {"series": [{"data": "x"}, 3, {"data": [1, "2", null, {"y": 4}]}], "height": -5}}`,
		`{"type": "Chart", "props": {"kind": "bar", "series": [{"data": [-1, 0, 0]}], "max": 0}}`,
		`{"type": "Chart", "props": {"kind": "stackedBar", "labels": [1,2,3], "series": [{"data": [1, 2, 3]}, {"data": [3]}]}}`,
		`{"type": "Graph", "props": {"nodes": [{"id": "a"}, {"id": "b", "col": 7}, {"id": "a"}], "edges": [{"from": "a", "to": "b"}, {"from": "b", "to": "a"}, {"from": "zz", "to": "a"}]}}`,
		`{"type": "Timeline", "props": {"items": [{"label": "x", "start": "garbage"}, {"label": "y", "start": 5, "end": 1}]}}`,
		`{"type": "Grid", "props": {"columns": "lots"}, "children": [{"type": "Badge", "props": {"text": {"$state": "/nope"}}}]}`,
		`{"type": "Text", "props": {"text": "evil \u001b[31mred\u001b]8;;x\u0007 **unclosed"}}`,
		`{"type": "Diff", "props": {"patch": "@@ garbage\n+++ \n--- "}}`,
		`{"type": "Code", "props": {"code": 12, "highlight": "x", "startLine": "7"}}`,
		`{"type": "Tabs", "props": {"tabs": "x"}, "children": [{"type": "Divider"}]}`,
		`{"type": "Choice", "props": {"options": [null, {"value": {"a": 1}}], "value": {"$bindState": "/a/b/c"}}}`,
		`{"type": "Image", "props": {"src": 3}}`,
		`{"type": "Meter", "props": {"label": "x", "value": "abc", "max": 0}}`,
		`{"type": "KeyValue", "props": {"items": [1, {"key": "a"}]}}`,
		`{"type": "List", "props": {"items": [null, 3, {"title": {"x": 1}, "status": "weird"}]}}`,
		`{"type": "Stack", "props": {"direction": "horizontal", "wrap": true}, "children": [{"type": "Text", "props": {"text": "a"}}, {"type": "Text", "props": {"text": "b"}}]}`,
	}
	for _, s := range specs {
		spec, err := Parse([]byte(s))
		if err != nil {
			t.Fatalf("%s: %v", s, err)
		}
		for _, w := range []int{80, 20, 3} {
			for _, color := range []bool{false, true} {
				out := Render(spec, Options{Width: w, Color: color})
				checkWidth(t, s, out, w, color)
				if strings.Contains(out, "\x1b[31m") || strings.Contains(out, "\x07") {
					t.Errorf("injected escape survived: %q", out)
				}
			}
		}
	}
	// a cycle hits the depth limit instead of recursing forever
	spec, _ := Parse([]byte(specs[1]))
	if out := Render(spec, Options{Width: 40}); !strings.Contains(out, "nested too deeply") {
		t.Errorf("cycle: %q", out)
	}
	if out := Render(nil, Options{Width: 40}); !strings.Contains(out, "bad spec") {
		t.Errorf("nil spec: %q", out)
	}
	for _, bad := range []string{`[]`, `{}`, `nope`, `{"elements": {"a": {}, "b": {}}}`} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("Parse(%s) should fail", bad)
		}
	}
}

func TestPanicRecovery(t *testing.T) {
	Registry["Boom"] = func(c *Ctx, id string, el *Element, props map[string]any, width int) string { panic("kaboom") }
	defer delete(Registry, "Boom")
	spec, _ := Parse([]byte(`{"type": "Stack", "children": [{"type": "Boom"}, {"type": "Text", "props": {"text": "after"}}]}`))
	out := Render(spec, Options{Width: 40})
	if !strings.Contains(out, "Boom: kaboom") || !strings.Contains(out, "after") {
		t.Errorf("got %q", out)
	}
}

func TestParseNested(t *testing.T) {
	spec, err := Parse([]byte(`{"type": "Card", "state": {"x": 1}, "children": [{"type": "Text", "props": {"text": "a"}}, {"type": "Stack", "children": [{"type": "Badge"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if spec.Root != "e1" || spec.Elements["e1"].Type != "Card" || !reflect.DeepEqual(spec.Elements["e1"].Children, []string{"e2", "e3"}) ||
		spec.Elements["e3"].Children[0] != "e4" || spec.State["x"] != 1.0 {
		t.Errorf("bad nested parse: %+v", spec)
	}
	// rux_ui event wrapper
	spec, err = Parse([]byte(`{"spec": {"root": "a", "elements": {"a": {"type": "Divider"}}}}`))
	if err != nil || spec.Root != "a" {
		t.Errorf("wrapper: %v %+v", err, spec)
	}
}

func TestState(t *testing.T) {
	st := map[string]any{"a": map[string]any{"b": 1.0}, "l": []any{"x", "y"}}
	if GetState(st, "/a/b") != 1.0 || GetState(st, "/l/1") != "y" || GetState(st, "/a/zz/q") != nil || GetState(st, "/l/9") != nil {
		t.Error("GetState")
	}
	if !reflect.DeepEqual(GetState(st, ""), st) {
		t.Error("empty pointer should return the whole state")
	}
	SetState(st, "/new/deep/x", "v")
	if GetState(st, "/new/deep/x") != "v" {
		t.Errorf("SetState deep: %v", st)
	}
	SetState(st, "/a/b/c", true) // replaces a non-container
	if GetState(st, "/a/b/c") != true {
		t.Errorf("SetState replace: %v", st)
	}
	SetState(st, "/l/0", "X")
	SetState(st, "/l/2", "Z")
	SetState(st, "/l/-", "W")
	if !reflect.DeepEqual(st["l"], []any{"X", "y", "Z", "W"}) {
		t.Errorf("SetState list: %v", st["l"])
	}
	SetState(st, "/a~1b", 1.0)
	if st["a/b"] != 1.0 || GetState(st, "/a~1b") != 1.0 {
		t.Error("pointer escapes")
	}
	SetState(st, "", 5) // no-op
	cl := CloneState(st)
	SetState(cl, "/new/deep/x", "changed")
	cl["l"].([]any)[0] = "changed"
	if GetState(st, "/new/deep/x") != "v" || GetState(st, "/l/0") != "X" {
		t.Error("CloneState isn't deep")
	}
}

func TestResolve(t *testing.T) {
	c := newCtx(&Spec{}, Options{State: map[string]any{"name": "Ada", "n": 2.0, "on": true, "list": []any{}}})
	got := c.Resolve(map[string]any{
		"a": map[string]any{"$state": "/name"},
		"b": map[string]any{"$template": "Hi ${/name}, n=${/n} ${/missing}!"},
		"c": map[string]any{"$cond": map[string]any{"$state": "/n", "eq": 2}, "$then": "two", "$else": "other"},
		"d": []any{map[string]any{"$cond": map[string]any{"not": map[string]any{"$state": "/list"}}, "$then": "empty"}},
		"e": map[string]any{"$bindState": "/on"},
	})
	want := map[string]any{"a": "Ada", "b": "Hi Ada, n=2 !", "c": "two", "d": []any{"empty"}, "e": true}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v", got)
	}
	if !c.Cond([]any{map[string]any{"$state": "/on"}, map[string]any{"$state": "/name", "neq": "Bob"}}) || c.Cond(map[string]any{"$state": "/list"}) || c.Cond(false) {
		t.Error("Cond")
	}
}

func TestInteractive(t *testing.T) {
	raw, _ := os.ReadFile("testdata/form_nested.json")
	spec, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	fields, buttons := Interactive(spec, nil)
	var got []string
	for _, f := range fields {
		got = append(got, fmt.Sprintf("%s %s %q %s opts=%d multi=%v ml=%v", f.ID, f.Type, f.Label, f.Path, len(f.Options), f.Multiple, f.Multiline))
	}
	want := []string{
		`e3 Choice "Speed" /speed opts=2 multi=false ml=false`,
		`e4 Choice "Checks" /checks opts=3 multi=true ml=false`,
		`e5 Select "Region" /region opts=3 multi=false ml=false`,
		`e6 Toggle "Dry run" /dry opts=0 multi=false ml=false`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("fields:\n%s", strings.Join(got, "\n"))
	}
	if fields[0].Options[1].Description == "" || fields[0].Options[0].Value != "fast" {
		t.Errorf("options: %+v", fields[0].Options)
	}
	if len(buttons) != 2 || buttons[0].Label != "Run" || buttons[0].Actions[0].Name != "submit" || buttons[0].Actions[0].Params["mode"] != "fast" ||
		buttons[1].Variant != "danger" || buttons[1].Actions[0].Name != "setState" || buttons[1].Actions[1].Name != "cancel" {
		t.Errorf("buttons: %+v", buttons)
	}
	// the notes field only shows when dry run is off; turning it off reveals it
	st := CloneState(spec.State)
	SetState(st, "/dry", false)
	fields, _ = Interactive(spec, st)
	if len(fields) != 5 || fields[4].Type != "TextInput" || !fields[4].Multiline || fields[4].Path != "/notes" || fields[4].Placeholder == "" {
		t.Errorf("after toggle: %+v", fields)
	}
	// and the button param template follows state
	SetState(st, "/speed", "thorough")
	_, buttons = Interactive(spec, st)
	if buttons[0].Actions[0].Params["mode"] != "thorough" {
		t.Errorf("params: %+v", buttons[0].Actions[0].Params)
	}
	// Render with live state shows the edit
	out := Render(spec, Options{Width: 60, State: st})
	if !strings.Contains(out, "(•) Thorough") {
		t.Errorf("render with state:\n%s", out)
	}
}

func TestRegistryCoversCatalog(t *testing.T) {
	for _, k := range []string{"Card", "Stack", "Grid", "Tabs", "Heading", "Text", "Badge", "Divider", "Stat", "Meter", "Table", "List", "KeyValue", "Timeline", "Chart", "Graph", "Choice", "TextInput", "Toggle", "Select", "Button", "Buttons", "Code", "Diff", "Image"} {
		if Registry[k] == nil {
			t.Errorf("no renderer for %s", k)
		}
	}
}
