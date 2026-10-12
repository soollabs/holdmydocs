package config

import (
	"fmt"
	"net"
	"net/url"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Rules are the value constraints a schema declares for one setting. The same
// rules validate config.yaml on load, settings submitted from the UI, and the
// browser form (through Rules.HTML), and they appear in the generated docs.
type Rules struct {
	Enum        []string // allowed values
	Pattern     string   // regular expression a string value must match
	Min, Max    *int64   // inclusive integer range
	MaxLength   *int     // most characters in a string value
	Format      string   // named check: host-port, url, origin, cidr or git-branch
	Schemes     []string // URL schemes accepted by the url and origin formats
	ItemEnum    []string // allowed values for each list item
	ItemPattern string   // regular expression each list item must match
	ItemFormat  string   // named check for each list item

	pattern, itemPattern *regexp.Regexp
}

// formatHints describe each named format in plain words for errors and tooltips.
var formatHints = map[string]string{
	"host-port":  "host:port, with a port from 1 to 65535",
	"url":        "URL without user-info or fragment",
	"origin":     "origin such as https://wiki.example.com, with no path, query, fragment or user-info",
	"cidr":       "a CIDR range such as 10.0.0.0/8",
	"git-branch": "a valid Git branch name",
}

func rulesFor(node schemaNode) Rules {
	r := Rules{
		Enum: node.Enum, Pattern: node.Pattern, Min: node.Minimum, Max: node.Maximum,
		MaxLength: node.MaxLength, Format: node.Format, Schemes: node.Schemes,
	}
	if node.Items != nil {
		r.ItemEnum, r.ItemPattern, r.ItemFormat = node.Items.Enum, node.Items.Pattern, node.Items.Format
	}
	return r
}

// checkRules compiles a setting's patterns and rejects unknown formats, so a
// mistake in the schema stops startup rather than silently accepting values.
func checkRules(key string, r Rules) error {
	for _, format := range []string{r.Format, r.ItemFormat} {
		if _, ok := formatHints[format]; format != "" && !ok {
			return fmt.Errorf("%s: unknown format %q", key, format)
		}
	}
	if err := r.compiled(); err != nil {
		return fmt.Errorf("%s: invalid pattern: %w", key, err)
	}
	return nil
}

func (r *Rules) compiled() error {
	var err error
	if r.Pattern != "" && r.pattern == nil {
		if r.pattern, err = regexp.Compile(r.Pattern); err != nil {
			return err
		}
	}
	if r.ItemPattern != "" && r.itemPattern == nil {
		if r.itemPattern, err = regexp.Compile(r.ItemPattern); err != nil {
			return err
		}
	}
	return nil
}

// Hint describes the accepted values in one short phrase, or "" when the
// setting has no constraint worth stating.
func (r Rules) Hint() string {
	var parts []string
	switch {
	case len(r.Enum) > 0:
		parts = append(parts, "one of "+strings.Join(r.Enum, ", "))
	case r.Format == "url" || r.Format == "origin":
		hint := "absolute " + formatHints[r.Format]
		if len(r.Schemes) > 0 {
			hint = strings.Join(r.Schemes, " or ") + " " + formatHints[r.Format]
		}
		parts = append(parts, hint)
	case r.Format != "":
		parts = append(parts, formatHints[r.Format])
	}
	switch {
	case r.Min != nil && r.Max != nil:
		parts = append(parts, fmt.Sprintf("%d to %d", *r.Min, *r.Max))
	case r.Min != nil:
		parts = append(parts, fmt.Sprintf("at least %d", *r.Min))
	case r.Max != nil:
		parts = append(parts, fmt.Sprintf("at most %d", *r.Max))
	}
	if r.MaxLength != nil {
		parts = append(parts, fmt.Sprintf("at most %d characters", *r.MaxLength))
	}
	if len(r.ItemEnum) > 0 {
		parts = append(parts, "any of "+strings.Join(r.ItemEnum, ", "))
	} else if r.ItemFormat != "" {
		parts = append(parts, "each "+formatHints[r.ItemFormat])
	}
	return strings.Join(parts, "; ")
}

// Check validates one value against the rules. Empty strings, nil pointers
// and empty lists are accepted: emptiness means "use the default" or "off",
// and required combinations are cross-field rules handled in code.
func (r Rules) Check(key string, value any) error {
	if err := r.compiled(); err != nil {
		return fmt.Errorf("%s: invalid schema pattern: %w", key, err)
	}
	switch v := value.(type) {
	case string:
		return r.checkString(key, v)
	case int:
		return r.checkInt(key, int64(v))
	case int64:
		return r.checkInt(key, v)
	case []string:
		for _, item := range v {
			if err := r.checkItem(key, item); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r Rules) checkString(key, v string) error {
	if v == "" {
		return nil
	}
	if len(r.Enum) > 0 && !slices.Contains(r.Enum, v) {
		return fmt.Errorf("invalid %s %q: must be one of %s", key, v, strings.Join(r.Enum, ", "))
	}
	if r.MaxLength != nil && len([]rune(v)) > *r.MaxLength {
		return fmt.Errorf("%s must be at most %d characters", key, *r.MaxLength)
	}
	if r.pattern != nil && !r.pattern.MatchString(v) {
		return fmt.Errorf("invalid %s %q: %s", key, v, r.Hint())
	}
	if r.Format != "" && !checkFormat(r.Format, r.Schemes, v) {
		return fmt.Errorf("invalid %s %q: must be %s", key, v, r.Hint())
	}
	return nil
}

func (r Rules) checkInt(key string, v int64) error {
	if (r.Min != nil && v < *r.Min) || (r.Max != nil && v > *r.Max) {
		return fmt.Errorf("%s must be %s", key, r.Hint())
	}
	return nil
}

func (r Rules) checkItem(key, item string) error {
	if len(r.ItemEnum) > 0 && !slices.Contains(r.ItemEnum, item) {
		return fmt.Errorf("invalid %s entry %q: must be one of %s", key, item, strings.Join(r.ItemEnum, ", "))
	}
	if r.itemPattern != nil && !r.itemPattern.MatchString(item) {
		return fmt.Errorf("invalid %s entry %q", key, item)
	}
	if r.ItemFormat != "" && !checkFormat(r.ItemFormat, nil, item) {
		return fmt.Errorf("invalid %s entry %q: must be %s", key, item, formatHints[r.ItemFormat])
	}
	return nil
}

func checkFormat(format string, schemes []string, v string) bool {
	switch format {
	case "host-port":
		_, port, err := net.SplitHostPort(v)
		n, convErr := strconv.Atoi(port)
		return err == nil && convErr == nil && n >= 1 && n <= 65535
	case "cidr":
		_, _, err := net.ParseCIDR(v)
		return err == nil
	case "git-branch":
		return validBranchName(v)
	case "url", "origin":
		u, err := url.Parse(strings.TrimRight(v, "/"))
		if err != nil || !u.IsAbs() || u.Host == "" || u.User != nil || u.Fragment != "" {
			return false
		}
		if len(schemes) > 0 && !slices.Contains(schemes, u.Scheme) {
			return false
		}
		if format == "origin" && (u.Path != "" || u.RawQuery != "" || u.ForceQuery) {
			return false
		}
		return true
	}
	return false
}

// ValidateFile checks every config.yaml value against its schema rules. It
// runs on load, after environment overrides, and before the settings screen
// writes the file, so an invalid value is never saved.
func ValidateFile(fc fileConfig) error {
	return validateValues(reflect.ValueOf(fc), "")
}

func validateValues(v reflect.Value, prefix string) error {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("yaml"), ",")
		if name == "" || name == "-" {
			continue
		}
		key := name
		if prefix != "" {
			key = prefix + "." + name
		}
		fv := v.Field(i)
		if fv.Kind() == reflect.Struct {
			if err := validateValues(fv, key); err != nil {
				return err
			}
			continue
		}
		if fv.Kind() == reflect.Pointer {
			if fv.IsNil() {
				continue
			}
			fv = fv.Elem()
		}
		s, ok := Lookup(key)
		if !ok {
			continue
		}
		if err := s.Rules.Check(key, fv.Interface()); err != nil {
			return err
		}
	}
	return nil
}
