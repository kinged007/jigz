package config

import (
	"fmt"
	"reflect"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// nodeKeyChecker is implemented by values that unmarshal from more than
// one YAML shape (a string or a {from_env} mapping, for example).
// checkKeys delegates their key check to them so an unknown key reports
// the path of the value it belongs to. Only types of this package can
// implement it; every other type in the document tree is checked against
// its yaml tags.
type nodeKeyChecker interface {
	checkNodeKeys(node *yaml.Node, path string) error
}

var nodeKeyCheckerType = reflect.TypeOf((*nodeKeyChecker)(nil)).Elem()

// checkKeys rejects mapping keys that no yaml-tagged field of typ accepts
// and reports each as unknown key "<path>.<key>". It is the strict-key
// mechanism for .jigz/config.yaml: yaml.Decoder.KnownFields cannot report
// the path of the key, and the loader's errors name one.
func checkKeys(node *yaml.Node, typ reflect.Type, path string) error {
	for node != nil && node.Kind == yaml.AliasNode {
		node = node.Alias
	}
	if node == nil {
		return nil
	}
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if reflect.PointerTo(typ).Implements(nodeKeyCheckerType) {
		return reflect.New(typ).Interface().(nodeKeyChecker).checkNodeKeys(node, path)
	}
	switch typ.Kind() {
	case reflect.Struct:
		if node.Kind != yaml.MappingNode {
			return nil
		}
		fields := yamlFields(typ)
		for i := 0; i+1 < len(node.Content); i += 2 {
			key := node.Content[i].Value
			ftyp, ok := fields[key]
			if !ok {
				return fmt.Errorf("unknown key %q", joinKeyPath(path, key))
			}
			if err := checkKeys(node.Content[i+1], ftyp, joinKeyPath(path, key)); err != nil {
				return err
			}
		}
	case reflect.Map:
		if node.Kind != yaml.MappingNode {
			return nil
		}
		for i := 0; i+1 < len(node.Content); i += 2 {
			if err := checkKeys(node.Content[i+1], typ.Elem(), joinKeyPath(path, node.Content[i].Value)); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		if node.Kind != yaml.SequenceNode {
			return nil
		}
		for i, item := range node.Content {
			if err := checkKeys(item, typ.Elem(), fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	}
	return nil
}

// joinKeyPath appends a mapping key to a field path.
func joinKeyPath(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

// yamlFields maps the YAML key of every exported field of typ to the
// field type, following the naming rules of the yaml package.
func yamlFields(typ reflect.Type) map[string]reflect.Type {
	fields := map[string]reflect.Type{}
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if f.PkgPath != "" {
			continue
		}
		name, opts, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		if name == "-" {
			continue
		}
		if (f.Anonymous && f.Tag.Get("yaml") == "") || slices.Contains(strings.Split(opts, ","), "inline") {
			inl := f.Type
			for inl.Kind() == reflect.Pointer {
				inl = inl.Elem()
			}
			if inl.Kind() == reflect.Struct {
				for k, v := range yamlFields(inl) {
					fields[k] = v
				}
			}
			continue
		}
		if name == "" {
			name = strings.ToLower(f.Name)
		}
		fields[name] = f.Type
	}
	return fields
}

// checkMapKeys rejects mapping keys outside allowed.
func checkMapKeys(node *yaml.Node, path string, allowed ...string) error {
	if node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		key := node.Content[i].Value
		if !slices.Contains(allowed, key) {
			return fmt.Errorf("unknown key %q", joinKeyPath(path, key))
		}
	}
	return nil
}
