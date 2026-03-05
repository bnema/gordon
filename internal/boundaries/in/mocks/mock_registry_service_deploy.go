package mocks

// SuppressDeployEvent satisfies the admin handler's composite registry/deploy test dependency.
// Admin handler tests that only need registry behavior do not assert on deploy suppression.
func (_mock *MockRegistryService) SuppressDeployEvent(imageName string) {}

// ClearDeployEventSuppression satisfies the admin handler's composite registry/deploy test dependency.
// Admin handler tests that only need registry behavior do not assert on deploy suppression.
func (_mock *MockRegistryService) ClearDeployEventSuppression(imageName string) {}
