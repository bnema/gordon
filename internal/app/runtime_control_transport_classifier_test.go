package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestPrivateTransportErrorClassifierUnifiesCategories(t *testing.T) {
	const maliciousText = "path=/private/runtime.sock uid=12345 token=do-not-leak"

	terminalCategories := []privateRuntimeTransportErrorCategory{
		privateRuntimeTransportInvalidShape,
		privateRuntimeTransportSymlinkAncestor,
		privateRuntimeTransportInvalidNode,
		privateRuntimeTransportConnectPermission,
		privateRuntimeTransportUnvalidatedFailure,
	}
	for _, category := range terminalCategories {
		t.Run("terminal_"+string(category), func(t *testing.T) {
			err := privateRuntimeTransportValidationError(category)
			require.False(t, isTransientPrivateRuntimeTransportError(err))
			gotCategory, ok := privateRuntimeTransportValidationCategory(err)
			require.True(t, ok)
			assert.Equal(t, category, gotCategory)
			assert.Equal(t, codes.PermissionDenied, status.Code(err))
			assert.Contains(t, err.Error(), "category="+string(category))
			assert.NotContains(t, err.Error(), maliciousText)

			wrapped := fmt.Errorf("%s: %w", maliciousText, err)
			gotCategory, ok = privateRuntimeTransportValidationCategory(wrapped)
			require.True(t, ok)
			assert.Equal(t, category, gotCategory)
			assert.Equal(t, codes.PermissionDenied, status.Code(wrapped))
			var transportErr *privateRuntimeTransportError
			require.True(t, errors.As(wrapped, &transportErr))
			assert.NotContains(t, transportErr.Error(), maliciousText)
		})
	}

	t.Run("transient_connect_unavailable", func(t *testing.T) {
		err := errPrivateRuntimeTransportUnavailable
		require.True(t, isTransientPrivateRuntimeTransportError(err))
		gotCategory, ok := privateRuntimeTransportValidationCategory(err)
		require.True(t, ok)
		assert.Equal(t, privateRuntimeTransportConnectUnavailable, gotCategory)
		assert.Equal(t, codes.Unknown, status.Code(err))
		assert.Contains(t, err.Error(), "category=connect_unavailable")
	})

	t.Run("transient_inspection_failure", func(t *testing.T) {
		err := privateRuntimeTransportValidationError(privateRuntimeTransportInspectionFailure)
		require.True(t, isTransientPrivateRuntimeTransportError(err))
		gotCategory, ok := privateRuntimeTransportValidationCategory(err)
		require.True(t, ok)
		assert.Equal(t, privateRuntimeTransportInspectionFailure, gotCategory)
		assert.Equal(t, codes.PermissionDenied, status.Code(err))
	})

	t.Run("bounded_unavailable_deadline", func(t *testing.T) {
		err := privateRuntimeTransportBoundedDeadlineError(privateRuntimeTransportConnectUnavailable)
		require.ErrorIs(t, err, context.DeadlineExceeded)
		require.True(t, errors.Is(err, errPrivateRuntimeTransportUnavailable))
		gotCategory, ok := privateRuntimeTransportValidationCategory(err)
		require.True(t, ok)
		assert.Equal(t, privateRuntimeTransportConnectUnavailable, gotCategory)
		assert.Contains(t, err.Error(), "category=connect_unavailable")
		assert.NotContains(t, err.Error(), maliciousText)

		wrapped := fmt.Errorf("%s: %w", maliciousText, err)
		require.ErrorIs(t, wrapped, context.DeadlineExceeded)
		require.True(t, errors.Is(wrapped, errPrivateRuntimeTransportUnavailable))
		gotCategory, ok = privateRuntimeTransportValidationCategory(wrapped)
		require.True(t, ok)
		assert.Equal(t, privateRuntimeTransportConnectUnavailable, gotCategory)
		var transportErr *privateRuntimeTransportError
		require.True(t, errors.As(wrapped, &transportErr))
		assert.NotContains(t, transportErr.Error(), maliciousText)
	})

	t.Run("bounded_inspection_deadline", func(t *testing.T) {
		err := privateRuntimeTransportBoundedDeadlineError(privateRuntimeTransportInspectionFailure)
		require.ErrorIs(t, err, context.DeadlineExceeded)
		gotCategory, ok := privateRuntimeTransportValidationCategory(err)
		require.True(t, ok)
		assert.Equal(t, privateRuntimeTransportInspectionFailure, gotCategory)
		assert.Equal(t, codes.PermissionDenied, status.Code(err))
		assert.Contains(t, err.Error(), "category=inspection_failure")
		assert.NotContains(t, err.Error(), maliciousText)
	})

	t.Run("deadline_classifier_from_dial_error", func(t *testing.T) {
		inspectionErr := privateRuntimeTransportValidationError(privateRuntimeTransportInspectionFailure)
		unavailableErr := errPrivateRuntimeTransportUnavailable

		inspectionBounded := boundedPrivateRuntimeTransportDeadlineError(inspectionErr)
		require.ErrorIs(t, inspectionBounded, context.DeadlineExceeded)
		assert.Equal(t, codes.PermissionDenied, status.Code(inspectionBounded))

		unavailableBounded := boundedPrivateRuntimeTransportDeadlineError(unavailableErr)
		require.ErrorIs(t, unavailableBounded, context.DeadlineExceeded)
		require.True(t, errors.Is(unavailableBounded, errPrivateRuntimeTransportUnavailable))
	})

	t.Run("retry_context_normalization", func(t *testing.T) {
		err := waitForPrivateRuntimeTransport(t.Context(), func(context.Context) (net.Conn, error) {
			return nil, fmt.Errorf("dial %s: %w", maliciousText, errPrivateRuntimeTransportUnavailable)
		}, func(context.Context) error {
			return context.DeadlineExceeded
		})
		require.ErrorIs(t, err, context.DeadlineExceeded)
		require.True(t, errors.Is(err, errPrivateRuntimeTransportUnavailable))
		assert.NotContains(t, err.Error(), maliciousText)
	})
}
