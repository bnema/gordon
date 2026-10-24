package app

import (
	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/usecase/deployment"
)

// publishSetImage publishes one set-image generation: the image-source
// policy (which names the CI host as an installation alias), the snapshot
// read by the CI router, the policy view the deployment engine authorizes
// image changes with, and the trusted issuers of workload identity. Startup
// and reload both call it with validated values only, so a rejected config
// never reaches any consumer. The image policy is published first: once the
// CI host routes, its references are already installation images.
func (svc *services) publishSetImage(snapshot SetImageSnapshot, imagePolicy domain.ImageSourcePolicy, cfg Config) {
	imagePolicy = imagePolicy.Clone()
	svc.imagePolicy.Store(&imagePolicy)
	svc.setImageSnapshot.Store(&snapshot)
	if svc.appDeploySvc != nil {
		svc.appDeploySvc.SetSetImageConfig(deployment.SetImageConfig{
			Policies:      snapshot.Policies(),
			RegistryHosts: setImageRegistryHosts(cfg),
		})
	}
	svc.reloadWorkloadIdentity()
}

// setImageRegistryHosts lists every host a set-image reference may name for
// this installation besides the registry domain: the management domain, the
// CI domain, and the legacy registry domains.
func setImageRegistryHosts(cfg Config) []string {
	hosts := []string{cfg.Server.GordonDomain, cfg.Server.CIDomain}
	return append(hosts, cfg.Server.LegacyRegistryDomains...)
}
