package pki

import (
	"crypto/x509"

	"github.com/smallstep/truststore"
)

// InstallRoot installs the root CA certificate into the system trust store.
func InstallRoot(cert *x509.Certificate) error {
	return truststore.Install(cert,
		truststore.WithFirefox(),
		truststore.WithJava(),
	)
}

// UninstallRoot removes the root CA certificate from the system trust store.
func UninstallRoot(cert *x509.Certificate) error {
	return truststore.Uninstall(cert,
		truststore.WithFirefox(),
		truststore.WithJava(),
	)
}
