package common

import "testing"

func TestParseID(t *testing.T) {
	tests := []struct {
		in      string
		ns      string
		name    string
		wantErr bool
	}{
		{"default/dev-1", "default", "dev-1", false},
		{"dev-1", "default", "dev-1", false},
		{"team-a/box", "team-a", "box", false},
		{"a/b/c", "", "", true},
		{"/name", "", "", true},
		{"ns/", "", "", true},
	}
	for _, tt := range tests {
		ns, name, err := ParseID(tt.in)
		if (err != nil) != tt.wantErr {
			t.Errorf("ParseID(%q) err = %v, wantErr %v", tt.in, err, tt.wantErr)
			continue
		}
		if err == nil && (ns != tt.ns || name != tt.name) {
			t.Errorf("ParseID(%q) = (%q, %q), want (%q, %q)", tt.in, ns, name, tt.ns, tt.name)
		}
	}
}
