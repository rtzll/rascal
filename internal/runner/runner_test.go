package runner

import "testing"

func TestNormalizeMode(t *testing.T) {
	t.Parallel()

	tests := map[string]Mode{
		"":        ModeNoop,
		"noop":    ModeNoop,
		"docker":  ModeDocker,
		"DOCKER":  ModeDocker,
		"unknown": ModeNoop,
	}
	for in, want := range tests {
		if got := NormalizeMode(in); got != want {
			t.Fatalf("NormalizeMode(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseMode(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		want    Mode
		wantErr bool
	}{
		"":        {want: ModeNoop},
		"noop":    {want: ModeNoop},
		"docker":  {want: ModeDocker},
		"DOCKER":  {want: ModeDocker},
		"unknown": {wantErr: true},
	}
	for in, tt := range tests {
		got, err := ParseMode(in)
		if tt.wantErr {
			if err == nil {
				t.Fatalf("ParseMode(%q) error = nil, want error", in)
			}
			continue
		}
		if err != nil {
			t.Fatalf("ParseMode(%q) returned error: %v", in, err)
		}
		if got != tt.want {
			t.Fatalf("ParseMode(%q) = %q, want %q", in, got, tt.want)
		}
	}
}
