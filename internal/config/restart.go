package config

import "reflect"

// RestartSettings lists the settings the settings screen can change that only
// take effect after a restart, in schema order.
func RestartSettings() []Setting {
	var restart []Setting
	for _, s := range Settings() {
		if s.Restart && !s.EnvOnly {
			restart = append(restart, s)
		}
	}
	return restart
}

// PendingRestart lists the restart-only settings whose saved value differs
// from the value the process is running with, in schema order.
func PendingRestart(running, saved Config) []Setting {
	var pending []Setting
	for _, s := range RestartSettings() {
		if !reflect.DeepEqual(SettingValue(running, s.Key), SettingValue(saved, s.Key)) {
			pending = append(pending, s)
		}
	}
	return pending
}

// KeepRestartValues returns saved with every restart-only setting left at its
// running value. The process reads those settings only at startup, so
// applying a saved value early would half-apply it: request handling would
// use the new value while the parts set up at startup kept the old one.
func KeepRestartValues(running, saved Config) Config {
	saved.Bind = running.Bind
	saved.RepoDir = running.RepoDir
	saved.BaseURL = running.BaseURL
	saved.TrustedProxies = running.TrustedProxies
	saved.ReadOnly = running.ReadOnly
	saved.Debug = running.Debug
	saved.TikaTimeout = running.TikaTimeout
	saved.MCP = running.MCP
	saved.OAuth = running.OAuth
	saved.OIDC = running.OIDC
	saved.TikaURL = running.TikaURL
	saved.DocumentSearch = running.DocumentSearch
	return saved
}
