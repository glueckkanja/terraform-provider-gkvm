package monitoring

import "testing"

func TestValidatePath(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{"empty", "", false},
		{"valid simple", "defaults", false},
		{"valid nested", "configs/monitoring", false},
		{"traversal", "../secrets", true},
		{"absolute", "/etc/passwd", true},
		{"traversal nested", "a/../b", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidatePath(tt.path)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidatePath(%q) error = %v, wantErr %v", tt.path, err, tt.wantErr)
			}
		})
	}
}
