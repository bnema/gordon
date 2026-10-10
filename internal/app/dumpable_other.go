//go:build !linux

package app

// disableDumpable is Linux-only; other platforms keep their defaults.
func disableDumpable() error { return nil }
