package accesslog

import (
	"errors"
	"testing"

	"github.com/coreos/go-systemd/v22/journal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// capturedJournalCall records a single call to the stubbed journalSendFunc.
type capturedJournalCall struct {
	msg      string
	priority journal.Priority
	vars     map[string]string
}

// withJournalStub replaces journalEnabledFunc and journalSendFunc for the
// duration of a test, restoring them afterwards.
func withJournalStub(t *testing.T, enabled bool, captures *[]capturedJournalCall) {
	t.Helper()
	origEnabled := journalEnabledFunc
	origSend := journalSendFunc

	journalEnabledFunc = func() bool { return enabled }
	journalSendFunc = func(msg string, priority journal.Priority, vars map[string]string) error {
		*captures = append(*captures, capturedJournalCall{msg: msg, priority: priority, vars: vars})
		return nil
	}

	t.Cleanup(func() {
		journalEnabledFunc = origEnabled
		journalSendFunc = origSend
	})
}

func TestNewJournaldSink_JournaldUnavailable(t *testing.T) {
	var calls []capturedJournalCall
	withJournalStub(t, false, &calls)

	_, err := New(Config{
		Format:           "json",
		Output:           "journald",
		SyslogIdentifier: "gordon-access",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "journald is not available")
}

func TestNewJournaldSink_EmptySyslogIdentifier(t *testing.T) {
	var calls []capturedJournalCall
	withJournalStub(t, true, &calls)

	_, err := New(Config{
		Format:           "json",
		Output:           "journald",
		SyslogIdentifier: "",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "syslog_identifier")
}

func TestNewJournaldSink_WhitespaceSyslogIdentifier(t *testing.T) {
	var calls []capturedJournalCall
	withJournalStub(t, true, &calls)

	_, err := New(Config{
		Format:           "json",
		Output:           "journald",
		SyslogIdentifier: "   ",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "syslog_identifier")
}

func TestJournaldSink_Write_CallsJournalSend(t *testing.T) {
	var calls []capturedJournalCall
	withJournalStub(t, true, &calls)

	writer, err := New(Config{
		Format:           "json",
		Output:           "journald",
		SyslogIdentifier: "gordon-access",
	})
	require.NoError(t, err)

	require.NoError(t, writer.Write(sampleEntry))
	require.NoError(t, writer.Close())

	require.Len(t, calls, 1)
	assert.Equal(t, journal.PriInfo, calls[0].priority)
	assert.Equal(t, "gordon-access", calls[0].vars["SYSLOG_IDENTIFIER"])
	assert.NotEmpty(t, calls[0].msg, "message must be the serialized log line")
}

func TestJournaldSink_Write_SendsCustomIdentifier(t *testing.T) {
	var calls []capturedJournalCall
	withJournalStub(t, true, &calls)

	writer, err := New(Config{
		Format:           "clf",
		Output:           "journald",
		SyslogIdentifier: "my-app-access",
	})
	require.NoError(t, err)

	require.NoError(t, writer.Write(sampleEntry))

	require.Len(t, calls, 1)
	assert.Equal(t, "my-app-access", calls[0].vars["SYSLOG_IDENTIFIER"])
}

func TestJournaldSink_Write_ReturnsErrorOnSendFailure(t *testing.T) {
	origEnabled := journalEnabledFunc
	origSend := journalSendFunc
	t.Cleanup(func() {
		journalEnabledFunc = origEnabled
		journalSendFunc = origSend
	})

	journalEnabledFunc = func() bool { return true }
	journalSendFunc = func(_ string, _ journal.Priority, _ map[string]string) error {
		return errors.New("journal socket closed")
	}

	writer, err := New(Config{
		Format:           "json",
		Output:           "journald",
		SyslogIdentifier: "gordon-access",
	})
	require.NoError(t, err)

	err = writer.Write(sampleEntry)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "journal socket closed")
}
