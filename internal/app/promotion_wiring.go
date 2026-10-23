package app

import (
	"github.com/bnema/gordon/internal/usecase/deployment"
)

// publishPromotion atomically publishes one promotion generation: the
// snapshot read by the CI router, the policy view the deployment engine
// authorizes promotions with, and the trusted issuers of workload identity.
// Startup and reload both call it, so a rejected config never reaches any
// consumer.
func (svc *services) publishPromotion(snapshot PromotionSnapshot, cfg Config) {
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
