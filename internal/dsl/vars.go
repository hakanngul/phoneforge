package dsl

import (
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
)

var varRe = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_.\-]*)\}`)

// expand replaces ${name} in s. In strict mode an unknown name is an error;
// otherwise it is left untouched.
func expand(s string, vars map[string]string, strict bool) (string, error) {
	var firstErr error
	out := varRe.ReplaceAllStringFunc(s, func(m string) string {
		name := varRe.FindStringSubmatch(m)[1]
		if v, ok := vars[name]; ok {
			return v
		}
		if strict && firstErr == nil {
			firstErr = codeErr(CodeValidation, "unknown variable: ${%s}", name)
		}
		return m
	})
	return out, firstErr
}

// Substitute returns a copy of st with ${name} replaced in every string
// field. Nested steps (then, else, body) are left for execution time. With
// strict set an unknown variable is an error; otherwise it stays as written.
func Substitute(st Step, vars map[string]string, strict bool) (Step, error) {
	b, err := json.Marshal(st)
	if err != nil {
		return st, err
	}
	var c Step
	if err := json.Unmarshal(b, &c); err != nil {
		return st, err
	}
	// Restore nested steps untouched (substituted when they run).
	c.Then, c.Else, c.Body = st.Then, st.Else, st.Body
	if err := walkStrings(reflect.ValueOf(&c).Elem(), func(s string) (string, error) {
		return expand(s, vars, strict)
	}); err != nil {
		return st, err
	}
	return c, nil
}

var stepSliceType = reflect.TypeOf([]Step(nil))

func walkStrings(v reflect.Value, fn func(string) (string, error)) error {
	switch v.Kind() {
	case reflect.String:
		if !v.CanSet() {
			return nil
		}
		out, err := fn(v.String())
		if err != nil {
			return err
		}
		v.SetString(out)
	case reflect.Ptr:
		if !v.IsNil() {
			return walkStrings(v.Elem(), fn)
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).IsExported() {
				if err := walkStrings(v.Field(i), fn); err != nil {
					return err
				}
			}
		}
	case reflect.Slice:
		if v.Type() == stepSliceType || v.Type().Elem().Kind() == reflect.Uint8 {
			return nil
		}
		for i := 0; i < v.Len(); i++ {
			if err := walkStrings(v.Index(i), fn); err != nil {
				return err
			}
		}
	case reflect.Map:
		if v.Type().Elem().Kind() != reflect.String {
			return nil
		}
		for _, k := range v.MapKeys() {
			out, err := fn(v.MapIndex(k).String())
			if err != nil {
				return err
			}
			v.SetMapIndex(k, reflect.ValueOf(out))
		}
	}
	return nil
}

func scriptVars(s Script, over map[string]string) (map[string]string, error) {
	vars := map[string]string{}
	for name, spec := range s.Params {
		if v, ok := over[name]; ok {
			vars[name] = v
		} else if spec.Has {
			vars[name] = spec.Default
		} else {
			return nil, fmt.Errorf("dsl: param %q has no value (no default and none supplied)", name)
		}
	}
	for k, v := range over {
		if _, ok := vars[k]; !ok {
			vars[k] = v
		}
	}
	return vars, nil
}
