package web

import "hmd/internal/config"

type Config = config.Config
type GitConfig = config.GitConfig
type MCPConfig = config.MCPConfig
type OIDCConfig = config.OIDCConfig
type DocumentSearchConfig = config.DocumentSearchConfig

var (
	LoadConfig     = config.LoadConfig
	LoadFileConfig = config.LoadFileConfig
	SaveFileConfig = config.SaveFileConfig
)

func envOr(key, fallback string) string { return config.EnvOr(key, fallback) }

func parseAuthor(value, fallback string) (string, string) { return config.ParseAuthor(value, fallback) }

func validateWritableDataDir(path string) error { return config.ValidateWritableDataDir(path) }
