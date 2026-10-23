package cli

import (
	"context"
	"fmt"

	"github.com/bnema/gordon/internal/adapters/in/cli/remote"
)

// oidcAudienceFlag is --oidc-audience: the audience requested from the CI
// runner's ID token endpoint (overrides GORDON_OIDC_AUDIENCE).
var oidcAudienceFlag string

// newOIDCClient builds the OIDC-mode client; tests swap it to trust a TLS
// test server.
var newOIDCClient = remote.NewClient

// resolveOIDCRemote returns the explicit remote and an OIDC-mode client
// when the environment provides a workload identity (GORDON_OIDC_TOKEN or
// the runner's ACTIONS_ID_TOKEN_REQUEST_* variables). It returns a nil
// client in stored-token mode. OIDC mode requires an explicit remote: the
// CI host URL, over https: CI credentials never travel in plaintext.
func resolveOIDCRemote() (*remote.ResolvedRemote, *remote.Client, error) {
	source, err := remote.ResolveOIDCSource(oidcAudienceFlag)
	if err != nil || source == nil {
		return nil, nil, err
	}
	resolved, isRemote, err := remote.ResolveStrict(remoteFlag, tokenFlag, insecureTLSFlag)
	if err != nil {
		return nil, nil, err
	}
	if !isRemote {
		return nil, nil, fmt.Errorf("an OIDC identity is set but no remote is selected: pass --remote with the Gordon CI host URL")
	}
	if err := remote.CheckCIRemote(resolved.URL); err != nil {
		return nil, nil, err
	}
	opts := []remote.ClientOption{remote.WithOIDC(source)}
	if resolved.InsecureTLS {
		opts = append(opts, remote.WithInsecureTLS(true))
	}
	return resolved, newOIDCClient(resolved.URL, opts...), nil
}

// ciRegistryAuth returns the Authorization header pushing repository
// through the CI host needs: a ci-access token scoped to it.
func ciRegistryAuth(ctx context.Context, client *remote.Client, repository string) (string, error) {
	token, err := client.ExchangeCIToken(ctx, "repository:"+repository+":push,pull")
	if err != nil {
		return "", err
	}
	return "Bearer " + token, nil
}
