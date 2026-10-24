package apps

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/bnema/gordon/internal/domain"
)

// setImageRevertWalkLimit bounds how many superseded revisions one service
// lookup visits. Revision retention keeps far fewer, so the bound only stops
// a corrupt (cyclic) Supersedes chain.
const setImageRevertWalkLimit = 64

// detectSetImageReverts lists the services whose manifest image differs from
// the current desired image when that desired image was introduced by a
// set-image revision. A service only in one of the specs is never a revert.
// A history that cannot be followed (missing revision, walk limit) is not
// flagged.
func detectSetImageReverts(ctx context.Context, store revisionLoader, desired domain.AppDesiredRevision, spec domain.AppSpec) ([]domain.AppSetImageRevert, error) {
	var reverts []domain.AppSetImageRevert
	for _, fileSvc := range spec.Services {
		serverSvc, ok := specService(desired.Spec, fileSvc.Name)
		if !ok || serverSvc.Image == fileSvc.Image {
			continue
		}
		origin, found, err := imageOrigin(ctx, store, desired, fileSvc.Name)
		if err != nil {
			return nil, err
		}
		if !found || origin.SetImage == nil || origin.SetImage.Service != fileSvc.Name {
			continue
		}
		reverts = append(reverts, domain.AppSetImageRevert{
			Service:     fileSvc.Name,
			ServerImage: serverSvc.Image,
			FileImage:   fileSvc.Image,
			Revision:    origin.Revision,
			Principal:   origin.SetImage.Principal,
			ChangedAt:   origin.AcceptedAt,
		})
	}
	sort.Slice(reverts, func(i, j int) bool { return reverts[i].Service < reverts[j].Service })
	return reverts, nil
}

// imageOrigin walks back from current through Supersedes to the revision that
// introduced the service's current image. A set-image revision for the service
// always changes its image, so it is the origin as soon as it is reached.
// found is false when the history ends at a missing revision or the walk limit.
func imageOrigin(ctx context.Context, store revisionLoader, current domain.AppDesiredRevision, service string) (domain.AppDesiredRevision, bool, error) {
	svc, _ := specService(current.Spec, service)
	image := svc.Image
	for range setImageRevertWalkLimit {
		if current.SetImage != nil && current.SetImage.Service == service {
			return current, true, nil
		}
		if current.Supersedes == "" {
			return current, true, nil
		}
		parent, err := store.LoadRevision(ctx, current.App, current.Supersedes)
		if errors.Is(err, domain.ErrAppRevisionNotFound) {
			return domain.AppDesiredRevision{}, false, nil
		}
		if err != nil {
			return domain.AppDesiredRevision{}, false, fmt.Errorf("apps: load revision %s: %w", current.Supersedes, err)
		}
		parentSvc, ok := specService(parent.Spec, service)
		if !ok || parentSvc.Image != image {
			return current, true, nil
		}
		current = parent
	}
	return domain.AppDesiredRevision{}, false, nil
}

func specService(spec domain.AppSpec, name string) (domain.AppService, bool) {
	for _, svc := range spec.Services {
		if svc.Name == name {
			return svc, true
		}
	}
	return domain.AppService{}, false
}
