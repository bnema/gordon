package cli

import (
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	pkiadapter "github.com/bnema/gordon/internal/adapters/out/pki"
	"github.com/bnema/gordon/internal/app"
	"github.com/bnema/zerowrap"
	"github.com/spf13/cobra"
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
			return runCAExport(cmd.OutOrStdout(), outPath)
		},
	}

	cmd.Flags().StringVar(&outPath, "out", "", "Write certificate to file instead of stdout")

	return cmd
}

func newCAInstallCmd() *cobra.Command {
	var uninstall bool

	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install/uninstall the root CA in the system trust store",
		Long:  "Install Gordon's root CA certificate into the system, Firefox, and Java trust stores. Requires running as root.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runCAInstall(cmd.OutOrStdout(), uninstall)
		},
	}

	cmd.Flags().BoolVar(&uninstall, "uninstall", false, "Remove from trust stores instead of installing")

	return cmd
}

func newCAInfoCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "info",
		Short: "Show CA status information",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runCAInfo(cmd.OutOrStdout())
		},
	}
}

func loadCAFromDataDir() (*pkiadapter.CA, error) {
	dataDir := app.DefaultDataDir()
	log := zerowrap.Default()
	ca, err := pkiadapter.NewCA(dataDir, log)
	if err != nil {
		return nil, fmt.Errorf("failed to load CA from %s: %w", dataDir, err)
	}
	return ca, nil
}

func runCAExport(out io.Writer, outPath string) error {
	ca, err := loadCAFromDataDir()
	if err != nil {
		return err
	}

	rootPEM := ca.RootCertificate()

	if outPath != "" {
		if err := os.MkdirAll(filepath.Dir(outPath), 0755); err != nil {
			return err
		}
		if err := os.WriteFile(outPath, rootPEM, 0644); err != nil {
			return err
		}
		return cliWriteLine(out, cliRenderSuccess(fmt.Sprintf("Root CA certificate written to %s", outPath)))
	}

	_, err = out.Write(rootPEM)
	return err
}

func runCAInstall(out io.Writer, uninstall bool) error {
	ca, err := loadCAFromDataDir()
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
		if err := pkiadapter.UninstallRoot(cert); err != nil {
			return fmt.Errorf("failed to uninstall root CA: %w", err)
		}
		return cliWriteLine(out, cliRenderSuccess("Root CA removed from system trust stores"))
	}

	if err := pkiadapter.InstallRoot(cert); err != nil {
		return fmt.Errorf("failed to install root CA: %w", err)
	}
	return cliWriteLine(out, cliRenderSuccess("Root CA installed in system trust stores (system, Firefox, Java)"))
}

func runCAInfo(out io.Writer) error {
	ca, err := loadCAFromDataDir()
	if err != nil {
		return err
	}

	interExpiry := ca.IntermediateExpiresAt()
	remaining := time.Until(interExpiry).Truncate(time.Minute)

	if err := cliWriteLine(out, cliRenderMeta("Root CA:", ca.RootCommonName())); err != nil {
		return err
	}
	if err := cliWriteLine(out, cliRenderMeta("Fingerprint:", "SHA256:"+ca.RootFingerprint())); err != nil {
		return err
	}
	return cliWriteLine(out, cliRenderMeta("Intermediate:", fmt.Sprintf("expires in %s (auto-renews)", remaining)))
}
