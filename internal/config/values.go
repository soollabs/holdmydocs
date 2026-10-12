package config

import (
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
)

// fileField finds the config.yaml field for a dotted schema key.
func fileField(v reflect.Value, key string) (reflect.Value, bool) {
	name, rest, nested := strings.Cut(key, ".")
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		tag, _, _ := strings.Cut(t.Field(i).Tag.Get("yaml"), ",")
		if tag != name {
			continue
		}
		if nested {
			if v.Field(i).Kind() != reflect.Struct {
				return reflect.Value{}, false
			}
			return fileField(v.Field(i), rest)
		}
		return v.Field(i), true
	}
	return reflect.Value{}, false
}

// FileValue returns a config.yaml value by schema key as a string, bool,
// int64 or []string; an unset pointer returns nil.
func FileValue(fc fileConfig, key string) any {
	f, ok := fileField(reflect.ValueOf(fc), key)
	if !ok {
		return nil
	}
	if f.Kind() == reflect.Pointer {
		if f.IsNil() {
			return nil
		}
		f = f.Elem()
	}
	switch f.Kind() {
	case reflect.Int, reflect.Int64:
		return f.Int()
	case reflect.Slice:
		return append([]string(nil), f.Interface().([]string)...)
	}
	return f.Interface()
}

// SetFileValue writes a submitted value into config.yaml by schema key,
// converting from the JSON shapes a form sends: strings, booleans, numbers
// and string lists. Rules are checked separately by ValidateFile.
func SetFileValue(fc *fileConfig, key string, value any) error {
	f, ok := fileField(reflect.ValueOf(fc).Elem(), key)
	if !ok {
		return fmt.Errorf("unknown setting %q", key)
	}
	if value == nil {
		// A cleared field drops the key, so the default applies again.
		f.Set(reflect.Zero(f.Type()))
		return nil
	}
	target := f
	if f.Kind() == reflect.Pointer {
		target = reflect.New(f.Type().Elem()).Elem()
	}
	switch target.Kind() {
	case reflect.String:
		s, ok := value.(string)
		if !ok {
			return fmt.Errorf("%s must be text", key)
		}
		target.SetString(strings.TrimSpace(s))
	case reflect.Bool:
		b, ok := value.(bool)
		if !ok {
			return fmt.Errorf("%s must be true or false", key)
		}
		target.SetBool(b)
	case reflect.Int, reflect.Int64:
		n, err := toInt(value)
		if err != nil {
			return fmt.Errorf("%s must be a whole number", key)
		}
		target.SetInt(n)
	case reflect.Slice:
		list, err := toStrings(value)
		if err != nil {
			return fmt.Errorf("%s must be a list of text values", key)
		}
		target.Set(reflect.ValueOf(list))
	default:
		return fmt.Errorf("%s has an unsupported type", key)
	}
	if f.Kind() == reflect.Pointer {
		f.Set(target.Addr())
	}
	return nil
}

func toInt(v any) (int64, error) {
	switch n := v.(type) {
	case float64:
		if n != math.Trunc(n) {
			return 0, fmt.Errorf("not whole")
		}
		return int64(n), nil
	case int:
		return int64(n), nil
	case int64:
		return n, nil
	case string:
		return strconv.ParseInt(strings.TrimSpace(n), 10, 64)
	}
	return 0, fmt.Errorf("not a number")
}

func toStrings(v any) ([]string, error) {
	var raw []any
	switch l := v.(type) {
	case []string:
		raw = make([]any, len(l))
		for i, s := range l {
			raw[i] = s
		}
	case []any:
		raw = l
	case string:
		for _, s := range strings.FieldsFunc(l, func(r rune) bool { return r == ',' || r == '\n' }) {
			raw = append(raw, s)
		}
	default:
		return nil, fmt.Errorf("not a list")
	}
	var out []string
	for _, item := range raw {
		s, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("not text")
		}
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out, nil
}

// SettingValue returns the value in effect for a settings key, including
// environment-only ones. Callers that render it must report secrets only as
// set or not set.
func SettingValue(c Config, key string) any {
	if v, ok := envOnlyValues[key]; ok {
		return v(c)
	}
	return FileValue(c.toFileConfig(), key)
}

// envOnlyValues reads each x-env-only setting from the resolved configuration,
// since none of them has a config.yaml field to read it from.
var envOnlyValues = map[string]func(Config) string{
	"server.app_dir":        func(c Config) string { return c.AppDir },
	"server.config_file":    func(c Config) string { return c.ConfigFile },
	"server.admin.user":     func(c Config) string { return c.AdminUser },
	"server.admin.password": func(c Config) string { return c.AdminPass },
	"git.token":             func(c Config) string { return c.Git.Token },
	"oidc.client_secret":    func(c Config) string { return c.OIDC.ClientSecret },
}
