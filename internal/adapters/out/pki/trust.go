package pki

import (
	"crypto/x509"
	"fmt"

	"github.com/smallstep/truststore"
)

// InstallRoot installs the root CA certificate into the system trust store.
func InstallRoot(cert *x509.Certificate) error {
	if err := truststore.Install(cert,
		truststore.WithFirefox(),
		truststore.WithJava(),
	); err != nil {
		return fmt.Errorf("install root certificate: %w", err)
	}
	return nil
}

// UninstallRoot removes the root CA certificate from the system trust store.
func UninstallRoot(cert *x509.Certificate) error {
	if err := truststore.Uninstall(cert,
		truststore.WithFirefox(),
		truststore.WithJava(),
	); err != nil {
		return fmt.Errorf("uninstall root certificate: %w", err)
	}
	return nil
}
