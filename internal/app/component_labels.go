package app

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/bnema/gordon/internal/domain"
)

var componentLabelValue = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
var componentDesiredStateHash = regexp.MustCompile(`^[a-f0-9]{64}$`)

// ComponentLabelRequest contains the stable identity of one Gordon component
// generation. It deliberately contains no environment or credential material.
type ComponentLabelRequest struct {
	Role             domain.ComponentRole
	Version          string
	Generation       uint64
	MigrationID      string
	Owner            string
	DesiredStateHash string
}

// BuildComponentLabels produces the complete, stable label set used to find a
// prepared component on retry. Validation rejects ambiguous values rather than
// creating labels which cannot be safely selected by the runtime.
func BuildComponentLabels(request ComponentLabelRequest) (map[string]string, error) {
	if !domain.IsKnownComponentRole(request.Role) {
		return nil, fmt.Errorf("invalid component role")
	}
	if request.Generation == 0 {
		return nil, fmt.Errorf("component generation is required")
	}
	for _, value := range []string{request.Version, request.MigrationID, request.Owner} {
		if !componentLabelValue.MatchString(value) {
			return nil, fmt.Errorf("invalid component label value")
		}
	}
	if !componentDesiredStateHash.MatchString(request.DesiredStateHash) {
		return nil, fmt.Errorf("invalid component desired state hash")
	}
	return map[string]string{
		domain.LabelComponent:                 "true",
		domain.LabelComponentRole:             string(request.Role),
		domain.LabelComponentVersion:          request.Version,
		domain.LabelComponentGeneration:       strconv.FormatUint(request.Generation, 10),
		domain.LabelComponentMigrationID:      request.MigrationID,
		domain.LabelComponentOwner:            request.Owner,
		domain.LabelComponentDesiredStateHash: strings.ToLower(request.DesiredStateHash),
	}, nil
}
