// Package remotemanagement defines the test contract for management command
// families that must execute against a remote control plane.
package remotemanagement

// Families is the complete remote Cobra management surface. Keep this list in
// sync with NewRootCmd; both the lightweight CLI smoke and production control
// listener gate consume it.
var Families = []string{
	"attachments", "autoroute", "backups", "bootstrap", "config",
	"deploy", "images", "logs", "migrate", "networks", "pin", "preview",
	"push", "reload", "restart", "routes", "secrets", "status",
	"tls", "traffic", "volumes",
}
