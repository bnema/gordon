package auth

import (
	"fmt"
	"strings"

	"github.com/bnema/gordon/pkg/validation"
)

// validateCIScopes accepts only repository:<name>:<actions> scopes where
// <actions> is a comma list of push and pull. CI tokens never carry admin,
// catalog, wildcard or delete permissions.
func validateCIScopes(scopes []string) error {
	for _, scope := range scopes {
		parts := strings.Split(scope, ":")
		if len(parts) != 3 || parts[0] != "repository" || validation.ValidateRepositoryName(parts[1]) != nil {
			return fmt.Errorf("invalid ci-access scope %q", scope)
		}
		for _, action := range strings.Split(parts[2], ",") {
			if action != "push" && action != "pull" {
				return fmt.Errorf("invalid ci-access scope %q", scope)
			}
		}
	}
	return nil
}
