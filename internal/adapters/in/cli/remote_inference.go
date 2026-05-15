package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/bnema/gordon/internal/adapters/in/cli/remote"
	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/pkg/validation"
)

type remoteInferenceProbe func(context.Context, *remote.Client) (bool, error)

func inferPushRemote(ctx context.Context, imageArg, domainFlag, dockerfile string) (*remote.ResolvedRemote, error) {
	if domainFlag != "" {
		return inferRemoteForRouteDomain(ctx, domainFlag)
	}

	if imageArg == "" {
		detected, err := detectImageName(dockerfile)
		if err != nil {
			return nil, err
		}
		imageArg = detected
	}

	if looksLikeLegacyDomain(imageArg) {
		lookupImage := imageArg
		domainLookupArg := imageArg
		if parsedImage, ref := validation.ParseImageReference(imageArg); parsedImage != imageArg {
			domainLookupArg = parsedImage
			if !strings.HasPrefix(ref, "sha256:") {
				lookupImage = parsedImage
			}
		}

		resolved, err := inferRemoteForImage(ctx, lookupImage)
		if err != nil || resolved != nil {
			return resolved, err
		}

		return inferRemoteForRouteDomain(ctx, domainLookupArg)
	}

	classified := classifyPushArgument(imageArg)
	return inferRemoteForImage(ctx, classified.lookupImage)
}

func inferRemoteForImage(ctx context.Context, imageName string) (*remote.ResolvedRemote, error) {
	return inferSavedRemote(ctx, "image", imageName, func(ctx context.Context, client *remote.Client) (bool, error) {
		routes, err := client.FindRoutesByImage(ctx, imageName)
		if err != nil {
			return false, err
		}
		return len(filterPreviewRoutes(routes)) > 0, nil
	})
}

func inferRemoteForRouteDomain(ctx context.Context, routeDomain string) (*remote.ResolvedRemote, error) {
	return inferSavedRemote(ctx, "route", routeDomain, func(ctx context.Context, client *remote.Client) (bool, error) {
		_, err := client.GetRoute(ctx, routeDomain)
		if err != nil {
			if isRemoteNotFoundError(err) {
				return false, nil
			}
			return false, err
		}
		return true, nil
	})
}

func inferRemoteForAttachmentImage(ctx context.Context, imageName string) (*remote.ResolvedRemote, error) {
	return inferSavedRemote(ctx, "attachment image", imageName, func(ctx context.Context, client *remote.Client) (bool, error) {
		targets, err := client.FindAttachmentTargetsByImage(ctx, imageName)
		if err != nil {
			return false, err
		}
		return len(targets) > 0, nil
	})
}

func inferRemoteForAttachmentTarget(ctx context.Context, target string) (*remote.ResolvedRemote, error) {
	return inferSavedRemote(ctx, "attachment target", target, func(ctx context.Context, client *remote.Client) (bool, error) {
		images, err := client.GetAttachmentsConfig(ctx, target)
		if err == nil {
			return len(images) > 0, nil
		}
		if !isRemoteNotFoundError(err) {
			return false, err
		}

		_, err = client.GetRoute(ctx, target)
		if err != nil {
			if isRemoteNotFoundError(err) {
				return false, nil
			}
			return false, err
		}
		return true, nil
	})
}

func inferRemoteForRepository(ctx context.Context, repository string) (*remote.ResolvedRemote, error) {
	return inferSavedRemote(ctx, "repository", repository, func(ctx context.Context, client *remote.Client) (bool, error) {
		tags, err := client.ListTags(ctx, repository)
		if err != nil {
			if isRemoteNotFoundError(err) {
				return false, nil
			}
			return false, err
		}
		return len(tags) > 0, nil
	})
}

func inferSavedRemote(ctx context.Context, targetKind, target string, probe remoteInferenceProbe) (*remote.ResolvedRemote, error) {
	remotes, ok := loadInferenceCandidateRemotes()
	if !ok {
		return nil, nil
	}

	names := sortedRemoteNames(remotes)
	matches := make([]*remote.ResolvedRemote, 0, len(names))
	probeFailures := make([]string, 0)

	for _, name := range names {
		entry := remotes[name]
		matched, err := probe(ctx, newRoutesTargetClient(name, entry))
		if err != nil {
			probeFailures = append(probeFailures, fmt.Sprintf("%s (%v)", name, err))
			continue
		}
		if matched {
			matches = append(matches, resolvedRemoteFromEntry(name, entry))
		}
	}

	if len(probeFailures) > 0 {
		return nil, fmt.Errorf("could not safely infer remote for %s %q because probing failed for: %s; use --remote", targetKind, target, strings.Join(probeFailures, ", "))
	}

	if len(matches) > 1 {
		names := make([]string, 0, len(matches))
		for _, match := range matches {
			names = append(names, match.DisplayName())
		}
		return nil, fmt.Errorf("multiple saved remotes match %s %q: %s; use --remote", targetKind, target, strings.Join(names, ", "))
	}

	if len(matches) == 1 {
		return matches[0], nil
	}

	return nil, nil
}

func loadInferenceCandidateRemotes() (map[string]remote.RemoteEntry, bool) {
	if _, ok := resolveRoutesExplicitTarget(); ok {
		return nil, false
	}

	remotes, err := remote.LoadRemotes("")
	if err != nil || remotes == nil {
		return nil, false
	}
	if remotes.Active != "" {
		return nil, false
	}
	if len(remotes.Remotes) == 0 {
		return nil, false
	}

	return remotes.Remotes, true
}

func resolvedRemoteFromEntry(name string, entry remote.RemoteEntry) *remote.ResolvedRemote {
	return &remote.ResolvedRemote{
		Name:        name,
		URL:         entry.URL,
		Token:       resolveRoutesTokenForTarget(name, entry),
		InsecureTLS: resolveRoutesInsecureForTarget(name, entry),
	}
}

func isRemoteNotFoundError(err error) bool {
	var httpErr *remote.HTTPError
	if errors.As(err, &httpErr) {
		return httpErr.StatusCode == http.StatusNotFound
	}
	return errors.Is(err, domain.ErrRouteNotFound)
}

func filterPreviewRoutes(routes []domain.Route) []domain.Route {
	filtered := routes[:0]
	for _, route := range routes {
		if !strings.Contains(route.Domain, domain.DefaultPreviewSeparator) {
			filtered = append(filtered, route)
		}
	}
	return filtered
}
