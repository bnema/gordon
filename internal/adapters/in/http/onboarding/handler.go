package onboarding

import (
	"fmt"
	"html"
	"net"
	"net/http"
	"strconv"
)

// Handler serves CA onboarding endpoints for direct/Tailnet clients.
type Handler struct {
	rootPEM      []byte
	mobileconfig []byte
	fingerprint  string
	httpPort     int
	tlsPort      int
}

// NewHandler creates an onboarding handler.
// fingerprint is the SHA-256 fingerprint of the root CA certificate.
// httpPort is the configured HTTP listener port (used to map to tlsPort).
// tlsPort is the configured HTTPS listener port.
func NewHandler(rootPEM, mobileconfig []byte, fingerprint string, httpPort, tlsPort int) *Handler {
	return &Handler{
		rootPEM:      rootPEM,
		mobileconfig: mobileconfig,
		fingerprint:  fingerprint,
		httpPort:     httpPort,
		tlsPort:      tlsPort,
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

// publicHTTPSURL derives the public HTTPS URL from the request Host header.
//
// Rules:
//   - no port in Host => https://host/ (omit internal TLS port)
//   - Host port == httpPort => https://host:tlsPort/
//   - any other explicit port => preserve it
//
// The result is HTML-escaped to prevent XSS via a crafted Host header.
func (h *Handler) publicHTTPSURL(r *http.Request) string {
	host, portStr, err := net.SplitHostPort(r.Host)
	if err != nil {
		// No port in Host header — use bare hostname.
		return html.EscapeString("https://" + r.Host + "/")
	}

	port, _ := strconv.Atoi(portStr)
	if port == h.httpPort {
		return html.EscapeString(fmt.Sprintf("https://%s:%d/", host, h.tlsPort))
	}
	return html.EscapeString(fmt.Sprintf("https://%s:%s/", host, portStr))
}

// isRawIP reports whether the host (without port) is a raw IP literal.
func isRawIP(hostHeader string) bool {
	host, _, err := net.SplitHostPort(hostHeader)
	if err != nil {
		host = hostHeader
	}
	return net.ParseIP(host) != nil
}

// ServeOnboardingPage serves the CA trust onboarding HTML page.
func (h *Handler) ServeOnboardingPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	redirectURL := h.publicHTTPSURL(r)
	showSiteLink := !isRawIP(r.Host)

	// On HTTP (r.TLS == nil), omit Secure from the cookie so browsers persist it.
	// On HTTPS, do not set the cookie at all — it is an HTTP-bootstrap-only UX hint.
	cookieJS := ""
	if r.TLS == nil {
		cookieJS = fmt.Sprintf(
			`onclick="document.cookie='gordon-ca-installed=1;path=/;max-age=315360000;SameSite=Lax'"`,
		)
	}

	siteLink := ""
	if showSiteLink {
		siteLink = fmt.Sprintf(
			`<div class="skip"><p>Already installed? <a href="%s" %s>Go to site &#x2192;</a></p></div>`,
			redirectURL, cookieJS,
		)
	}

	_, _ = fmt.Fprintf(w, `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Gordon — Trust CA Certificate</title>
<style>
  body { font-family: -apple-system, system-ui, sans-serif; max-width: 600px; margin: 40px auto; padding: 0 20px; color: #333; }
  h1 { font-size: 1.5em; }
  .fingerprint { background: #fef3c7; border: 1px solid #f59e0b; border-radius: 8px; padding: 12px 16px; margin: 16px 0; font-family: monospace; font-size: 0.85em; word-break: break-all; }
  .fingerprint strong { display: block; margin-bottom: 4px; font-family: -apple-system, system-ui, sans-serif; }
  .platform { background: #f5f5f5; border-radius: 8px; padding: 16px; margin: 12px 0; }
  .platform h2 { font-size: 1.1em; margin: 0 0 8px; }
  a.btn { display: inline-block; background: #2563eb; color: white; padding: 10px 20px; border-radius: 6px; text-decoration: none; margin: 4px 0; }
  a.btn:hover { background: #1d4ed8; }
  .skip { margin-top: 24px; font-size: 0.9em; }
  .skip a { color: #666; }
  code { background: #e5e7eb; padding: 2px 6px; border-radius: 4px; font-size: 0.9em; }
  .note { margin-top: 20px; font-size: 0.85em; color: #666; }
</style>
</head>
<body>
<h1>Gordon — Trust CA Certificate</h1>
<p>This Gordon instance uses its own internal CA for HTTPS. Import the CA certificate into your device or browser to connect securely.</p>

<div class="fingerprint">
  <strong>CA Fingerprint (SHA-256)</strong>
  %s
</div>
<p style="font-size:0.85em;color:#666;">Verify this fingerprint matches the output of <code>gordon ca info</code> on the server.</p>

<div class="platform" id="ios">
  <h2>iOS / iPadOS</h2>
  <p><a class="btn" href="/ca.mobileconfig">Download Profile</a></p>
  <p>After downloading, go to <strong>Settings → General → VPN &amp; Device Management</strong> to install, then <strong>Settings → General → About → Certificate Trust Settings</strong> to enable full trust.</p>
</div>

<div class="platform" id="android">
  <h2>Android</h2>
  <p><a class="btn" href="/ca.crt">Download Certificate</a></p>
  <p>Go to <strong>Settings → Security → Install a certificate → CA certificate</strong> and select the downloaded file.</p>
</div>

<div class="platform" id="desktop">
  <h2>macOS / Linux / Windows</h2>
  <p><a class="btn" href="/ca.crt">Download Certificate</a></p>
  <p>Double-click the downloaded file and add it to your system trust store, or import it via your OS certificate manager.</p>
</div>

<div class="platform" id="firefox">
  <h2>Firefox / Zen</h2>
  <p><a class="btn" href="/ca.crt">Download Certificate</a></p>
  <p>Firefox and Zen use their own certificate store (NSS). Go to <strong>Settings → Privacy &amp; Security → Certificates → View Certificates → Authorities → Import</strong> and select the downloaded file. Check <strong>Trust this CA to identify websites</strong>.</p>
</div>

%s

<p class="note">The <code>gordon ca install</code> command is for the Gordon server host only, not for client devices.</p>
</body>
</html>`, html.EscapeString(h.fingerprint), siteLink)
}
