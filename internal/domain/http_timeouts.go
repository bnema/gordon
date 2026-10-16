package domain

import (
	"fmt"
	"time"
)

// Default HTTP server timeouts.
const (
	DefaultRegistryReadHeaderTimeout = 10 * time.Second
	DefaultRegistryReadTimeout       = 5 * time.Minute
	DefaultRegistryWriteTimeout      = 5 * time.Minute
	DefaultRegistryIdleTimeout       = 120 * time.Second

	DefaultEntryPointReadHeaderTimeout   = 5 * time.Second
	DefaultEntryPointIdleTimeout         = 30 * time.Second
	DefaultEntryPointTransferIdleTimeout = 2 * time.Minute
)

// RegistryHTTPTimeouts holds the fixed timeouts of the registry HTTP servers
// (config section server.http). Read and Write timeouts bound the whole
// request and response, which suits registry API calls.
type RegistryHTTPTimeouts struct {
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
}

// DefaultRegistryHTTPTimeouts returns the default registry server timeouts.
func DefaultRegistryHTTPTimeouts() RegistryHTTPTimeouts {
	return RegistryHTTPTimeouts{
		ReadHeaderTimeout: DefaultRegistryReadHeaderTimeout,
		ReadTimeout:       DefaultRegistryReadTimeout,
		WriteTimeout:      DefaultRegistryWriteTimeout,
		IdleTimeout:       DefaultRegistryIdleTimeout,
	}
}

// WithDefaults replaces zero values with the defaults.
func (t RegistryHTTPTimeouts) WithDefaults() RegistryHTTPTimeouts {
	defaults := DefaultRegistryHTTPTimeouts()
	if t.ReadHeaderTimeout == 0 {
		t.ReadHeaderTimeout = defaults.ReadHeaderTimeout
	}
	if t.ReadTimeout == 0 {
		t.ReadTimeout = defaults.ReadTimeout
	}
	if t.WriteTimeout == 0 {
		t.WriteTimeout = defaults.WriteTimeout
	}
	if t.IdleTimeout == 0 {
		t.IdleTimeout = defaults.IdleTimeout
	}
	return t
}

// Validate rejects negative durations. Zero values mean "use the default".
func (t RegistryHTTPTimeouts) Validate() error {
	return validateNonNegativeDurations(map[string]time.Duration{
		"server.http.read_header_timeout": t.ReadHeaderTimeout,
		"server.http.read_timeout":        t.ReadTimeout,
		"server.http.write_timeout":       t.WriteTimeout,
		"server.http.idle_timeout":        t.IdleTimeout,
	})
}

// HTTPOptions holds the timeouts of the HTTP servers behind traffic
// entrypoints (config section traffic.http).
//
// There is deliberately no fixed read or write timeout: ReadHeaderTimeout
// bounds the request header, IdleTimeout bounds keep-alive connections, and
// TransferIdleTimeout cuts a transfer only after that long without data in
// the direction being waited on.
type HTTPOptions struct {
	ReadHeaderTimeout   time.Duration
	IdleTimeout         time.Duration
	TransferIdleTimeout time.Duration
}

// DefaultHTTPOptions returns the default entrypoint HTTP timeouts.
func DefaultHTTPOptions() HTTPOptions {
	return HTTPOptions{
		ReadHeaderTimeout:   DefaultEntryPointReadHeaderTimeout,
		IdleTimeout:         DefaultEntryPointIdleTimeout,
		TransferIdleTimeout: DefaultEntryPointTransferIdleTimeout,
	}
}

// WithDefaults replaces zero values with the defaults.
func (o HTTPOptions) WithDefaults() HTTPOptions {
	defaults := DefaultHTTPOptions()
	if o.ReadHeaderTimeout == 0 {
		o.ReadHeaderTimeout = defaults.ReadHeaderTimeout
	}
	if o.IdleTimeout == 0 {
		o.IdleTimeout = defaults.IdleTimeout
	}
	if o.TransferIdleTimeout == 0 {
		o.TransferIdleTimeout = defaults.TransferIdleTimeout
	}
	return o
}

// Validate rejects negative durations. Zero values mean "use the default".
func (o HTTPOptions) Validate() error {
	return validateNonNegativeDurations(map[string]time.Duration{
		"traffic.http.read_header_timeout":   o.ReadHeaderTimeout,
		"traffic.http.idle_timeout":          o.IdleTimeout,
		"traffic.http.transfer_idle_timeout": o.TransferIdleTimeout,
	})
}

func validateNonNegativeDurations(values map[string]time.Duration) error {
	for name, value := range values {
		if value < 0 {
			return fmt.Errorf("%s must be positive", name)
		}
	}
	return nil
}
