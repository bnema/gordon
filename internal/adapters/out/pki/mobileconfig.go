package pki

import (
	"encoding/base64"
	"fmt"
	"html"
	"strings"
	"unicode"

	"github.com/google/uuid"
)

// GenerateMobileconfig generates an iOS configuration profile containing
// the root CA certificate for one-tap trust installation.
func GenerateMobileconfig(rootDER []byte, rootCN string) []byte {
	b64Cert := base64.StdEncoding.EncodeToString(rootDER)
	profileUUID := uuid.New().String()
	certUUID := uuid.New().String()

	safeCN := html.EscapeString(rootCN)
	identifierCN := payloadIdentifierComponent(rootCN)
	profile := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>PayloadContent</key>
	<array>
		<dict>
			<key>PayloadCertificateFileName</key>
			<string>gordon-ca.crt</string>
			<key>PayloadContent</key>
			<data>%s</data>
			<key>PayloadDescription</key>
			<string>Installs the Gordon internal CA root certificate</string>
			<key>PayloadDisplayName</key>
			<string>%s</string>
			<key>PayloadIdentifier</key>
			<string>dev.gordon.ca.%s</string>
			<key>PayloadType</key>
			<string>com.apple.security.root</string>
			<key>PayloadUUID</key>
			<string>%s</string>
			<key>PayloadVersion</key>
			<integer>1</integer>
		</dict>
	</array>
	<key>PayloadDescription</key>
	<string>Trust Gordon's internal Certificate Authority for HTTPS access</string>
	<key>PayloadDisplayName</key>
	<string>Gordon CA Trust</string>
	<key>PayloadIdentifier</key>
	<string>dev.gordon.ca-profile</string>
	<key>PayloadType</key>
	<string>Configuration</string>
	<key>PayloadUUID</key>
	<string>%s</string>
	<key>PayloadVersion</key>
	<integer>1</integer>
	</dict>
</plist>`, b64Cert, safeCN, identifierCN, certUUID, profileUUID)

	return []byte(profile)
}

func payloadIdentifierComponent(rootCN string) string {
	var b strings.Builder
	lastHyphen := false

	for _, r := range rootCN {
		r = unicode.ToLower(r)
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.':
			b.WriteRune(r)
			lastHyphen = false
		case r == '-':
			if !lastHyphen {
				b.WriteRune(r)
				lastHyphen = true
			}
		default:
			if !lastHyphen {
				b.WriteRune('-')
				lastHyphen = true
			}
		}
	}

	identifier := strings.Trim(b.String(), "-.")
	if identifier == "" {
		return "gordon-ca"
	}

	return identifier
}
