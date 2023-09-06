package config

import "io/fs"

type Provider interface {
	GetTemplateFS() fs.FS
	GetPublicFS() fs.FS
	GetModelFS() fs.FS
}
