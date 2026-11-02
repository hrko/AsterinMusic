package config

import (
	"bytes"
	"errors"
	"go.yaml.in/yaml/v3"
	"io"
	"regexp"
	"strings"
)

type yamlParser struct{ lookup func(string) (string, bool) }

func (yamlParser) Marshal(map[string]any) ([]byte, error) { return nil, errors.New("unsupported") }
func (p yamlParser) Unmarshal(b []byte) (map[string]any, error) {
	var root yaml.Node
	decoder := yaml.NewDecoder(bytes.NewReader(b))
	if err := decoder.Decode(&root); err != nil {
		return nil, errors.New("invalid YAML")
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, errors.New("multiple YAML documents")
	}
	if len(root.Content) != 1 || root.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("expected mapping")
	}
	if err := checkNode(root.Content[0]); err != nil {
		return nil, err
	}
	var m map[string]any
	if err := root.Decode(&m); err != nil {
		return nil, errors.New("invalid YAML values")
	}
	v, err := expandValue(m, p.lookup)
	if err != nil {
		return nil, err
	}
	return v.(map[string]any), nil
}
func checkNode(n *yaml.Node) error {
	if n.Kind == yaml.AliasNode || n.Tag == "!!null" {
		return errors.New("aliases and null unsupported")
	}
	if n.Kind == yaml.MappingNode {
		seen := map[string]bool{}
		for i := 0; i < len(n.Content); i += 2 {
			k := n.Content[i]
			// Dots would collide with koanf's path delimiter.
			if k.Tag != "!!str" || strings.Contains(k.Value, ".") || seen[k.Value] {
				return errors.New("duplicate or invalid YAML key")
			}
			seen[k.Value] = true
		}
	}
	for _, c := range n.Content {
		if err := checkNode(c); err != nil {
			return err
		}
	}
	return nil
}
func expandValue(v any, lookup func(string) (string, bool)) (any, error) {
	switch x := v.(type) {
	case string:
		return expand(x, lookup)
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			v, err := expandValue(e, lookup)
			if err != nil {
				return nil, err
			}
			out[i] = v
		}
		return out, nil
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			v, err := expandValue(e, lookup)
			if err != nil {
				return nil, err
			}
			out[k] = v
		}
		return out, nil
	default:
		return v, nil
	}
}

var variablePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func expand(s string, lookup func(string) (string, bool)) (string, error) {
	var out strings.Builder
	for i := 0; i < len(s); {
		if s[i] != '$' {
			out.WriteByte(s[i])
			i++
			continue
		}
		if i+1 < len(s) && s[i+1] == '$' {
			out.WriteByte('$')
			i += 2
			continue
		}
		if i+1 >= len(s) || s[i+1] != '{' {
			return "", errors.New("invalid variable syntax")
		}
		end := strings.IndexByte(s[i+2:], '}')
		if end < 0 {
			return "", errors.New("unterminated variable")
		}
		end += i + 2
		name := s[i+2 : end]
		if !variablePattern.MatchString(name) {
			return "", errors.New("invalid variable name")
		}
		value, ok := lookup(name)
		if !ok {
			return "", errors.New("undefined variable")
		}
		out.WriteString(value)
		i = end + 1
	}
	return out.String(), nil
}
