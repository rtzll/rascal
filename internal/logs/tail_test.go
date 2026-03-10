package logs

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestTail(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		content  string
		maxLines int
		want     []string
	}{
		{
			name:     "empty file",
			content:  "",
			maxLines: 5,
			want:     []string{},
		},
		{
			name:     "fewer lines than requested",
			content:  "one\ntwo\n",
			maxLines: 5,
			want:     []string{"one", "two"},
		},
		{
			name:     "more lines than requested",
			content:  "one\ntwo\nthree\nfour\n",
			maxLines: 2,
			want:     []string{"three", "four"},
		},
		{
			name:     "non positive maxLines uses default",
			content:  "one\ntwo\n",
			maxLines: 0,
			want:     []string{"one", "two"},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(t.TempDir(), "rascal.log")
			if err := os.WriteFile(path, []byte(tt.content), 0o600); err != nil {
				t.Fatalf("write log file: %v", err)
			}

			got, err := Tail(path, tt.maxLines)
			if err != nil {
				t.Fatalf("Tail(%q, %d) error = %v", path, tt.maxLines, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Tail(%q, %d) = %#v, want %#v", path, tt.maxLines, got, tt.want)
			}
		})
	}
}

func TestTailInvalidPath(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "missing.log")
	_, err := Tail(path, 10)
	if err == nil {
		t.Fatal("expected error for missing path")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected os.ErrNotExist, got %v", err)
	}
	if !strings.Contains(err.Error(), "open log file") {
		t.Fatalf("expected wrapped open log file error, got %v", err)
	}
}
