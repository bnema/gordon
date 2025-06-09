package mocks

import (
	"io"
	"github.com/stretchr/testify/mock"
)

// MockStorage is a mock implementation of registry.Storage
type MockStorage struct {
	mock.Mock
}

// Manifest operations
func (m *MockStorage) GetManifest(name, reference string) ([]byte, string, error) {
	args := m.Called(name, reference)
	if args.Get(0) == nil {
		return nil, "", args.Error(2)
	}
	return args.Get(0).([]byte), args.String(1), args.Error(2)
}

func (m *MockStorage) PutManifest(name, reference, contentType string, data []byte) error {
	args := m.Called(name, reference, contentType, data)
	return args.Error(0)
}

func (m *MockStorage) DeleteManifest(name, reference string) error {
	args := m.Called(name, reference)
	return args.Error(0)
}

// Blob operations
func (m *MockStorage) GetBlob(digest string) (io.ReadCloser, error) {
	args := m.Called(digest)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(io.ReadCloser), args.Error(1)
}

func (m *MockStorage) GetBlobPath(digest string) (string, error) {
	args := m.Called(digest)
	return args.String(0), args.Error(1)
}

func (m *MockStorage) PutBlob(digest string, data io.Reader, size int64) error {
	args := m.Called(digest, data, size)
	return args.Error(0)
}

func (m *MockStorage) DeleteBlob(digest string) error {
	args := m.Called(digest)
	return args.Error(0)
}

func (m *MockStorage) BlobExists(digest string) bool {
	args := m.Called(digest)
	return args.Bool(0)
}

// Upload operations
func (m *MockStorage) StartBlobUpload(name string) (string, error) {
	args := m.Called(name)
	return args.String(0), args.Error(1)
}

func (m *MockStorage) AppendBlobChunk(name, uuid string, chunk []byte) (int64, error) {
	args := m.Called(name, uuid, chunk)
	return args.Get(0).(int64), args.Error(1)
}

func (m *MockStorage) GetBlobUpload(uuid string) (io.WriteCloser, error) {
	args := m.Called(uuid)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(io.WriteCloser), args.Error(1)
}

func (m *MockStorage) FinishBlobUpload(uuid, digest string) error {
	args := m.Called(uuid, digest)
	return args.Error(0)
}

func (m *MockStorage) CancelBlobUpload(uuid string) error {
	args := m.Called(uuid)
	return args.Error(0)
}

// Tag operations
func (m *MockStorage) ListTags(name string) ([]string, error) {
	args := m.Called(name)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]string), args.Error(1)
}

// Repository operations
func (m *MockStorage) ListRepositories() ([]string, error) {
	args := m.Called()
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]string), args.Error(1)
}