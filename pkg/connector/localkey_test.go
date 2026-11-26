package connector

import (
	"errors"
	"testing"
)

func TestAdoptLocalHardwareKey(t *testing.T) {
	readErr := errors.New("no IOKit")
	tests := []struct {
		name        string
		stored      string
		onMacOS     bool
		readKey     func() (string, error)
		wantAdopted bool
		wantKey     string
		wantErr     error
	}{
		{"pre-2.0 Mac login gets this Mac's key", "", true, func() (string, error) { return "local", nil }, true, "local", nil},
		{"stored key is left alone", "stored", true, func() (string, error) { return "local", nil }, false, "stored", nil},
		{"non-Mac never reads a key", "", false, func() (string, error) { return "local", nil }, false, "", nil},
		{"read failure surfaces and stores nothing", "", true, func() (string, error) { return "", readErr }, false, "", readErr},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			meta := &UserLoginMetadata{HardwareKey: tt.stored}
			adopted, err := adoptLocalHardwareKey(meta, tt.onMacOS, tt.readKey)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if adopted != tt.wantAdopted {
				t.Errorf("adopted = %v, want %v", adopted, tt.wantAdopted)
			}
			if meta.HardwareKey != tt.wantKey {
				t.Errorf("HardwareKey = %q, want %q", meta.HardwareKey, tt.wantKey)
			}
		})
	}
}
