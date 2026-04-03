package accesslog

import (
	"fmt"
	"strings"

	"github.com/coreos/go-systemd/v22/journal"
)

// journalEnabledFunc and journalSendFunc are package-level variables so that
// tests can replace them with stubs without requiring a live systemd journal.
var (
	journalEnabledFunc = journal.Enabled
	journalSendFunc    = func(msg string, priority journal.Priority, vars map[string]string) error {
		return journal.Send(msg, priority, vars)
	}
)

// journaldSink writes access log lines directly to the systemd journal with a
// distinct SYSLOG_IDENTIFIER so security tools (CrowdSec, fail2ban) can filter
// on the identity without catching application logs.
type journaldSink struct {
	syslogID string
}

func newJournaldSink(cfg Config) (*journaldSink, error) {
	id := strings.TrimSpace(cfg.SyslogIdentifier)
	if id == "" {
		return nil, fmt.Errorf("accesslog: journald output requires a non-empty syslog_identifier")
	}
	if !journalEnabledFunc() {
		return nil, fmt.Errorf("accesslog: journald output requested but journald is not available on this system")
	}
	return &journaldSink{syslogID: id}, nil
}

func (s *journaldSink) WriteLine(line string) error {
	err := journalSendFunc(line, journal.PriInfo, map[string]string{
		"SYSLOG_IDENTIFIER": s.syslogID,
	})
	if err != nil {
		return fmt.Errorf("accesslog: journald write: %w", err)
	}
	return nil
}

func (s *journaldSink) Close() error { return nil }
