package edit

import (
	"strings"

	"gopkg.in/yaml.v3"
)

// Scope returns the mapping an agent or project editor works on, inside
// the central config's root: "defaults", a project slug, or
// "<slug>/<workflow>" for a workflow override. With create unset a scope
// that does not exist yet is nil; a missing project is nil either way.
func Scope(root *yaml.Node, scope string, create bool) *yaml.Node {
	parts := strings.Split(scope, "/")
	switch len(parts) {
	case 1:
		if parts[0] != "defaults" {
			return nil
		}
		return child(root, "defaults", create)
	case 2, 3:
		repo := Repo(root, parts[0]+"/"+parts[1])
		if repo == nil || len(parts) == 2 {
			return repo
		}
		return child(child(repo, "workflow_overrides", create), parts[2], create)
	}
	return nil
}

// Repo returns the entry of repos with the given slug, or nil.
func Repo(root *yaml.Node, slug string) *yaml.Node {
	repos := get(root, "repos")
	if repos == nil || repos.Kind != yaml.SequenceNode {
		return nil
	}
	for _, r := range repos.Content {
		if scalarText(get(r, "repo")) == slug {
			return r
		}
	}
	return nil
}

func child(m *yaml.Node, key string, create bool) *yaml.Node {
	if m == nil {
		return nil
	}
	if c := get(m, key); c != nil && c.Kind == yaml.MappingNode {
		return c
	}
	if !create {
		return nil
	}
	c := newMap()
	setKey(m, key, c)
	return c
}

// AddRepo appends an entry with the given slug to repos, creating the list
// when the file has none, and returns the new mapping.
func AddRepo(root *yaml.Node, slug string) *yaml.Node {
	repos := get(root, "repos")
	if repos == nil || repos.Kind != yaml.SequenceNode {
		repos = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		setKey(root, "repos", repos)
	}
	entry := newMap()
	setKey(entry, "repo", scalar("!!str", slug))
	repos.Content = append(repos.Content, entry)
	return entry
}

// RemoveRepo deletes the entry of repos with the given slug and reports
// whether there was one.
func RemoveRepo(root *yaml.Node, slug string) bool {
	repos := get(root, "repos")
	if repos == nil || repos.Kind != yaml.SequenceNode {
		return false
	}
	for i, r := range repos.Content {
		if scalarText(get(r, "repo")) == slug {
			repos.Content = append(repos.Content[:i], repos.Content[i+1:]...)
			return true
		}
	}
	return false
}
