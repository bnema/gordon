package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/bnema/gordon/internal/adapters/in/cli/remote"
	"github.com/bnema/gordon/internal/domain"
)

type remoteInferenceProbe func(context.Context, *remote.Client) (bool, error)

func inferPushRemote(ctx context.Context, imageArg, _, dockerfile string) (*remote.ResolvedRemote, error) {
	if imageArg == "" {
		detected, err := detectImageName(dockerfile)
		if err != nil {
			return nil, err
		}
		imageArg = detected
	}
	classified := classifyPushArgument(imageArg)
	return inferRemoteForImage(ctx, classified.repository)
}

func inferRemoteForImage(ctx context.Context, imageName string) (*remote.ResolvedRemote, error) {
	return inferSavedRemote(ctx, "image", imageName, func(ctx context.Context, client *remote.Client) (bool, error) {
		tags, err := client.ListTags(ctx, imageName)
		if err != nil {
			if isRemoteNotFoundError(err) {
				return false, nil
			}
			return false, err
		}
		return len(tags) > 0, nil
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
		matched, err := probe(ctx, newInferenceTargetClient(name, entry))
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
	if _, ok := resolveExplicitTargetName(); ok {
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
		Token:       resolveTokenForTarget(name, entry),
		InsecureTLS: resolveInsecureForTarget(name, entry),
	}
}

func isRemoteNotFoundError(err error) bool {
	if httpErr, ok := errors.AsType[*remote.HTTPError](err); ok {
		return httpErr.StatusCode == http.StatusNotFound
	}
	return errors.Is(err, domain.ErrRouteNotFound)
}

func resolveExplicitRemote() (*remote.ResolvedRemote, bool, error) {
	target, ok := resolveExplicitTargetName()
	if !ok {
		return nil, false, nil
	}

	if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") {
		return &remote.ResolvedRemote{
			URL:         target,
			Token:       resolveTokenForTarget("", remote.RemoteEntry{}),
			InsecureTLS: resolveInsecureForTarget("", remote.RemoteEntry{}),
		}, true, nil
	}

	remotes, err := remote.LoadRemotes("")
	if err != nil {
		return nil, false, err
	}

	if remotes != nil {
		if entry, found := remotes.Remotes[target]; found {
			return &remote.ResolvedRemote{
				Name:        target,
				URL:         entry.URL,
				Token:       resolveTokenForTarget(target, entry),
				InsecureTLS: resolveInsecureForTarget(target, entry),
			}, true, nil
		}
	}

	return nil, false, nil
}

func resolveExplicitTargetName() (string, bool) {
	if target := strings.TrimSpace(remoteFlag); target != "" {
		return target, true
	}
	if target := strings.TrimSpace(os.Getenv("GORDON_REMOTE")); target != "" {
		return target, true
	}
	return "", false
}

func resolveTokenForTarget(name string, entry remote.RemoteEntry) string {
	if token := strings.TrimSpace(tokenFlag); token != "" {
		return token
	}
	if token := strings.TrimSpace(os.Getenv("GORDON_TOKEN")); token != "" {
		return token
	}
	if name != "" {
		return remote.ResolveTokenForRemote(name, entry)
	}
	return ""
}

func resolveInsecureForTarget(name string, entry remote.RemoteEntry) bool {
	if insecureTLSFlag {
		return true
	}
	if env := strings.TrimSpace(os.Getenv("GORDON_INSECURE")); env != "" {
		if value, err := strconv.ParseBool(env); err == nil {
			return value
		}
	}
	if name != "" {
		return entry.InsecureTLS
	}
	return false
}

func newInferenceTargetClient(name string, entry remote.RemoteEntry) *remote.Client {
	return remote.NewClient(entry.URL, remoteClientOptions(resolveTokenForTarget(name, entry), resolveInsecureForTarget(name, entry))...)
}

// inferExplicitTarget resolves --remote/GORDON_REMOTE without probing saved remotes.
func inferExplicitTarget(_ context.Context) (*remote.ResolvedRemote, error) {
	resolved, ok, err := resolveExplicitRemote()
	if err != nil || !ok {
		return nil, err
	}
	return resolved, nil
}
