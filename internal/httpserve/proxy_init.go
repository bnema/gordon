package httpserve

import (
	"github.com/bnema/gordon/internal/proxy"
	"github.com/bnema/gordon/internal/server"
	"github.com/charmbracelet/log"
)

// InitializeProxy initializes and starts the reverse proxy
func InitializeProxy(a *server.App) (*proxy.Proxy, error) {
	log.Info("Initializing reverse proxy")

	// Create a new proxy
	p, err := proxy.NewProxy(a)
	if err != nil {
		return nil, err
	}

	// Start the proxy
	if err := p.Start(); err != nil {
		return nil, err
	}

	log.Info("Reverse proxy initialized and started")
	return p, nil
}
