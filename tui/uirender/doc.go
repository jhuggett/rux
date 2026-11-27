// Package uirender renders rux UI specs (the json-render style specs agents
// post with `rux ui`, see catalogDoc in uicli.go) as terminal text. It is the
// TUI counterpart of web/ui.js and follows its semantics: the same spec
// forms, expressions ($state, $bindState, $template, $cond), "visible"
// conditions and "on" actions.
//
// # Rendering
//
// Parse turns raw JSON (flat {root, elements, state} or a nested
// {type, props, children} tree) into a Spec. Render(spec, opts) returns lines
// joined by "\n", each at most opts.Width display cells. With opts.Color
// false the output is plain text (box drawing and block glyphs, no escape
// codes), which is what the golden tests in testdata/ use; with Color true
// it is styled with 256-color lipgloss styles in a green phosphor palette
// meant for a black background.
//
// # The registry
//
// Registry maps each catalog component type ("Card", "Table", "Chart", ...)
// to a Component:
//
//	type Component func(c *Ctx, id string, el *Element, props map[string]any, width int) string
//
// A component gets its element id, the raw Element (children, on, and the
// unresolved props, e.g. to find a $bindState path), its props already
// resolved against state, and the width it must fit. It returns its lines.
// Layout components render children through the context:
//
//	c.Children(el, w)  // visible children rendered at width w, empties dropped
//	c.Node(id, w)      // one element (visibility, depth limit, memo, panic recovery)
//	c.Resolve(v)       // evaluate expressions in v against the current state
//	c.Paint(style, s)  // style text (no-op when Color is off)
//
// Ctx.Node is the single entry point for every element: it reports missing
// elements and specs nested deeper than 40 levels (which also stops cycles)
// as error lines, skips elements whose "visible" condition fails, recovers
// from a component's panic as a "⚠ Type: message" line, memoizes the result
// per (id, width) and truncates any line wider than the width. Types not in
// the registry render as a dim box with the type name and the props as
// compact JSON, followed by their children. To add or override a component,
// set Registry["Name"] before rendering (it is not safe to modify while
// rendering concurrently).
//
// Strings from the spec are cleaned of control characters, so a spec can't
// emit terminal escape sequences; links in Text and List hrefs become OSC 8
// hyperlinks only when Color is on and the URL is plain http(s).
//
// # Options
//
//   - State replaces spec.State, so the TUI can render live form edits.
//   - Image(src, width) draws an Image's picture (e.g. with terminal
//     graphics or half blocks); without it a "[image: src]" placeholder is
//     shown. The caption (or alt) is rendered below by this package.
//   - Fetch(src) loads a Diff's "src" patch; returning ok=false shows
//     "loading…" (re-render once it has arrived). It may be called several
//     times per render, so it should be a cache lookup.
//   - Focus is the id of the element under the TUI's form cursor: inputs get
//     a bright "▌ " gutter and a bright label/border, buttons render as
//     [▸Label◂] in reverse video.
//   - Tabs selects a tab per Tabs element id; without an entry a Tabs element
//     shows every tab stacked under a "▸ Label ───" header (a terminal
//     scrollback can't click tabs).
//   - Now fixes the time for Timeline (open-ended bars, the now marker).
//
// # Forms
//
// Interactive(spec, state) lists the visible inputs (Choice, Select,
// TextInput, Toggle) and Buttons in document order, with each input's
// $bindState path, options and on.change actions, and each button's press
// actions (params resolved against state). The TUI edits a CloneState copy
// with SetState, re-renders with Options.State, and on a button runs
// setState actions locally and posts others (with the state) as the answer.
package uirender
