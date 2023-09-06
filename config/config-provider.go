package config

import "io/fs"

type Provider interface {
	GetTemplateFS() fs.FS
	GetPublicFS() fs.FS
	GetModelFS() fs.FS
}

type Config struct {
	TemplateFS fs.FS
	PublicFS   fs.FS
	ModelFS    fs.FS
}

func GetConfig() *Config {
	return &Config{}
}
