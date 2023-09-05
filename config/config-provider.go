package config

import "embed"

type Provider interface {
	GetTemplateFS() embed.FS
	GetPublicFS() embed.FS
	GetModelFS() embed.FS
}
