package act

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Raposa-Industries/adhunters/contract/actions"
)

// Check tells whether in is an input the action takes, without running it:
// the same checks Read and Call make. Names in bound are filled later (from
// an earlier step of a plan), so they count as given and are not checked; a
// name with a dot ("input.set") is one field of an object argument.
func Check(a actions.Action, in map[string]any, bound []string) error {
	if err := known(a, in); err != nil {
		return err
	}
	isBound := map[string]bool{}
	for _, b := range bound {
		isBound[b] = true
	}
	for _, g := range a.Args {
		if g.From != "" || len(g.Const) > 0 || isBound[g.Name] {
			continue
		}
		raw, ok := in[g.Name]
		if !ok || raw == nil {
			switch {
			case g.Type == "object" && boundInside(g.Name, bound):
				// Only its bound fields will be there: they must be all it needs.
				if err := checkObjectExcept(g, map[string]any{}, bound); err != nil {
					return err
				}
			case !g.Optional:
				return refuse("%s is needed: %s", g.Name, g.Says)
			}
			continue
		}
		if g.Type == "object" && boundInside(g.Name, bound) {
			// Its bound fields come later; check the rest with them left out.
			if err := checkObjectExcept(g, raw, bound); err != nil {
				return err
			}
			continue
		}
		if _, err := value(g.Name, g.Type, g.Enum, g.Max, g.Schema, raw); err != nil {
			return err
		}
	}
	if a.Kind == actions.Read {
		for _, f := range a.Filters {
			raw, ok := in[f.Property()]
			if !ok || raw == nil || isBound[f.Property()] {
				continue
			}
			if f.Op == "in" {
				list, ok := raw.([]any)
				if !ok {
					return refuse("%s is a list", f.Property())
				}
				for _, item := range list {
					if _, err := value(f.Property(), f.Type, f.Enum, 0, nil, item); err != nil {
						return err
					}
				}
				continue
			}
			if _, err := value(f.Property(), f.Type, f.Enum, 0, nil, raw); err != nil {
				return err
			}
		}
		if raw, ok := in[actions.LimitProperty]; ok && raw != nil {
			n, err := integer(actions.LimitProperty, raw)
			if err != nil {
				return err
			}
			if n < 1 {
				return refuse("limit is 1 or more")
			}
		}
	}
	return nil
}

func boundInside(arg string, bound []string) bool {
	for _, b := range bound {
		if strings.HasPrefix(b, arg+".") {
			return true
		}
	}
	return false
}

func checkObjectExcept(g actions.Arg, raw any, bound []string) error {
	m, ok := raw.(map[string]any)
	if !ok {
		return refuse("%s is an object", g.Name)
	}
	var s map[string]any
	if err := json.Unmarshal(g.Schema, &s); err != nil {
		return fmt.Errorf("act: %s has no schema", g.Name)
	}
	skip := map[string]bool{}
	for _, b := range bound {
		if field, ok := strings.CutPrefix(b, g.Name+"."); ok {
			skip[field] = true
		}
	}
	rest := map[string]any{}
	for k, v := range m {
		if !skip[k] {
			rest[k] = v
		}
	}
	s2 := map[string]any{}
	for k, v := range s {
		s2[k] = v
	}
	var req []any
	for _, r := range asList(s["required"]) {
		if name, _ := r.(string); !skip[name] {
			req = append(req, r)
		}
	}
	s2["required"] = req
	return Valid(s2, rest, g.Name)
}

func asList(v any) []any {
	switch l := v.(type) {
	case []any:
		return l
	case []string:
		out := make([]any, len(l))
		for i, s := range l {
			out[i] = s
		}
		return out
	}
	return nil
}

// Valid checks v against a schema of the kind the catalog allows
// (actions.CheckSchema): objects with closed properties, arrays, strings,
// integers, numbers, booleans and enums. path names v in what it says.
func Valid(s map[string]any, v any, path string) error {
	if enum, ok := s["enum"].([]any); ok {
		for _, e := range enum {
			if jsonEqual(e, v) {
				return nil
			}
		}
		return refuse("%s is one of %s", path, joinAny(enum))
	}
	switch t, _ := s["type"].(string); t {
	case "object":
		m, ok := v.(map[string]any)
		if !ok {
			return refuse("%s is an object", path)
		}
		props, _ := s["properties"].(map[string]any)
		for k, fv := range m {
			ps, ok := props[k].(map[string]any)
			if !ok {
				return refuse("%s takes no %q", path, k)
			}
			if err := Valid(ps, fv, path+"."+k); err != nil {
				return err
			}
		}
		for _, r := range asList(s["required"]) {
			name, _ := r.(string)
			if _, ok := m[name]; !ok {
				return refuse("%s.%s is needed", path, name)
			}
		}
	case "array":
		list, ok := v.([]any)
		if !ok {
			return refuse("%s is a list", path)
		}
		items, _ := s["items"].(map[string]any)
		for i, item := range list {
			if err := Valid(items, item, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	case "string":
		str, ok := v.(string)
		if !ok {
			return refuse("%s is text", path)
		}
		switch s["format"] {
		case "date":
			if _, err := time.Parse("2006-01-02", str); err != nil {
				return refuse("%s is a date like 2026-09-30", path)
			}
		case "date-time":
			if _, err := time.Parse(time.RFC3339, str); err != nil {
				return refuse("%s is a time like 2026-09-30T18:00:00Z", path)
			}
		}
	case "integer":
		if _, err := integer(path, v); err != nil {
			return err
		}
	case "number":
		switch v.(type) {
		case json.Number, float64, int64, int:
		default:
			return refuse("%s is a number", path)
		}
	case "boolean":
		if _, ok := v.(bool); !ok {
			return refuse("%s is true or false", path)
		}
	}
	return nil
}

func jsonEqual(a, b any) bool {
	x, err1 := json.Marshal(a)
	y, err2 := json.Marshal(b)
	return err1 == nil && err2 == nil && string(x) == string(y)
}

func joinAny(list []any) string {
	parts := make([]string, len(list))
	for i, v := range list {
		parts[i] = fmt.Sprint(v)
	}
	return strings.Join(parts, ", ")
}

// PropertyType is the type of one input property of an action, as the
// catalog writes types ("integer", "string[]"): an arg, a read's filter (an
// "in" filter takes a list), or a field of an object arg ("input.set").
// ok is false when the action takes no such input.
func PropertyType(a actions.Action, prop string) (typ string, ok bool) {
	top, field, nested := strings.Cut(prop, ".")
	for _, g := range a.Args {
		if g.Name != top || g.From != "" || len(g.Const) > 0 {
			continue
		}
		if !nested {
			return g.Type, true
		}
		if g.Type != "object" {
			return "", false
		}
		var s map[string]any
		if json.Unmarshal(g.Schema, &s) != nil {
			return "", false
		}
		props, _ := s["properties"].(map[string]any)
		fs, _ := props[field].(map[string]any)
		return schemaType(fs)
	}
	if a.Kind == actions.Read && !nested {
		for _, f := range a.Filters {
			if f.Property() == prop {
				if f.Op == "in" {
					return f.Type + "[]", true
				}
				return f.Type, true
			}
		}
	}
	return "", false
}

func schemaType(s map[string]any) (string, bool) {
	t, _ := s["type"].(string)
	switch t {
	case "string", "integer", "number", "boolean":
		return t, true
	case "array":
		items, _ := s["items"].(map[string]any)
		if it, _ := items["type"].(string); it == "string" || it == "integer" {
			return it + "[]", true
		}
	}
	if _, ok := s["enum"]; ok {
		return "string", true
	}
	return "", false
}
