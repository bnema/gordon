package domain

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
		return "failed to deploy"
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
