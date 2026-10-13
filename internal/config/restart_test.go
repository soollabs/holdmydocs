package config

import (
	"reflect"
	"testing"
)

// changeAll gives every string, bool, integer and string-list field of v a
// different value, so each setting reads differently afterwards.
func changeAll(v reflect.Value) {
	switch v.Kind() {
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			changeAll(v.Field(i))
		}
	case reflect.String:
		v.SetString(v.String() + "-changed")
	case reflect.Bool:
		v.SetBool(!v.Bool())
	case reflect.Int, reflect.Int64:
		v.SetInt(v.Int() + 1)
	case reflect.Slice:
		if list, ok := v.Interface().([]string); ok {
			v.Set(reflect.ValueOf(append(append([]string(nil), list...), "changed")))
		}
	}
}

// TestKeepRestartValuesCoversTheSchema pins KeepRestartValues to the schema:
// every x-restart setting keeps its running value and every other editable
// setting takes the saved one. A new restart setting fails here until it is
// added to KeepRestartValues.
func TestKeepRestartValuesCoversTheSchema(t *testing.T) {
	running := Config{}
	saved := running
	changeAll(reflect.ValueOf(&saved).Elem())

	kept := KeepRestartValues(running, saved)
	for _, s := range Settings() {
		if s.EnvOnly {
			continue
		}
		want := SettingValue(saved, s.Key)
		if s.Restart {
			want = SettingValue(running, s.Key)
		}
		if got := SettingValue(kept, s.Key); !reflect.DeepEqual(got, want) {
			t.Errorf("%s (restart %v) = %#v, want %#v", s.Key, s.Restart, got, want)
		}
	}

	if pending := PendingRestart(running, saved); len(pending) != len(RestartSettings()) {
		t.Errorf("PendingRestart after changing everything lists %d settings, want all %d", len(pending), len(RestartSettings()))
	}
	if pending := PendingRestart(running, kept); len(pending) != 0 {
		t.Errorf("PendingRestart with restart values kept = %v, want none", pending)
	}
	if pending := PendingRestart(running, running); len(pending) != 0 {
		t.Errorf("PendingRestart with nothing changed = %v, want none", pending)
	}
}
