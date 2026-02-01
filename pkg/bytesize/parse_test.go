package bytesize

import (
	"testing"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    int64
		wantErr bool
	}{
		{
			name:  "bytes",
			input: "512B",
			want:  512,
		},
		{
			name:  "kilobytes",
			input: "100KB",
			want:  100 * 1024,
		},
		{
			name:  "megabytes",
			input: "512MB",
			want:  512 * 1024 * 1024,
		},
		{
			name:  "gigabytes",
			input: "1GB",
			want:  1024 * 1024 * 1024,
		},
		{
			name:  "terabytes",
			input: "1TB",
			want:  int64(1024) * 1024 * 1024 * 1024,
		},
		{
			name:  "decimal megabytes",
			input: "1.5GB",
			want:  int64(1.5 * 1024 * 1024 * 1024),
		},
		{
			name:  "lowercase",
			input: "512mb",
			want:  512 * 1024 * 1024,
		},
		{
			name:  "mixed case",
			input: "512Mb",
			want:  512 * 1024 * 1024,
		},
		{
			name:  "with spaces",
			input: " 512 MB ",
			want:  512 * 1024 * 1024,
		},
		{
			name:    "empty string",
			input:   "",
			wantErr: true,
		},
		{
			name:    "missing unit",
			input:   "512",
			wantErr: true,
		},
		{
			name:    "missing value",
			input:   "MB",
			wantErr: true,
		},
		{
			name:    "invalid value",
			input:   "abcMB",
			wantErr: true,
		},
		{
			name:    "negative value",
			input:   "-1GB",
			wantErr: true,
		},
		{
			name:    "unknown unit",
			input:   "512XB",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("Parse() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("Parse() = %v, want %v", got, tt.want)
			}
		})
	}
}
