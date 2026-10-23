package app

import (
	"time"

	"github.com/bnema/gordon/internal/adapters/out/oidc"
	"github.com/bnema/gordon/internal/usecase/workloadauth"
)

// initWorkloadIdentity builds the OIDC workload identity verifier and feeds it
// the trusted issuers of the startup promotion snapshot. Key fetching runs in
// the background, so an unreachable issuer never delays boot.
func (si *serviceInit) initWorkloadIdentity() {
	si.svc.workloadIdentitySvc = workloadauth.NewService(si.ctx, oidc.NewProvider(), time.Now)
	si.svc.reloadWorkloadIdentity()
}

// reloadWorkloadIdentity publishes the current snapshot's trusted issuers.
func (s *services) reloadWorkloadIdentity() {
	if s.workloadIdentitySvc != nil {
		s.workloadIdentitySvc.SetIssuers(s.PromotionSnapshot().Issuers())
	}
}
