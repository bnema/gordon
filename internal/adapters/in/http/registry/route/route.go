// Package route parses Docker Registry V2 request paths into one validated
// operation shared by authorization and dispatch. Authorization and the
// handler must never derive the repository independently: a mismatch lets a
// request be authorized for one repository and served from another.
package route

import (
	"errors"
	"fmt"
	"strings"

	"github.com/bnema/gordon/pkg/validation"
)

// Parsing errors. ErrNotFound means the path is not a registry route;
// ErrInvalid means the route shape is recognized but a component is
// malformed (bad repository, reference, digest, or upload id).
var (
	ErrNotFound = errors.New("registry route not found")
	ErrInvalid  = errors.New("registry route invalid")
)

// Registry error codes for malformed components, matching the Docker
// Registry V2 error format the handler returns.
const (
	CodeNameInvalid       = "NAME_INVALID"
	CodeTagInvalid        = "TAG_INVALID"
	CodeDigestInvalid     = "DIGEST_INVALID"
	CodeBlobUploadInvalid = "BLOB_UPLOAD_INVALID"
)

// ParseError is a malformed-component error carrying the registry error
// code the handler must return. It wraps ErrInvalid for errors.Is.
type ParseError struct {
	Code string
	Err  error
}

// Error implements error.
func (e *ParseError) Error() string { return e.Err.Error() }

// Unwrap exposes the wrapped sentinel.
func (e *ParseError) Unwrap() error { return ErrInvalid }

// invalid builds a ParseError for a malformed component.
func invalid(code, format string, args ...any) error {
	return &ParseError{Code: code, Err: fmt.Errorf(format, args...)}
}

// Kind classifies a registry operation.
type Kind string

// Registry operation kinds.
const (
	KindBase     Kind = "base"
	KindCatalog  Kind = "catalog"
	KindManifest Kind = "manifest"
	KindBlob     Kind = "blob"
	KindUpload   Kind = "upload"
	KindTagList  Kind = "tags"
)

const (
	manifestsSegment = "/manifests/"
	blobsSegment     = "/blobs/"
	uploadsSegment   = "/blobs/uploads/"
	tagsSegment      = "/tags/list"
)

// Operation is one parsed registry request. Repository is the exact
// repository the request addresses (empty for protocol roots), so callers
// authorize and dispatch on the same value.
type Operation struct {
	Kind       Kind
	Repository string
	// Reference is the manifest tag or digest (manifest kind).
	Reference string
	// Digest is the blob digest (blob kind).
	Digest string
	// UploadID is the upload session id; empty marks a new upload (upload kind).
	UploadID string
}

// RequiresRepositoryAuth reports whether the operation must be authorized
// against a repository scope. Only protocol roots are exempt; every route
// that names a repository is scoped.
func (o Operation) RequiresRepositoryAuth() bool {
	return o.Kind != KindBase && o.Kind != KindCatalog
}

// Parse classifies a registry request path. It resolves the repository
// from the LAST route marker, matching the served route exactly, and
// validates every component with the shared validation grammar.
func Parse(path string) (Operation, error) {
	if path == "/v2" || path == "/v2/" {
		return Operation{Kind: KindBase}, nil
	}
	if !strings.HasPrefix(path, "/v2/") {
		return Operation{}, ErrNotFound
	}
	rest := strings.TrimPrefix(path, "/v2/")
	switch rest {
	case "":
		return Operation{Kind: KindBase}, nil
	case "_catalog":
		return Operation{Kind: KindCatalog}, nil
	}

	parsers := map[Kind]func(string) (Operation, bool, error){
		KindTagList:  parseTags,
		KindUpload:   parseUpload,
		KindManifest: parseManifest,
		KindBlob:     parseBlob,
	}
	markers := []struct {
		segment string
		kind    Kind
	}{
		{tagsSegment, KindTagList},
		{uploadsSegment, KindUpload},
		{manifestsSegment, KindManifest},
		{blobsSegment, KindBlob},
	}
	// The served route is the marker that appears LAST in the path, so a
	// reserved-looking segment earlier in the path can never shorten or
	// redirect the repository.
	best := -1
	bestKind := Kind("")
	for _, marker := range markers {
		if idx := strings.LastIndex(rest, marker.segment); idx > best {
			best = idx
			bestKind = marker.kind
		}
	}
	if parse, ok := parsers[bestKind]; ok {
		if op, matched, err := parse(rest); matched {
			return op, err
		}
	}
	return Operation{}, ErrNotFound
}

// parseTags matches /{repo}/tags/list.
func parseTags(rest string) (Operation, bool, error) {
	repo, ok := strings.CutSuffix(rest, tagsSegment)
	if !ok {
		return Operation{}, false, nil
	}
	op, err := repositoryOperation(KindTagList, repo)
	return op, true, err
}

// parseUpload matches /{repo}/blobs/uploads/{uuid?}.
func parseUpload(rest string) (Operation, bool, error) {
	idx := strings.LastIndex(rest, uploadsSegment)
	if idx < 0 {
		return Operation{}, false, nil
	}
	op, err := repositoryOperation(KindUpload, rest[:idx])
	if err != nil {
		return Operation{}, true, err
	}
	uploadID := rest[idx+len(uploadsSegment):]
	if uploadID == "" {
		return op, true, nil
	}
	if err := validation.ValidateUUID(uploadID); err != nil {
		return Operation{}, true, invalid(CodeBlobUploadInvalid, "%s", err)
	}
	op.UploadID = uploadID
	return op, true, nil
}

// parseManifest matches /{repo}/manifests/{reference}.
func parseManifest(rest string) (Operation, bool, error) {
	idx := strings.LastIndex(rest, manifestsSegment)
	if idx < 0 {
		return Operation{}, false, nil
	}
	reference := rest[idx+len(manifestsSegment):]
	if reference == "" || strings.Contains(reference, "/") {
		return Operation{}, true, ErrNotFound
	}
	if err := validation.ValidateReference(reference); err != nil {
		return Operation{}, true, invalid(CodeTagInvalid, "%s", err)
	}
	op, err := repositoryOperation(KindManifest, rest[:idx])
	if err != nil {
		return Operation{}, true, err
	}
	op.Reference = reference
	return op, true, nil
}

// parseBlob matches /{repo}/blobs/{digest}.
func parseBlob(rest string) (Operation, bool, error) {
	idx := strings.LastIndex(rest, blobsSegment)
	if idx < 0 {
		return Operation{}, false, nil
	}
	digest := rest[idx+len(blobsSegment):]
	if digest == "" || strings.Contains(digest, "/") {
		return Operation{}, true, ErrNotFound
	}
	if err := validation.ValidateDigest(digest); err != nil {
		return Operation{}, true, invalid(CodeDigestInvalid, "%s", err)
	}
	op, err := repositoryOperation(KindBlob, rest[:idx])
	if err != nil {
		return Operation{}, true, err
	}
	op.Digest = digest
	return op, true, nil
}

// repositoryOperation builds a repository-scoped operation, rejecting an
// empty or malformed repository so a reserved-looking segment can never be
// mistaken for a protocol root.
func repositoryOperation(kind Kind, repo string) (Operation, error) {
	if repo == "" || strings.HasSuffix(repo, "/") {
		return Operation{}, ErrNotFound
	}
	if err := validation.ValidateRepositoryName(repo); err != nil {
		return Operation{}, invalid(CodeNameInvalid, "%s", err)
	}
	return Operation{Kind: kind, Repository: repo}, nil
}
