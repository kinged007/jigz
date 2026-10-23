package edit

import (
	"fmt"
	"net/url"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// A form struct describes one editor. Each field carries its YAML path in
// the `f` tag, relative to the mapping the editor works on, and its
// presentation in `label`, `hint`, `opts` (a closed list of choices) and
// `group` (the card it is drawn in). The same tags drive reading a file,
// drawing the form, parsing a post and writing the result back.
//
// A field may also carry `at`, the YAML path to use when it differs from
// the form name, with a {a|b} part naming the paths whose scalar value
// (looked up in the edited mapping, then in the file's root) fills that
// part in: `{agent|agent.kind}.model` is the model setting of the block
// named after the agent kind. A field with no resolvable value is left
// out of reading and writing. A `needs` tag names an engine version the
// field depends on; callers pass the versions they cannot offer as hide
// and those fields are skipped everywhere.
//
// Supported field types: string, *int, *bool, []string, map[string]string
// (one "key: value" per line) and a slice of structs, drawn as a table with
// one row per element. A table row whose first field is empty is dropped.

// Problem is a validation failure, tied to a form field when Path is set.
type Problem struct {
	Path string
	Msg  string
}

type fieldInfo struct {
	path, label, hint, group string
	at                       string
	opts                     []string
	v                        reflect.Value
}

func walk(v reflect.Value, hide []string) []fieldInfo {
	t := v.Type()
	var out []fieldInfo
	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		p := sf.Tag.Get("f")
		if p == "" {
			continue
		}
		if n := sf.Tag.Get("needs"); n != "" && slices.Contains(hide, n) {
			continue
		}
		fi := fieldInfo{path: p, label: sf.Tag.Get("label"), hint: sf.Tag.Get("hint"), group: sf.Tag.Get("group"), at: sf.Tag.Get("at"), v: v.Field(i)}
		if o := sf.Tag.Get("opts"); o != "" {
			fi.opts = strings.Split(o, ",")
		}
		out = append(out, fi)
	}
	return out
}

func isRows(v reflect.Value) bool {
	return v.Kind() == reflect.Slice && v.Type().Elem().Kind() == reflect.Struct
}

// text is a field value as the form shows it.
func text(v reflect.Value) string {
	switch x := v.Interface().(type) {
	case string:
		return x
	case *int:
		if x != nil {
			return strconv.Itoa(*x)
		}
	case *bool:
		if x != nil {
			return strconv.FormatBool(*x)
		}
	case []string:
		return strings.Join(x, ", ")
	case map[string]string:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		lines := make([]string, len(keys))
		for i, k := range keys {
			lines[i] = k + ": " + x[k]
		}
		return strings.Join(lines, "\n")
	}
	return ""
}

func parseText(v reflect.Value, s string) error {
	s = strings.TrimSpace(s)
	switch v.Interface().(type) {
	case string:
		v.SetString(s)
	case *int:
		if s == "" {
			v.Set(reflect.Zero(v.Type()))
			return nil
		}
		n, err := strconv.Atoi(s)
		if err != nil {
			return fmt.Errorf("%q is not a whole number", s)
		}
		v.Set(reflect.ValueOf(&n))
	case *bool:
		if s == "" {
			v.Set(reflect.Zero(v.Type()))
			return nil
		}
		b, err := strconv.ParseBool(s)
		if err != nil {
			return fmt.Errorf("%q is not true or false", s)
		}
		v.Set(reflect.ValueOf(&b))
	case []string:
		var out []string
		for _, p := range strings.Split(s, ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
		v.Set(reflect.ValueOf(out))
	case map[string]string:
		out := map[string]string{}
		for _, line := range strings.Split(s, "\n") {
			if line = strings.TrimSpace(line); line == "" {
				continue
			}
			k, val, ok := strings.Cut(line, ":")
			if k = strings.TrimSpace(k); !ok || k == "" {
				return fmt.Errorf("%q is not in the form name: value", line)
			}
			out[k] = strings.TrimSpace(val)
		}
		v.Set(reflect.ValueOf(out))
	}
	return nil
}

// nodeText is a node's value in the form's text, parsed back through the
// field type so both sides compare the same way.
func nodeText(n *yaml.Node, v reflect.Value) string {
	if n == nil {
		return ""
	}
	tmp := reflect.New(v.Type()).Elem()
	switch tmp.Interface().(type) {
	case []string:
		var items []string
		if n.Kind == yaml.SequenceNode {
			for _, c := range n.Content {
				items = append(items, scalarText(c))
			}
		} else if s := scalarText(n); s != "" {
			items = []string{s}
		}
		tmp.Set(reflect.ValueOf(items))
	case map[string]string:
		m := map[string]string{}
		if n.Kind == yaml.MappingNode {
			for i := 0; i+1 < len(n.Content); i += 2 {
				m[n.Content[i].Value] = scalarText(n.Content[i+1])
			}
		}
		tmp.Set(reflect.ValueOf(m))
	default:
		_ = parseText(tmp, scalarText(n))
	}
	return text(tmp)
}

// physical is the YAML path a field reads and writes in the mapping cur,
// or "" when its `at` part has no value in cur or in the file's top.
func physical(f fieldInfo, cur, top *yaml.Node) string {
	if f.at == "" {
		return f.path
	}
	open, end := strings.Index(f.at, "{"), strings.Index(f.at, "}")
	for _, cand := range strings.Split(f.at[open+1:end], "|") {
		for _, m := range []*yaml.Node{cur, top} {
			if n := lookup(m, cand); n != nil && n.Kind == yaml.ScalarNode && scalarText(n) != "" {
				return f.at[:open] + scalarText(n) + f.at[end+1:]
			}
		}
	}
	return ""
}

// Read fills the struct dst points to from the mapping root.
func Read(root *yaml.Node, dst any, hide ...string) {
	readValue(root, root, reflect.ValueOf(dst).Elem(), hide)
}

func readValue(root, top *yaml.Node, v reflect.Value, hide []string) {
	for _, f := range walk(v, hide) {
		path := physical(f, root, top)
		if path == "" {
			continue
		}
		n := lookup(root, path)
		if isRows(f.v) {
			if n == nil || n.Kind != yaml.SequenceNode {
				continue
			}
			rows := reflect.MakeSlice(f.v.Type(), len(n.Content), len(n.Content))
			for i, item := range n.Content {
				readValue(item, top, rows.Index(i), hide)
			}
			f.v.Set(rows)
			continue
		}
		_ = parseText(f.v, nodeText(n, f.v))
	}
}

// Parse fills the struct dst points to from a posted form. Names are the
// field paths, with table rows as <path>.<index>.<field>. A value that
// does not fit its field type is returned as a problem.
func Parse(vals url.Values, dst any, hide ...string) []Problem {
	return parseValue(vals, "", reflect.ValueOf(dst).Elem(), hide)
}

func parseValue(vals url.Values, prefix string, v reflect.Value, hide []string) []Problem {
	var probs []Problem
	for _, f := range walk(v, hide) {
		name := prefix + f.path
		if isRows(f.v) {
			n := 0
			for k := range vals {
				if rest, ok := strings.CutPrefix(k, name+"."); ok {
					idx, _, _ := strings.Cut(rest, ".")
					if i, err := strconv.Atoi(idx); err == nil && i >= n {
						n = i + 1
					}
				}
			}
			rows := reflect.MakeSlice(f.v.Type(), n, n)
			for i := 0; i < n; i++ {
				probs = append(probs, parseValue(vals, name+"."+strconv.Itoa(i)+".", rows.Index(i), hide)...)
			}
			f.v.Set(rows)
			continue
		}
		if err := parseText(f.v, vals.Get(name)); err != nil {
			probs = append(probs, Problem{Path: name, Msg: err.Error()})
		}
	}
	return probs
}

// Write applies the struct src points to onto the mapping root. A value
// equal to what the file holds leaves its node alone, an empty value
// removes the key, and keys the struct does not name are never touched.
func Write(root *yaml.Node, src any, hide ...string) {
	writeValue(root, root, reflect.ValueOf(src).Elem(), hide)
}

func writeValue(root, top *yaml.Node, v reflect.Value, hide []string) {
	for _, f := range walk(v, hide) {
		if isRows(f.v) {
			writeRows(root, top, f, hide)
			continue
		}
		path := physical(f, root, top)
		if path == "" {
			continue
		}
		cur := lookup(root, path)
		want := text(f.v)
		if nodeText(cur, f.v) == want {
			continue
		}
		if want == "" {
			deleteKey(root, path)
			continue
		}
		parent, key := root, path
		if i := strings.LastIndex(path, "."); i >= 0 {
			parent, key = ensure(root, path[:i]), path[i+1:]
		}
		setNode(parent, key, cur, f.v)
	}
}

func setNode(parent *yaml.Node, key string, cur *yaml.Node, v reflect.Value) {
	switch x := v.Interface().(type) {
	case string:
		setScalar(parent, key, cur, "!!str", x)
	case *int:
		setScalar(parent, key, cur, "!!int", strconv.Itoa(*x))
	case *bool:
		setScalar(parent, key, cur, "!!bool", strconv.FormatBool(*x))
	case []string:
		seq := cur
		if seq == nil || seq.Kind != yaml.SequenceNode {
			seq = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Style: yaml.FlowStyle}
			setKey(parent, key, seq)
		}
		seq.Content = nil
		for _, s := range x {
			seq.Content = append(seq.Content, scalar("!!str", s))
		}
	case map[string]string:
		m := cur
		if m == nil || m.Kind != yaml.MappingNode {
			m = newMap()
			setKey(parent, key, m)
		}
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for i := 0; i < len(m.Content); i += 2 {
			if _, ok := x[m.Content[i].Value]; !ok {
				m.Content = append(m.Content[:i], m.Content[i+2:]...)
				i -= 2
			}
		}
		for _, k := range keys {
			if nodeText(get(m, k), reflect.ValueOf("")) != x[k] {
				setKey(m, k, scalar("!!str", x[k]))
			}
		}
	}
}

func setScalar(parent *yaml.Node, key string, cur *yaml.Node, tag, value string) {
	if cur != nil && cur.Kind == yaml.ScalarNode {
		retag(cur, tag, value)
		return
	}
	setKey(parent, key, scalar(tag, value))
}

func writeRows(root, top *yaml.Node, f fieldInfo, hide []string) {
	cur := lookup(root, f.path)
	var existing []*yaml.Node
	if cur != nil && cur.Kind == yaml.SequenceNode {
		existing = cur.Content
	}
	var content []*yaml.Node
	for i := 0; i < f.v.Len(); i++ {
		row := f.v.Index(i)
		if text(row.Field(firstTagged(row))) == "" {
			continue
		}
		var item *yaml.Node
		if i < len(existing) && existing[i].Kind == yaml.MappingNode {
			item = existing[i]
		} else {
			item = newMap()
		}
		writeValue(item, top, row, hide)
		content = append(content, item)
	}
	switch {
	case len(content) == 0 && cur != nil:
		deleteKey(root, f.path)
	case len(content) > 0 && cur == nil:
		parent, key := root, f.path
		if i := strings.LastIndex(f.path, "."); i >= 0 {
			parent, key = ensure(root, f.path[:i]), f.path[i+1:]
		}
		insertKey(parent, key, &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: content})
	case len(content) > 0:
		cur.Content = content
	}
}

func firstTagged(v reflect.Value) int {
	for i := 0; i < v.NumField(); i++ {
		if v.Type().Field(i).Tag.Get("f") != "" {
			return i
		}
	}
	return 0
}

// Field is one input of a drawn form.
type Field struct {
	Name, Label, Hint, Group string
	Kind                     string // text, int, bool, list, lines or select
	Value, Inherit, Error    string
	Options                  []string
}

// Table is a slice-of-structs field drawn as rows, with one spare row for
// adding an element.
type Table struct {
	Name, Title string
	Head        []Field
	Rows        [][]Field
}

// Form is everything the template needs to draw an editor.
type Form struct {
	Fields []Field
	Tables []Table
}

// Describe draws the struct src points to. Problems are matched to fields
// by name, and inherit gives the value a blank field falls back to.
func Describe(src any, probs []Problem, inherit map[string]string, hide ...string) Form {
	var form Form
	v := reflect.ValueOf(src).Elem()
	errs := map[string]string{}
	for _, p := range probs {
		if _, dup := errs[p.Path]; !dup {
			errs[p.Path] = p.Msg
		}
	}
	for _, f := range walk(v, hide) {
		if isRows(f.v) {
			form.Tables = append(form.Tables, describeRows(f, errs, hide))
			continue
		}
		form.Fields = append(form.Fields, describeField(f, f.path, errs, inherit))
	}
	return form
}

func describeField(f fieldInfo, name string, errs, inherit map[string]string) Field {
	fd := Field{Name: name, Label: f.label, Hint: f.hint, Group: f.group, Value: text(f.v),
		Inherit: inherit[f.path], Error: errs[name], Kind: "text", Options: f.opts}
	switch f.v.Interface().(type) {
	case *int:
		fd.Kind = "int"
	case *bool:
		fd.Kind, fd.Options = "bool", []string{"true", "false"}
	case []string:
		fd.Kind = "list"
	case map[string]string:
		fd.Kind = "lines"
	}
	if len(f.opts) > 0 {
		fd.Kind = "select"
		if fd.Value != "" && !slices.Contains(fd.Options, fd.Value) {
			fd.Options = append(slices.Clone(fd.Options), fd.Value)
		}
	}
	return fd
}

func describeRows(f fieldInfo, errs map[string]string, hide []string) Table {
	t := Table{Name: f.path, Title: f.label}
	rows := reflect.MakeSlice(f.v.Type(), f.v.Len(), f.v.Len()+1)
	reflect.Copy(rows, f.v)
	spare := reflect.New(f.v.Type().Elem()).Elem()
	if n := rows.Len(); n == 0 || text(rows.Index(n-1).Field(firstTagged(rows.Index(n-1)))) != "" {
		rows = reflect.Append(rows, spare)
	}
	for i := 0; i < rows.Len(); i++ {
		var cells []Field
		for _, c := range walk(rows.Index(i), hide) {
			cells = append(cells, describeField(c, f.path+"."+strconv.Itoa(i)+"."+c.path, errs, nil))
		}
		t.Rows = append(t.Rows, cells)
	}
	for _, c := range walk(rows.Index(0), hide) {
		t.Head = append(t.Head, Field{Label: c.label, Hint: c.hint})
	}
	return t
}
