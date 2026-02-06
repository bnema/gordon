# Attachment Secrets CLI Support — Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Allow `gordon secrets set` and `gordon secrets remove` to target attachment containers via an `--attachment` flag, closing issue #56.

**Architecture:** The storage layer (`DomainSecretStore.SetAttachment`) and file format (`gordon-{container}.env`) already exist. The gap is a missing `DeleteAttachment` in the store, missing attachment methods on the `SecretService` use case, a missing admin API endpoint for attachment secrets, and missing `--attachment` flag on CLI commands. We thread through all layers: storage → use case → admin API → remote client → CLI.

**Tech Stack:** Go, cobra (CLI), mockery (mock generation), httptest

---

### Task 1: Add `DeleteAttachment` to storage interface and implementation

The `DomainSecretStore` has `SetAttachment` but no `DeleteAttachment`. We need it to remove individual keys from attachment env files.

**Files:**
- Modify: `internal/boundaries/out/domain_secrets.go:13-36`
- Modify: `internal/adapters/out/domainsecrets/store.go` (add method after `SetAttachment`)
- Test: `internal/adapters/out/domainsecrets/store_test.go`

**Step 1: Write the failing test**

Add to `store_test.go`, near the existing `SetAttachment` tests (~line 229):

```go
func TestFileStore_DeleteAttachment(t *testing.T) {
	dir := t.TempDir()
	store := NewFileStore(dir, testLogger())
	containerName := "app__example__com-postgres"

	// Seed with two secrets
	err := store.SetAttachment(containerName, map[string]string{
		"POSTGRES_USER":     "admin",
		"POSTGRES_PASSWORD": "secret",
	})
	require.NoError(t, err)

	t.Run("deletes single key", func(t *testing.T) {
		err := store.DeleteAttachment(containerName, "POSTGRES_PASSWORD")
		require.NoError(t, err)

		secrets, err := store.GetAllAttachment(containerName)
		require.NoError(t, err)
		assert.Equal(t, map[string]string{"POSTGRES_USER": "admin"}, secrets)
	})

	t.Run("deleting nonexistent key is no-op", func(t *testing.T) {
		err := store.DeleteAttachment(containerName, "NONEXISTENT")
		require.NoError(t, err)
	})
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/adapters/out/domainsecrets/... -count=1 -run TestFileStore_DeleteAttachment -v`
Expected: FAIL — `store.DeleteAttachment` undefined

**Step 3: Add to interface and implement**

In `internal/boundaries/out/domain_secrets.go`, add to the `DomainSecretStore` interface:

```go
	// DeleteAttachment removes a specific secret key from an attachment container.
	DeleteAttachment(containerName, key string) error
```

In `internal/adapters/out/domainsecrets/store.go`, add after the `SetAttachment` method (~line 177):

```go
// DeleteAttachment removes a specific secret key from an attachment container.
func (s *FileStore) DeleteAttachment(containerName, key string) error {
	// Validate container name first
	if _, err := s.validateAttachmentEnvFilePath(containerName); err != nil {
		return err
	}

	// Read existing secrets
	existing, err := s.GetAllAttachment(containerName)
	if err != nil {
		return err
	}

	// Remove the key
	delete(existing, key)

	// Write back atomically
	return s.writeAttachmentSecretsAtomic(containerName, existing)
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./internal/adapters/out/domainsecrets/... -count=1 -run TestFileStore_DeleteAttachment -v`
Expected: PASS

**Step 5: Regenerate mocks**

Run: `mockery`
Expected: Regenerates `internal/boundaries/out/mocks/mock_domain_secret_store.go` with new `DeleteAttachment` method.

**Step 6: Run go fmt and full test suite**

Run: `go fmt ./... && go test ./... -count=1`
Expected: ALL PASS

**Step 7: Commit**

```bash
git add internal/boundaries/out/domain_secrets.go internal/adapters/out/domainsecrets/store.go internal/adapters/out/domainsecrets/store_test.go internal/boundaries/out/mocks/mock_domain_secret_store.go
git commit -m "feat(secrets): add DeleteAttachment to storage layer

Add ability to remove individual keys from attachment env files,
matching the existing SetAttachment capability."
```

---

### Task 2: Add attachment methods to SecretService interface and use case

The use case layer needs to resolve `(domain, service)` → container name and call through to the store.

**Files:**
- Modify: `internal/boundaries/in/secrets.go:11-31`
- Modify: `internal/usecase/secrets/service.go`
- Test: `internal/usecase/secrets/service_test.go` (create if needed)

**Step 1: Write the failing test**

Create or add to `internal/usecase/secrets/service_test.go`:

```go
package secrets

import (
	"context"
	"testing"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	outmocks "github.com/bnema/gordon/internal/boundaries/out/mocks"
)

func testLogger() zerowrap.Logger {
	return zerowrap.Default()
}

func TestService_SetAttachment(t *testing.T) {
	store := outmocks.NewMockDomainSecretStore(t)
	svc := NewService(store, testLogger())

	secrets := map[string]string{"POSTGRES_PASSWORD": "secret"}

	// domain "app.example.com" + service "postgres" → container name "app__example__com-postgres"
	store.EXPECT().SetAttachment("app__example__com-postgres", secrets).Return(nil)

	err := svc.SetAttachment(context.Background(), "app.example.com", "postgres", secrets)
	assert.NoError(t, err)
}

func TestService_DeleteAttachment(t *testing.T) {
	store := outmocks.NewMockDomainSecretStore(t)
	svc := NewService(store, testLogger())

	store.EXPECT().DeleteAttachment("app__example__com-postgres", "OLD_KEY").Return(nil)

	err := svc.DeleteAttachment(context.Background(), "app.example.com", "postgres", "OLD_KEY")
	assert.NoError(t, err)
}

func TestService_SetAttachment_InvalidDomain(t *testing.T) {
	store := outmocks.NewMockDomainSecretStore(t)
	svc := NewService(store, testLogger())

	err := svc.SetAttachment(context.Background(), "", "postgres", map[string]string{"K": "V"})
	assert.Error(t, err)
}

func TestService_SetAttachment_EmptyService(t *testing.T) {
	store := outmocks.NewMockDomainSecretStore(t)
	svc := NewService(store, testLogger())

	err := svc.SetAttachment(context.Background(), "app.example.com", "", map[string]string{"K": "V"})
	assert.ErrorIs(t, err, ErrServiceEmpty)
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/usecase/secrets/... -count=1 -run "TestService_SetAttachment|TestService_DeleteAttachment" -v`
Expected: FAIL — `svc.SetAttachment` undefined

**Step 3: Add to interface and implement**

In `internal/boundaries/in/secrets.go`, add to the `SecretService` interface:

```go
	// SetAttachment sets or updates secrets for an attachment container.
	// Resolves the container name from domain + service name.
	SetAttachment(ctx context.Context, domain, service string, secrets map[string]string) error

	// DeleteAttachment removes a secret key from an attachment container.
	// Resolves the container name from domain + service name.
	DeleteAttachment(ctx context.Context, domain, service, key string) error
```

In `internal/usecase/secrets/service.go`, add the error variable and methods:

```go
var ErrServiceEmpty = errors.New("service name cannot be empty")
```

Add the import for the `domain` package:

```go
import (
	// ... existing imports ...
	"github.com/bnema/gordon/internal/domain"
)
```

Add the methods:

```go
// SetAttachment sets or updates secrets for an attachment container.
func (s *Service) SetAttachment(ctx context.Context, domainName, service string, secrets map[string]string) error {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "SetAttachment",
		"domain":              domainName,
		"service":             service,
	})
	log := zerowrap.FromCtx(ctx)

	if err := ValidateDomain(domainName); err != nil {
		log.Warn().Err(err).Msg("domain validation failed")
		return err
	}
	if service == "" {
		return ErrServiceEmpty
	}

	containerName := resolveContainerName(domainName, service)
	if err := s.store.SetAttachment(containerName, secrets); err != nil {
		log.Error().Err(err).Str("container", containerName).Msg("failed to set attachment secrets")
		return err
	}

	log.Info().Str("container", containerName).Int("count", len(secrets)).Msg("attachment secrets set")
	return nil
}

// DeleteAttachment removes a secret key from an attachment container.
func (s *Service) DeleteAttachment(ctx context.Context, domainName, service, key string) error {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "DeleteAttachment",
		"domain":              domainName,
		"service":             service,
		"key":                 key,
	})
	log := zerowrap.FromCtx(ctx)

	if err := ValidateDomain(domainName); err != nil {
		log.Warn().Err(err).Msg("domain validation failed")
		return err
	}
	if service == "" {
		return ErrServiceEmpty
	}

	containerName := resolveContainerName(domainName, service)
	if err := s.store.DeleteAttachment(containerName, key); err != nil {
		log.Error().Err(err).Str("container", containerName).Msg("failed to delete attachment secret")
		return err
	}

	log.Info().Str("container", containerName).Msg("attachment secret deleted")
	return nil
}

// resolveContainerName builds the attachment container name from domain and service.
// Must match the naming convention in container service's deployAttachedService().
func resolveContainerName(domainName, service string) string {
	return domain.SanitizeDomainForContainer(domainName) + "-" + service
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./internal/usecase/secrets/... -count=1 -v`
Expected: ALL PASS

**Step 5: Regenerate mocks**

Run: `mockery`
Expected: Regenerates `internal/boundaries/in/mocks/mock_secret_service.go` with new methods.

**Step 6: Run go fmt and full test suite**

Run: `go fmt ./... && go test ./... -count=1`
Expected: ALL PASS

**Step 7: Commit**

```bash
git add internal/boundaries/in/secrets.go internal/usecase/secrets/service.go internal/usecase/secrets/service_test.go internal/boundaries/in/mocks/mock_secret_service.go
git commit -m "feat(secrets): add SetAttachment/DeleteAttachment to use case layer

Resolves (domain, service) to container name and delegates to
the storage layer. Matches container naming from deployAttachedService()."
```

---

### Task 3: Add admin API endpoint for attachment secrets

Add `/admin/secrets/{domain}/attachments/{service}` routes for POST (set) and DELETE (with key).

**Files:**
- Modify: `internal/adapters/in/http/admin/handler.go:446-518`
- Test: `internal/adapters/in/http/admin/handler_test.go`

**Step 1: Write the failing tests**

Add to `handler_test.go`:

```go
func TestHandler_AttachmentSecretsPost(t *testing.T) {
	configSvc := inmocks.NewMockConfigService(t)
	authSvc := inmocks.NewMockAuthService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	secretSvc := inmocks.NewMockSecretService(t)

	handler := NewHandler(configSvc, authSvc, containerSvc, inmocks.NewMockHealthService(t), secretSvc, nil, inmocks.NewMockRegistryService(t), nil, testLogger())

	secretSvc.EXPECT().SetAttachment(mock.Anything, "app.example.com", "postgres", map[string]string{"POSTGRES_PASSWORD": "secret"}).Return(nil)

	body := `{"POSTGRES_PASSWORD": "secret"}`
	req := httptest.NewRequest("POST", "/admin/secrets/app.example.com/attachments/postgres", bytes.NewBufferString(body))
	req = req.WithContext(ctxWithScopes("admin:secrets:write"))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestHandler_AttachmentSecretsDelete(t *testing.T) {
	configSvc := inmocks.NewMockConfigService(t)
	authSvc := inmocks.NewMockAuthService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	secretSvc := inmocks.NewMockSecretService(t)

	handler := NewHandler(configSvc, authSvc, containerSvc, inmocks.NewMockHealthService(t), secretSvc, nil, inmocks.NewMockRegistryService(t), nil, testLogger())

	secretSvc.EXPECT().DeleteAttachment(mock.Anything, "app.example.com", "postgres", "OLD_KEY").Return(nil)

	req := httptest.NewRequest("DELETE", "/admin/secrets/app.example.com/attachments/postgres/OLD_KEY", nil)
	req = req.WithContext(ctxWithScopes("admin:secrets:write"))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}
```

**Step 2: Run tests to verify they fail**

Run: `go test ./internal/adapters/in/http/admin/... -count=1 -run "TestHandler_AttachmentSecrets" -v`
Expected: FAIL — routes not recognized, 400 or 404

**Step 3: Implement the endpoint**

In `handler.go`, modify the `handleSecrets` function. Replace the path parsing section (lines 451-462) to detect the `/attachments/` sub-path:

```go
func (h *Handler) handleSecrets(w http.ResponseWriter, r *http.Request, path string) {
	ctx := r.Context()
	log := zerowrap.FromCtx(ctx)

	// Parse path: /secrets/{domain}, /secrets/{domain}/{key},
	// or /secrets/{domain}/attachments/{service}[/{key}]
	trimmed := strings.TrimPrefix(path, "/secrets/")
	parts := strings.Split(trimmed, "/")
	if len(parts) == 0 || parts[0] == "" {
		h.sendError(w, http.StatusBadRequest, "domain required")
		return
	}

	secretDomain := parts[0]

	// Check for attachment sub-path: /secrets/{domain}/attachments/{service}[/{key}]
	if len(parts) >= 3 && parts[1] == "attachments" {
		service := parts[2]
		attachmentKey := ""
		if len(parts) > 3 {
			attachmentKey = parts[3]
		}
		h.handleAttachmentSecrets(w, r, secretDomain, service, attachmentKey)
		return
	}

	secretKey := ""
	if len(parts) > 1 {
		secretKey = parts[1]
	}

	switch r.Method {
	case http.MethodGet:
		h.handleSecretsGet(w, r, secretDomain)

	case http.MethodPost:
		if !HasAccess(ctx, domain.AdminResourceSecrets, domain.AdminActionWrite) {
			h.sendError(w, http.StatusForbidden, "insufficient permissions for secrets:write")
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, maxAdminRequestSize)

		var data map[string]string
		if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
			log.Warn().Err(err).Msg("invalid secrets JSON")
			h.sendError(w, http.StatusBadRequest, "invalid JSON")
			return
		}

		if err := h.secretSvc.Set(ctx, secretDomain, data); err != nil {
			log.Error().Err(err).Str("domain", secretDomain).Msg("failed to set secrets")
			h.sendError(w, http.StatusBadRequest, "invalid domain")
			return
		}

		log.Info().Str("domain", secretDomain).Int("count", len(data)).Msg("secrets set")
		h.sendJSON(w, http.StatusOK, dto.SecretsStatusResponse{Status: "updated"})

	case http.MethodDelete:
		if !HasAccess(ctx, domain.AdminResourceSecrets, domain.AdminActionWrite) {
			h.sendError(w, http.StatusForbidden, "insufficient permissions for secrets:write")
			return
		}
		if secretKey == "" {
			h.sendError(w, http.StatusBadRequest, "key required in path")
			return
		}

		if err := h.secretSvc.Delete(ctx, secretDomain, secretKey); err != nil {
			log.Error().Err(err).Str("domain", secretDomain).Str("key", secretKey).Msg("failed to delete secret")
			h.sendError(w, http.StatusBadRequest, "invalid domain")
			return
		}

		log.Info().Str("domain", secretDomain).Str("key", secretKey).Msg("secret deleted")
		h.sendJSON(w, http.StatusOK, dto.SecretsStatusResponse{Status: "deleted"})

	default:
		h.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}
```

Add the new handler method:

```go
// handleAttachmentSecrets handles /admin/secrets/{domain}/attachments/{service}[/{key}].
func (h *Handler) handleAttachmentSecrets(w http.ResponseWriter, r *http.Request, secretDomain, service, key string) {
	ctx := r.Context()
	log := zerowrap.FromCtx(ctx)

	switch r.Method {
	case http.MethodPost:
		if !HasAccess(ctx, domain.AdminResourceSecrets, domain.AdminActionWrite) {
			h.sendError(w, http.StatusForbidden, "insufficient permissions for secrets:write")
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, maxAdminRequestSize)

		var data map[string]string
		if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
			log.Warn().Err(err).Msg("invalid attachment secrets JSON")
			h.sendError(w, http.StatusBadRequest, "invalid JSON")
			return
		}

		if err := h.secretSvc.SetAttachment(ctx, secretDomain, service, data); err != nil {
			log.Error().Err(err).Str("domain", secretDomain).Str("service", service).Msg("failed to set attachment secrets")
			h.sendError(w, http.StatusBadRequest, err.Error())
			return
		}

		log.Info().Str("domain", secretDomain).Str("service", service).Int("count", len(data)).Msg("attachment secrets set")
		h.sendJSON(w, http.StatusOK, dto.SecretsStatusResponse{Status: "updated"})

	case http.MethodDelete:
		if !HasAccess(ctx, domain.AdminResourceSecrets, domain.AdminActionWrite) {
			h.sendError(w, http.StatusForbidden, "insufficient permissions for secrets:write")
			return
		}
		if key == "" {
			h.sendError(w, http.StatusBadRequest, "key required in path")
			return
		}

		if err := h.secretSvc.DeleteAttachment(ctx, secretDomain, service, key); err != nil {
			log.Error().Err(err).Str("domain", secretDomain).Str("service", service).Str("key", key).Msg("failed to delete attachment secret")
			h.sendError(w, http.StatusBadRequest, err.Error())
			return
		}

		log.Info().Str("domain", secretDomain).Str("service", service).Str("key", key).Msg("attachment secret deleted")
		h.sendJSON(w, http.StatusOK, dto.SecretsStatusResponse{Status: "deleted"})

	default:
		h.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}
```

**Step 4: Run tests to verify they pass**

Run: `go test ./internal/adapters/in/http/admin/... -count=1 -v`
Expected: ALL PASS (both new and existing tests)

**Step 5: Run go fmt**

Run: `go fmt ./internal/adapters/in/http/admin/...`

**Step 6: Commit**

```bash
git add internal/adapters/in/http/admin/handler.go internal/adapters/in/http/admin/handler_test.go
git commit -m "feat(api): add attachment secrets endpoints

POST /admin/secrets/{domain}/attachments/{service} to set secrets.
DELETE /admin/secrets/{domain}/attachments/{service}/{key} to remove."
```

---

### Task 4: Add remote client methods

**Files:**
- Modify: `internal/adapters/in/cli/remote/client.go` (after `DeleteSecret` ~line 296)

**Step 1: Implement the client methods**

No separate test file exists for the remote client (it's tested via integration). Add the methods:

```go
// SetAttachmentSecrets sets secrets for an attachment container.
func (c *Client) SetAttachmentSecrets(ctx context.Context, domain, service string, secrets map[string]string) error {
	path := "/secrets/" + url.PathEscape(domain) + "/attachments/" + url.PathEscape(service)
	resp, err := c.request(ctx, http.MethodPost, path, secrets)
	if err != nil {
		return err
	}
	return parseResponse(resp, nil)
}

// DeleteAttachmentSecret removes a secret from an attachment container.
func (c *Client) DeleteAttachmentSecret(ctx context.Context, domain, service, key string) error {
	path := "/secrets/" + url.PathEscape(domain) + "/attachments/" + url.PathEscape(service) + "/" + url.PathEscape(key)
	resp, err := c.request(ctx, http.MethodDelete, path, nil)
	if err != nil {
		return err
	}
	return parseResponse(resp, nil)
}
```

**Step 2: Run go fmt and verify build**

Run: `go fmt ./internal/adapters/in/cli/remote/... && go build ./...`
Expected: Clean build

**Step 3: Commit**

```bash
git add internal/adapters/in/cli/remote/client.go
git commit -m "feat(client): add attachment secrets API methods"
```

---

### Task 5: Add `--attachment` flag to `secrets set` and `secrets remove` CLI commands

**Files:**
- Modify: `internal/adapters/in/cli/secrets.go:214-270` (set command)
- Modify: `internal/adapters/in/cli/secrets.go:272-327` (remove command)

**Step 1: Write the failing test for flag parsing**

Add to `internal/adapters/in/cli/secrets_test.go` (create file):

```go
package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestResolveContainerName(t *testing.T) {
	tests := []struct {
		domain  string
		service string
		want    string
	}{
		{"app.example.com", "postgres", "app__example__com-postgres"},
		{"git.example.com", "redis", "git__example__com-redis"},
	}

	for _, tt := range tests {
		t.Run(tt.domain+"/"+tt.service, func(t *testing.T) {
			got := resolveAttachmentContainerName(tt.domain, tt.service)
			assert.Equal(t, tt.want, got)
		})
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/adapters/in/cli/... -count=1 -run TestResolveContainerName -v`
Expected: FAIL — `resolveAttachmentContainerName` undefined

**Step 3: Implement**

In `secrets.go`, add the helper:

```go
// resolveAttachmentContainerName builds the container name from domain and service.
// Must match the naming in usecase/container deployAttachedService() and usecase/secrets resolveContainerName().
func resolveAttachmentContainerName(domainName, service string) string {
	return domain.SanitizeDomainForContainer(domainName) + "-" + service
}
```

Add the import for the domain package at the top of `secrets.go`:

```go
import (
	// ... existing imports ...
	"github.com/bnema/gordon/internal/domain"
)
```

Now modify `newSecretsSetCmd()` to add the `--attachment` flag. Replace the function:

```go
func newSecretsSetCmd() *cobra.Command {
	var attachment string

	cmd := &cobra.Command{
		Use:   "set <domain> <KEY=value>...",
		Short: "Set secrets for a domain or attachment",
		Long: `Set one or more secrets for a domain or an attachment container.

Secrets are specified as KEY=value pairs. Multiple secrets can be set at once.

Use --attachment to target an attachment service (e.g., postgres, redis) instead
of the main domain container.

Examples:
  gordon secrets set app.mydomain.com DATABASE_URL=postgres://localhost/db
  gordon secrets set app.mydomain.com API_KEY=secret123 DEBUG=false
  gordon secrets set app.mydomain.com --attachment postgres POSTGRES_PASSWORD=secret
  gordon secrets set app.mydomain.com --attachment redis REDIS_PASSWORD=secret`,
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			secretDomain := args[0]
			pairs := args[1:]

			// Parse KEY=value pairs
			secrets := make(map[string]string)
			for _, pair := range pairs {
				parts := strings.SplitN(pair, "=", 2)
				if len(parts) != 2 {
					fmt.Println(styles.RenderError(fmt.Sprintf("Invalid format: %s (expected KEY=value)", pair)))
					return nil
				}
				secrets[parts[0]] = parts[1]
			}

			client, isRemote := GetRemoteClient()
			if isRemote {
				if attachment != "" {
					if err := client.SetAttachmentSecrets(ctx, secretDomain, attachment, secrets); err != nil {
						return fmt.Errorf("failed to set attachment secrets: %w", err)
					}
				} else {
					if err := client.SetSecrets(ctx, secretDomain, secrets); err != nil {
						return fmt.Errorf("failed to set secrets: %w", err)
					}
				}
			} else {
				local, err := GetLocalServices(configPath)
				if err != nil {
					return fmt.Errorf("failed to initialize local services: %w", err)
				}
				if attachment != "" {
					if err := local.GetSecretService().SetAttachment(ctx, secretDomain, attachment, secrets); err != nil {
						return fmt.Errorf("failed to set attachment secrets: %w", err)
					}
				} else {
					if err := local.GetSecretService().Set(ctx, secretDomain, secrets); err != nil {
						return fmt.Errorf("failed to set secrets: %w", err)
					}
				}
			}

			target := secretDomain
			if attachment != "" {
				target = fmt.Sprintf("%s [%s]", secretDomain, attachment)
			}

			if len(secrets) == 1 {
				for key := range secrets {
					fmt.Println(styles.RenderSuccess(fmt.Sprintf("Secret set: %s on %s", key, target)))
				}
			} else {
				fmt.Println(styles.RenderSuccess(fmt.Sprintf("Set %d secrets for %s", len(secrets), target)))
			}

			return nil
		},
	}

	cmd.Flags().StringVarP(&attachment, "attachment", "a", "", "Target an attachment service (e.g., postgres, redis)")

	return cmd
}
```

Now modify `newSecretsRemoveCmd()` similarly:

```go
func newSecretsRemoveCmd() *cobra.Command {
	var (
		force      bool
		attachment string
	)

	cmd := &cobra.Command{
		Use:   "remove <domain> <key>",
		Short: "Remove a secret",
		Long: `Remove a secret from a domain or attachment container.

Use --attachment to target an attachment service instead of the main domain.

Examples:
  gordon secrets remove app.mydomain.com OLD_API_KEY
  gordon secrets remove app.mydomain.com OLD_API_KEY --force
  gordon secrets remove app.mydomain.com --attachment postgres POSTGRES_PASSWORD`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			secretDomain := args[0]
			key := args[1]

			target := secretDomain
			if attachment != "" {
				target = fmt.Sprintf("%s [%s]", secretDomain, attachment)
			}

			// Confirm unless --force
			if !force {
				confirmed, err := components.RunConfirm(
					fmt.Sprintf("Remove secret '%s' from %s?", key, target),
				)
				if err != nil {
					return err
				}
				if !confirmed {
					fmt.Println(styles.Theme.Muted.Render("Cancelled"))
					return nil
				}
			}

			client, isRemote := GetRemoteClient()
			if isRemote {
				if attachment != "" {
					if err := client.DeleteAttachmentSecret(ctx, secretDomain, attachment, key); err != nil {
						return fmt.Errorf("failed to remove attachment secret: %w", err)
					}
				} else {
					if err := client.DeleteSecret(ctx, secretDomain, key); err != nil {
						return fmt.Errorf("failed to remove secret: %w", err)
					}
				}
			} else {
				local, err := GetLocalServices(configPath)
				if err != nil {
					return fmt.Errorf("failed to initialize local services: %w", err)
				}
				if attachment != "" {
					if err := local.GetSecretService().DeleteAttachment(ctx, secretDomain, attachment, key); err != nil {
						return fmt.Errorf("failed to remove attachment secret: %w", err)
					}
				} else {
					if err := local.GetSecretService().Delete(ctx, secretDomain, key); err != nil {
						return fmt.Errorf("failed to remove secret: %w", err)
					}
				}
			}

			fmt.Println(styles.RenderSuccess(fmt.Sprintf("Secret removed: %s from %s", key, target)))
			return nil
		},
	}

	cmd.Flags().BoolVarP(&force, "force", "f", false, "Skip confirmation")
	cmd.Flags().StringVarP(&attachment, "attachment", "a", "", "Target an attachment service (e.g., postgres, redis)")

	return cmd
}
```

**Step 4: Run tests to verify they pass**

Run: `go test ./internal/adapters/in/cli/... -count=1 -v`
Expected: ALL PASS

**Step 5: Run go fmt and full suite**

Run: `go fmt ./... && go test ./... -count=1`
Expected: ALL PASS

**Step 6: Commit**

```bash
git add internal/adapters/in/cli/secrets.go internal/adapters/in/cli/secrets_test.go
git commit -m "feat(cli): add --attachment flag to secrets set/remove

Closes #56. Users can now configure attachment secrets via:
  gordon secrets set <domain> --attachment <service> KEY=value
  gordon secrets remove <domain> --attachment <service> KEY"
```

---

### Task 6: Update secrets command description and verify end-to-end

**Files:**
- Modify: `internal/adapters/in/cli/secrets.go:16-33` (parent command description)

**Step 1: Update parent command long description**

```go
func newSecretsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secrets",
		Short: "Manage secrets",
		Long: `Manage secrets (environment variables) for routes and attachments.

Secrets are stored per-domain and injected into containers as environment variables.
Use --attachment to target attachment containers (databases, caches, etc.).

When targeting a remote Gordon instance (via --remote flag or GORDON_REMOTE env var),
these commands operate on the remote server.`,
	}

	cmd.AddCommand(newSecretsListCmd())
	cmd.AddCommand(newSecretsSetCmd())
	cmd.AddCommand(newSecretsRemoveCmd())

	return cmd
}
```

**Step 2: Run full test suite**

Run: `go test ./... -count=1`
Expected: ALL PASS

**Step 3: Run go vet and build**

Run: `go vet ./... && go build ./...`
Expected: Clean

**Step 4: Commit**

```bash
git add internal/adapters/in/cli/secrets.go
git commit -m "docs(cli): update secrets command description for attachments"
```
