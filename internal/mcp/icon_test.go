package mcp

import "testing"

func TestServerAdvertisesIconFromConfiguredOrigin(t *testing.T) {
	env, _, _ := newMCPTestAppWithApp(t, true)
	cfg := env.api.Config()
	cfg.BaseURL = "https://wiki.example.test"
	env.api.SetConfig(cfg)
	s := NewServer(env.api, Options{Version: "test"})
	info := s.implementation()
	if info.Title != "HoldMyDocs" || info.Description != "Search, read and manage pages and attachments in HoldMyDocs." {
		t.Fatalf("server metadata = %#v", info)
	}
	if len(info.Icons) != 1 || info.Icons[0].Source != cfg.BaseURL+"/_/static/icon.svg" ||
		info.Icons[0].MIMEType != "image/svg+xml" || info.Icons[0].Sizes[0] != "any" {
		t.Fatalf("server icons = %#v", info.Icons)
	}
	cfg.BaseURL = ""
	env.api.SetConfig(cfg)
	if len(s.implementation().Icons) != 0 {
		t.Fatal("icon advertised without canonical origin")
	}
}
