package domain

const DefaultDeployFailureSummary = "failed to deploy"

// DeployFailureError describes a surfaced deployment failure.
type DeployFailureError struct {
	Summary       string
	Cause         string
	Hint          string
	Logs          []string
	ContainerName string
	ContainerID   string
	Err           error
}

// Error returns the deploy failure summary.
func (e *DeployFailureError) Error() string {
	if e == nil || e.Summary == "" {
		return DefaultDeployFailureSummary
	}

	return e.Summary
}

// Unwrap returns the wrapped error.
func (e *DeployFailureError) Unwrap() error {
	if e == nil {
		return nil
	}

	return e.Err
}
