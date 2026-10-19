package edit

import (
	"bytes"
	"errors"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Doc is a YAML document held as a node tree, so an edit keeps the
// comments and the key order of the file it started from.
type Doc struct {
	src  string
	node yaml.Node
	root *yaml.Node
}

// ParseDoc parses YAML text. Empty text, or text of comments only, is an
// empty mapping.
func ParseDoc(text string) (*Doc, error) {
	d := &Doc{src: text}
	if err := yaml.Unmarshal([]byte(text), &d.node); err != nil {
		return nil, err
	}
	if d.node.Kind == 0 {
		d.node = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
	}
	d.root = d.node.Content[0]
	if d.root.Kind != yaml.MappingNode {
		return nil, errors.New("the top level must be a mapping of keys")
	}
	return d, nil
}

// Root is the top-level mapping.
func (d *Doc) Root() *yaml.Node { return d.root }

// String encodes the document with the source's indentation style and the
// blank lines it had.
func (d *Doc) String() (string, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&d.node); err != nil {
		return "", err
	}
	if err := enc.Close(); err != nil {
		return "", err
	}
	return keepBlankLines(d.src, buf.String()), nil
}

// get returns the value node of key in a mapping, or nil.
func get(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// lookup follows a dotted path of mapping keys.
func lookup(m *yaml.Node, path string) *yaml.Node {
	for _, k := range strings.Split(path, ".") {
		if m = get(m, k); m == nil {
			return nil
		}
	}
	return m
}

func newMap() *yaml.Node { return &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"} }

// ensure returns the mapping at a dotted path, creating the keys it lacks.
func ensure(m *yaml.Node, path string) *yaml.Node {
	for _, k := range strings.Split(path, ".") {
		next := get(m, k)
		if next == nil || next.Kind != yaml.MappingNode {
			next = newMap()
			setKey(m, k, next)
		}
		m = next
	}
	return m
}

// setKey replaces the value of key, or appends the pair. A replaced
// value keeps the comments that sat on the old one.
func setKey(m *yaml.Node, key string, v *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			old := m.Content[i+1]
			v.HeadComment, v.LineComment, v.FootComment = old.HeadComment, old.LineComment, old.FootComment
			m.Content[i+1] = v
			return
		}
	}
	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, v)
}

// insertKey puts a new pair first in the mapping.
func insertKey(m *yaml.Node, key string, v *yaml.Node) {
	k := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}
	m.Content = append([]*yaml.Node{k, v}, m.Content...)
}

// deleteKey removes a pair, and removes the mapping it leaves empty.
func deleteKey(m *yaml.Node, path string) {
	keys := strings.Split(path, ".")
	parents := []*yaml.Node{m}
	for _, k := range keys[:len(keys)-1] {
		next := get(parents[len(parents)-1], k)
		if next == nil || next.Kind != yaml.MappingNode {
			return
		}
		parents = append(parents, next)
	}
	for depth := len(keys) - 1; depth >= 0; depth-- {
		p := parents[depth]
		for i := 0; i+1 < len(p.Content); i += 2 {
			if p.Content[i].Value == keys[depth] {
				p.Content = append(p.Content[:i], p.Content[i+2:]...)
				break
			}
		}
		if len(p.Content) > 0 || depth == 0 {
			return
		}
	}
}

// scalarText is a scalar node's value as text, "" for anything else.
func scalarText(n *yaml.Node) string {
	if n == nil || n.Kind != yaml.ScalarNode || n.Tag == "!!null" {
		return ""
	}
	return n.Value
}

func scalar(tag, value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: value}
}

// retag sets a node to a new scalar value, keeping its comments and quoting.
func retag(n *yaml.Node, tag, value string) {
	n.Kind, n.Tag, n.Value = yaml.ScalarNode, tag, value
	n.Content = nil
	if tag != "!!str" {
		n.Style = 0
	}
}

func isInt(s string) bool { _, err := strconv.Atoi(s); return err == nil }

// SplitFrontMatter separates a workflow file into its YAML front matter
// and everything after the closing fence, which is returned verbatim.
func SplitFrontMatter(text string) (front, rest string, err error) {
	if !strings.HasPrefix(text, "---\n") {
		return "", "", errors.New("the file does not start with a --- front matter fence")
	}
	body := text[4:]
	if strings.HasPrefix(body, "---\n") || body == "---" {
		return "", body, nil
	}
	i := strings.Index(body, "\n---\n")
	if i < 0 {
		if strings.HasSuffix(body, "\n---") {
			return body[:len(body)-3], "---", nil
		}
		return "", "", errors.New("the front matter has no closing --- fence")
	}
	return body[:i+1], body[i+1:], nil
}

// JoinFrontMatter is the inverse of SplitFrontMatter.
func JoinFrontMatter(front, rest string) string { return "---\n" + front + rest }
