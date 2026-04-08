package cli

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	pkiadapter "github.com/bnema/gordon/internal/adapters/out/pki"
	boundaryout "github.com/bnema/gordon/internal/boundaries/out"
)

func newCACmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ca",
		Short: "Manage Gordon's internal Certificate Authority",
	}

	cmd.AddCommand(newCAExportCmd())
	cmd.AddCommand(newCAInstallCmd())
	cmd.AddCommand(newCAInfoCmd())

	return cmd
}

func newCAExportCmd() *cobra.Command {
	var outPath string

	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export the root CA certificate",
		Long:  "Export Gordon's root CA certificate in PEM format for manual trust installation.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			dataDir, err := resolveCADataDir()
			if err != nil {
				return err
			}
			return runCAExport(cmd.Context(), cmd.OutOrStdout(), dataDir, outPath)
		},
	}

	cmd.Flags().StringVar(&outPath, "out", "", "Write certificate to file instead of stdout")

	return cmd
}

func newCAInstallCmd() *cobra.Command {
	var (
		uninstall bool
		jsonOut   bool
	)

	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install/uninstall the root CA in the system trust store",
		Long:  "Install Gordon's root CA certificate into the system, Firefox, and Java trust stores. Requires running as root.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			dataDir, err := resolveCADataDir()
			if err != nil {
				return err
			}
			return runCAInstall(cmd.Context(), cmd.OutOrStdout(), dataDir, uninstall, jsonOut)
		},
	}

	cmd.Flags().BoolVar(&uninstall, "uninstall", false, "Remove from trust stores instead of installing")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")

	return cmd
}

func newCAInfoCmd() *cobra.Command {
	var jsonOut bool

	cmd := &cobra.Command{
		Use:   "info",
		Short: "Show CA status information",
		RunE: func(cmd *cobra.Command, _ []string) error {
			dataDir, err := resolveCADataDir()
			if err != nil {
				return err
			}
			return runCAInfo(cmd.Context(), cmd.OutOrStdout(), dataDir, jsonOut)
		},
	}

	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")

	return cmd
}

func resolveCADataDir() (string, error) {
	local, err := GetLocalServices(configPath)
	if err != nil {
		return "", err
	}
	if local.GetTLSPort() == 0 {
		return "", fmt.Errorf("internal TLS is disabled (server.tls_port=0); CA commands are not available — set server.tls_port to enable")
	}
	return local.GetDataDir(), nil
}

// loadCAFromDataDir creates a CA adapter directly (bypassing the usecase layer).
// This is intentional: CLI commands like 'ca export' and 'ca info' are standalone
// admin utilities that run outside the server lifecycle and don't need business logic.
func loadCAFromDataDir(dataDir string) (boundaryout.CertificateAuthority, error) {
	ca, err := pkiadapter.NewCA(dataDir, cliLogger())
	if err != nil {
		return nil, fmt.Errorf("failed to load CA from %s: %w", dataDir, err)
	}
	return ca, nil
}

func runCAExport(_ context.Context, out io.Writer, dataDir, outPath string) error {
	ca, err := loadCAFromDataDir(dataDir)
	if err != nil {
		return err
	}

	rootPEM := ca.RootCertificate()

	if outPath != "" {
		if err := os.MkdirAll(filepath.Dir(outPath), 0750); err != nil {
			return fmt.Errorf("create directory for %s: %w", outPath, err)
		}
		if err := os.WriteFile(outPath, rootPEM, 0600); err != nil {
			return fmt.Errorf("write certificate to %s: %w", outPath, err)
		}
		return cliWriteLine(out, cliRenderSuccess(fmt.Sprintf("Root CA certificate written to %s", outPath)))
	}

	_, err = out.Write(rootPEM)
	return err
}

func runCAInstall(_ context.Context, out io.Writer, dataDir string, uninstall, jsonOut bool) error {
	ca, err := loadCAFromDataDir(dataDir)
	if err != nil {
		return err
	}

	rootPEM := ca.RootCertificate()
	block, _ := pem.Decode(rootPEM)
	if block == nil {
		return fmt.Errorf("failed to decode root CA PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return fmt.Errorf("failed to parse root CA: %w", err)
	}

	if uninstall {
		if err := ca.UninstallRoot(cert); err != nil {
			return fmt.Errorf("failed to uninstall root CA: %w", err)
		}
		if jsonOut {
			return writeJSON(out, map[string]string{"status": "uninstalled"})
		}
		return cliWriteLine(out, cliRenderSuccess("Root CA removed from system trust stores"))
	}

	if err := ca.InstallRoot(cert); err != nil {
		return fmt.Errorf("failed to install root CA: %w", err)
	}
	if jsonOut {
		return writeJSON(out, map[string]string{"status": "installed"})
	}
	return cliWriteLine(out, cliRenderSuccess("Root CA installed in system trust stores (system, Firefox, Java)"))
}

func runCAInfo(_ context.Context, out io.Writer, dataDir string, jsonOut bool) error {
	ca, err := loadCAFromDataDir(dataDir)
	if err != nil {
		return err
	}

	interExpiry := ca.IntermediateExpiresAt()
	remaining := time.Until(interExpiry).Truncate(time.Minute)

	if jsonOut {
		return writeJSON(out, map[string]any{
			"root_cn":             ca.RootCommonName(),
			"fingerprint":         "SHA256:" + ca.RootFingerprint(),
			"intermediate_expiry": interExpiry.Format(time.RFC3339),
			"intermediate_ttl":    remaining.String(),
		})
	}

	if err := cliWriteLine(out, cliRenderMeta("Root CA:", ca.RootCommonName())); err != nil {
		return err
	}
	if err := cliWriteLine(out, cliRenderMeta("Fingerprint:", "SHA256:"+ca.RootFingerprint())); err != nil {
		return err
	}
	return cliWriteLine(out, cliRenderMeta("Intermediate:", fmt.Sprintf("expires in %s (auto-renews)", remaining)))
}
