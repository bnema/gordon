package onboarding

import (
	"fmt"
	"net"
	"net/http"

	pkiadapter "github.com/bnema/gordon/internal/adapters/out/pki"
)

// Handler serves CA onboarding endpoints on the HTTP port.
type Handler struct {
	rootPEM      []byte
	mobileconfig []byte
	cidrs        []*net.IPNet
}

// NewHandler creates an onboarding handler.
func NewHandler(rootPEM, rootDER []byte, rootCN string, cloudflareCIDRs []*net.IPNet) *Handler {
	return &Handler{
		rootPEM:      rootPEM,
		mobileconfig: pkiadapter.GenerateMobileconfig(rootDER, rootCN),
		cidrs:        cloudflareCIDRs,
	}
}

// ServeCACert serves the root CA certificate as PEM.
func (h *Handler) ServeCACert(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/x-x509-ca-cert")
	w.Header().Set("Content-Disposition", "attachment; filename=gordon-ca.crt")
	_, _ = w.Write(h.rootPEM)
}

// ServeMobileconfig serves an iOS configuration profile with the root CA.
func (h *Handler) ServeMobileconfig(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/x-apple-aspen-config")
	w.Header().Set("Content-Disposition", "attachment; filename=gordon-ca.mobileconfig")
	_, _ = w.Write(h.mobileconfig)
}

// ServeOnboardingPage serves the CA trust onboarding HTML page.
func (h *Handler) ServeOnboardingPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	scheme := "https"
	host := r.Host
	redirectURL := fmt.Sprintf("%s://%s/", scheme, host)

	_, _ = fmt.Fprintf(w, `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Gordon — Install CA Certificate</title>
<style>
  body { font-family: -apple-system, system-ui, sans-serif; max-width: 600px; margin: 40px auto; padding: 0 20px; color: #333; }
  h1 { font-size: 1.5em; }
  .platform { background: #f5f5f5; border-radius: 8px; padding: 16px; margin: 12px 0; }
  .platform h2 { font-size: 1.1em; margin: 0 0 8px; }
  a.btn { display: inline-block; background: #2563eb; color: white; padding: 10px 20px; border-radius: 6px; text-decoration: none; margin: 4px 0; }
  a.btn:hover { background: #1d4ed8; }
  .skip { margin-top: 24px; font-size: 0.9em; }
  .skip a { color: #666; }
  code { background: #e5e7eb; padding: 2px 6px; border-radius: 4px; font-size: 0.9em; }
</style>
</head>
<body>
<h1>Gordon — Secure Access Setup</h1>
<p>To access this service over HTTPS, install Gordon's CA certificate on your device. This is a one-time step.</p>

<div class="platform" id="ios">
  <h2>iOS / iPadOS</h2>
  <p><a class="btn" href="/ca.mobileconfig">Install Profile</a></p>
  <p>After downloading, go to <strong>Settings → General → VPN & Device Management</strong> to complete installation, then <strong>Settings → General → About → Certificate Trust Settings</strong> to enable full trust.</p>
</div>

<div class="platform" id="android">
  <h2>Android</h2>
  <p><a class="btn" href="/ca.crt">Download Certificate</a></p>
  <p>After downloading, go to <strong>Settings → Security → Install a certificate → CA certificate</strong>.</p>
</div>

<div class="platform" id="desktop">
  <h2>macOS / Linux / Windows</h2>
  <p><a class="btn" href="/ca.crt">Download Certificate</a></p>
  <p>Or run on the Gordon host: <code>sudo gordon ca install</code></p>
</div>

<div class="skip">
  <p>Already installed? <a href="%s" onclick="document.cookie='gordon-ca-installed=1;path=/;max-age=315360000;SameSite=Lax'">Go to site →</a></p>
</div>
</body>
</html>`, redirectURL)
}

// IsCloudflareIP checks if the given IP is in Cloudflare's CIDR ranges.
func (h *Handler) IsCloudflareIP(ip net.IP) bool {
	for _, cidr := range h.cidrs {
		if cidr.Contains(ip) {
			return true
		}
	}
	return false
}
