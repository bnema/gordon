package duration

import (
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    time.Duration
		wantErr bool
	}{
		// Single human-friendly units
		{name: "1 day", input: "1d", want: Day},
		{name: "2 days", input: "2d", want: 2 * Day},
		{name: "1 week", input: "1w", want: Week},
		{name: "2 weeks", input: "2w", want: 2 * Week},
		{name: "1 month", input: "1M", want: Month},
		{name: "3 months", input: "3M", want: 3 * Month},
		{name: "1 year", input: "1y", want: Year},
		{name: "2 years", input: "2y", want: 2 * Year},

		// Compound human-friendly units
		{name: "1 year 6 months", input: "1y6M", want: Year + 6*Month},
		{name: "2 weeks 3 days", input: "2w3d", want: 2*Week + 3*Day},
		{name: "1 year 2 months 1 week", input: "1y2M1w", want: Year + 2*Month + Week},

		// Mixed with standard Go units
		{name: "1 day 12 hours", input: "1d12h", want: Day + 12*time.Hour},
		{name: "2 weeks 6 hours", input: "2w6h", want: 2*Week + 6*time.Hour},
		{name: "1 month 1 day 1 hour", input: "1M1d1h", want: Month + Day + time.Hour},
		{name: "1 year 30 minutes", input: "1y30m", want: Year + 30*time.Minute},

		// Standard Go duration units (fallback)
		{name: "24 hours", input: "24h", want: 24 * time.Hour},
		{name: "30 minutes", input: "30m", want: 30 * time.Minute},
		{name: "1 hour 30 minutes", input: "1h30m", want: time.Hour + 30*time.Minute},
		{name: "1 second", input: "1s", want: time.Second},
		{name: "500 milliseconds", input: "500ms", want: 500 * time.Millisecond},
		{name: "1000 microseconds", input: "1000us", want: 1000 * time.Microsecond},
		{name: "1000000 nanoseconds", input: "1000000ns", want: 1000000 * time.Nanosecond},

		// Special cases
		{name: "zero duration", input: "0", want: 0},
		{name: "zero with unit", input: "0d", want: 0},
		{name: "zero hours", input: "0h", want: 0},

		// Large values
		{name: "10 years", input: "10y", want: 10 * Year},
		{name: "52 weeks", input: "52w", want: 52 * Week},
		{name: "365 days", input: "365d", want: 365 * Day},

		// Edge cases with whitespace (should be trimmed)
		{name: "whitespace around", input: "  1d  ", want: Day},

		// Error cases
		{name: "empty string", input: "", wantErr: true},
		{name: "invalid format", input: "abc", wantErr: true},
		{name: "invalid unit", input: "1x", wantErr: true},
		{name: "missing value", input: "d", wantErr: true},
		{name: "negative not supported by Go", input: "-1d", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("Parse(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("Parse(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseConstants(t *testing.T) {
	// Verify our constants are correct
	if Day != 24*time.Hour {
		t.Errorf("Day = %v, want %v", Day, 24*time.Hour)
	}
	if Week != 7*24*time.Hour {
		t.Errorf("Week = %v, want %v", Week, 7*24*time.Hour)
	}
	if Month != 30*24*time.Hour {
		t.Errorf("Month = %v, want %v", Month, 30*24*time.Hour)
	}
	if Year != 365*24*time.Hour {
		t.Errorf("Year = %v, want %v", Year, 365*24*time.Hour)
	}
}

func TestParseEquivalences(t *testing.T) {
	// Test that our human-friendly units produce expected equivalences
	tests := []struct {
		name  string
		human string
		goStd string
		hours int
	}{
		{name: "1 day = 24 hours", human: "1d", goStd: "24h", hours: 24},
		{name: "1 week = 168 hours", human: "1w", goStd: "168h", hours: 168},
		{name: "1 month = 720 hours", human: "1M", goStd: "720h", hours: 720},
		{name: "1 year = 8760 hours", human: "1y", goStd: "8760h", hours: 8760},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			humanDur, err := Parse(tt.human)
			if err != nil {
				t.Fatalf("Parse(%q) error = %v", tt.human, err)
			}

			goDur, err := time.ParseDuration(tt.goStd)
			if err != nil {
				t.Fatalf("time.ParseDuration(%q) error = %v", tt.goStd, err)
			}

			if humanDur != goDur {
				t.Errorf("Parse(%q) = %v, want %v (from %q)", tt.human, humanDur, goDur, tt.goStd)
			}

			expectedHours := time.Duration(tt.hours) * time.Hour
			if humanDur != expectedHours {
				t.Errorf("Parse(%q) = %v, want %d hours", tt.human, humanDur, tt.hours)
			}
		})
	}
}
