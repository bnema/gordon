package app

import (
	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/usecase/deployment"
)

// publishPromotion publishes one promotion generation: the image-source
// policy (which names the CI host as an installation alias), the snapshot
// read by the CI router, the policy view the deployment engine authorizes
// promotions with, and the trusted issuers of workload identity. Startup
// and reload both call it with validated values only, so a rejected config
// never reaches any consumer. The image policy is published first: once the
// CI host routes, its references are already installation images.
func (svc *services) publishPromotion(snapshot PromotionSnapshot, imagePolicy domain.ImageSourcePolicy, cfg Config) {
	imagePolicy = imagePolicy.Clone()
	svc.imagePolicy.Store(&imagePolicy)
	svc.promotionSnapshot.Store(&snapshot)
	if svc.appDeploySvc != nil {
		svc.appDeploySvc.SetPromotionConfig(deployment.PromotionConfig{
			Policies:      snapshot.Policies(),
			RegistryHosts: promotionRegistryHosts(cfg),
		})
	}
	svc.reloadWorkloadIdentity()
}

// promotionRegistryHosts lists every host a promotion reference may name for
// this installation besides the registry domain: the management domain, the
// CI domain, and the legacy registry domains.
func promotionRegistryHosts(cfg Config) []string {
	hosts := []string{cfg.Server.GordonDomain, cfg.Server.CIDomain}
	return append(hosts, cfg.Server.LegacyRegistryDomains...)
}
