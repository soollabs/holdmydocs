package config

import (
	"testing"

	"github.com/goccy/go-yaml"
)

func FuzzConfigYAML(f *testing.F) {
	for _, seed := range []string{"bind: ':8080'", "unknown: true", "oidc:\n  issuer: https://example.com", "git:\n  token: !!binary AA=="} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		if len(input) > 1<<20 {
			t.Skip()
		}
		var cfg fileConfig
		_ = yaml.UnmarshalWithOptions([]byte(input), &cfg, yaml.Strict())
	})
}
