package cli

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
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

// caResolveRemote allows tests to override remote control-plane resolution.
// Tests that swap it must not run in parallel. It returns a nil ControlPlane when no remote is targeted.
var caResolveRemote = func() (ControlPlane, error) {
	client, isRemote, err := GetRemoteClient()
	if err != nil || !isRemote {
		return nil, err
	}
	return client, nil
}

// caDetails is the CA data the ca commands operate on, whether it was read
// from the local data directory or fetched from a remote Gordon.
type caDetails struct {
	rootPEM            []byte
	rootCN             string
	fingerprint        string
	intermediateExpiry time.Time
	// remote is true when the CA was fetched over the network.
	remote bool
}

func newCAExportCmd() *cobra.Command {
	var outPath string

	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export the root CA certificate",
		Long:  "Export Gordon's root CA certificate in PEM format for manual trust installation. When a remote is targeted (--remote, GORDON_REMOTE, or the active remote), the certificate is fetched from that Gordon.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			details, err := loadCADetails(cmd.Context())
			if err != nil {
				return err
			}
			if details.remote {
				// The checks prove a well-formed CA, not its origin: over an
				// insecure or plain-http remote the response can be swapped.
				if err := cliWriteLine(cmd.ErrOrStderr(), cliRenderWarning(fmt.Sprintf(
					"Before trusting this CA, check that fingerprint %s matches `gordon ca info` on the Gordon host", details.fingerprint))); err != nil {
					return err
				}
			}
			return runCAExport(cmd.OutOrStdout(), details, outPath)
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
		Long:  "Install Gordon's root CA certificate into the system, Firefox, and Java trust stores. Requires running as root on the Gordon host.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := rejectRemoteForCAInstall(); err != nil {
				return err
			}
			dataDir, err := resolveCADataDir(false)
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

// rejectRemoteForCAInstall refuses to run install/uninstall while a remote is
// targeted: trusting a network-fetched root is unsafe (insecure or plain-http
// remotes), sudo drops the user's remotes config, and uninstall would depend
// on the remote still being reachable.
func rejectRemoteForCAInstall() error {
	cp, err := caResolveRemote()
	if err != nil {
		return err
	}
	if cp != nil {
		return errCAInstallRemote
	}
	return nil
}

var errCAInstallRemote = errors.New("ca install only works on the Gordon host; from another machine run " +
	"`gordon ca export --remote <name> --out gordon-ca.pem` and install that file with your OS trust tools")

func newCAInfoCmd() *cobra.Command {
	var jsonOut bool

	cmd := &cobra.Command{
		Use:   "info",
		Short: "Show CA status information",
		RunE: func(cmd *cobra.Command, _ []string) error {
			details, err := loadCADetails(cmd.Context())
			if err != nil {
				return err
			}
			return runCAInfo(cmd.OutOrStdout(), details, jsonOut)
		},
	}

	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")

	return cmd
}

// loadCADetails reads the CA from the remote Gordon when --remote targets one,
// otherwise from the local server config and data directory.
func loadCADetails(ctx context.Context) (*caDetails, error) {
	cp, err := caResolveRemote()
	if err != nil {
		return nil, err
	}
	if cp != nil {
		return fetchRemoteCADetails(ctx, cp)
	}

	dataDir, err := resolveCADataDir(true)
	if err != nil {
		return nil, err
	}
	return loadLocalCADetails(dataDir)
}

// fetchRemoteCADetails fetches the CA from the remote and verifies it locally:
// the PEM must hold exactly one self-signed CA certificate, and the CN and
// fingerprint are computed from it rather than trusted from the response.
func fetchRemoteCADetails(ctx context.Context, cp ControlPlane) (*caDetails, error) {
	resp, err := cp.GetCA(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get CA from remote: %w", err)
	}
	if resp == nil || resp.RootPEM == "" {
		return nil, fmt.Errorf("failed to get CA from remote: empty response")
	}
	cert, err := parseRemoteRootCA([]byte(resp.RootPEM))
	if err != nil {
		return nil, fmt.Errorf("remote returned an invalid root CA: %w", err)
	}
	fingerprint := pkiadapter.CertFingerprint(cert)
	if resp.Fingerprint != "" && !strings.EqualFold(resp.Fingerprint, fingerprint) {
		return nil, fmt.Errorf("remote returned an invalid root CA: reported fingerprint %s does not match certificate fingerprint %s",
			resp.Fingerprint, fingerprint)
	}
	return &caDetails{
		rootPEM:            []byte(resp.RootPEM),
		rootCN:             cert.Subject.CommonName,
		fingerprint:        fingerprint,
		intermediateExpiry: resp.IntermediateExpiry,
		remote:             true,
	}, nil
}

func parseRemoteRootCA(rootPEM []byte) (*x509.Certificate, error) {
	block, rest := pem.Decode(rootPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("no CERTIFICATE PEM block found")
	}
	if extra, _ := pem.Decode(rest); extra != nil {
		return nil, errors.New("expected exactly one certificate, found more")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse certificate: %w", err)
	}
	if !cert.BasicConstraintsValid || !cert.IsCA {
		return nil, errors.New("certificate is not a CA")
	}
	if err := cert.CheckSignatureFrom(cert); err != nil {
		return nil, fmt.Errorf("certificate is not validly self-signed: %w", err)
	}
	return cert, nil
}

// resolveCADataDir returns the local data directory. remoteHint adds the
// --remote suggestion for commands that support remotes.
func resolveCADataDir(remoteHint bool) (string, error) {
	local, err := GetLocalServices(cliConfigPath)
	if err != nil {
		return "", err
	}
	if !local.HasInternalTLS() {
		return "", errLocalInternalTLSDisabled(local.GetConfigFile(), remoteHint)
	}
	return local.GetDataDir(), nil
}

func errLocalInternalTLSDisabled(configFile string, remoteHint bool) error {
	source := "no local server config file was found (searched /etc/gordon, ~/.config/gordon and the current directory)"
	if configFile != "" {
		source = fmt.Sprintf("local server config %s was read", configFile)
	}
	hint := ""
	if remoteHint {
		hint = "; to use the CA of a remote Gordon, pass --remote <name|url>"
	}
	return fmt.Errorf("internal TLS is disabled: %s and it has no TLS-capable entrypoint (smart_tcp or tls_mux)%s", source, hint)
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

func loadLocalCADetails(dataDir string) (*caDetails, error) {
	ca, err := loadCAFromDataDir(dataDir)
	if err != nil {
		return nil, err
	}
	return &caDetails{
		rootPEM:            ca.RootCertificate(),
		rootCN:             ca.RootCommonName(),
		fingerprint:        ca.RootFingerprint(),
		intermediateExpiry: ca.IntermediateExpiresAt(),
	}, nil
}

func runCAExport(out io.Writer, details *caDetails, outPath string) error {
	if outPath != "" {
		if err := os.MkdirAll(filepath.Dir(outPath), 0750); err != nil {
			return fmt.Errorf("create directory for %s: %w", outPath, err)
		}
		if err := os.WriteFile(outPath, details.rootPEM, 0600); err != nil {
			return fmt.Errorf("write certificate to %s: %w", outPath, err)
		}
		return cliWriteLine(out, cliRenderSuccess(fmt.Sprintf("Root CA certificate written to %s", outPath)))
	}

	_, err := out.Write(details.rootPEM)
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

func runCAInfo(out io.Writer, details *caDetails, jsonOut bool) error {
	remaining := time.Until(details.intermediateExpiry).Truncate(time.Minute)

	if jsonOut {
		return writeJSON(out, map[string]any{
			"root_cn":             details.rootCN,
			"fingerprint":         "SHA256:" + details.fingerprint,
			"intermediate_expiry": details.intermediateExpiry.Format(time.RFC3339),
			"intermediate_ttl":    remaining.String(),
		})
	}

	if err := cliWriteLine(out, cliRenderMeta("Root CA:", details.rootCN)); err != nil {
		return err
	}
	if err := cliWriteLine(out, cliRenderMeta("Fingerprint:", "SHA256:"+details.fingerprint)); err != nil {
		return err
	}
	return cliWriteLine(out, cliRenderMeta("Intermediate:", fmt.Sprintf("expires in %s (auto-renews)", remaining)))
}
