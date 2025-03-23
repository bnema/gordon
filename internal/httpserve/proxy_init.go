package httpserve

import (
	"time"
	
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

	// Add a small delay to ensure router initialization is complete
	// This helps prevent race conditions during startup
	time.Sleep(500 * time.Millisecond)
	
	log.Info("Reverse proxy initialized and started")
	return p, nil
}
