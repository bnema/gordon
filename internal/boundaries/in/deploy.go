package in

// DeployCoordinator defines deploy coordination operations.
//
// These methods let the CLI/admin API suppress image-pushed deploy events
// while an explicit deploy flow is in progress.
type DeployCoordinator interface {
	SuppressDeployEvent(imageName string)
	ClearDeployEventSuppression(imageName string)
}
