package semver

import "testing"

func TestBumpPatch(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"1.0.0", "1.0.1"},
		{"1.2.3", "1.2.4"},
		{"0.0.0", "0.0.1"},
		{"2.10.99", "2.10.100"},
		{"1.0.0-beta", "1.0.1"},
		{"1.0.0+build.123", "1.0.1"},
		{"1.0.0-rc.1+sha.abc", "1.0.1"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := BumpPatch(tt.input)
			if got != tt.want {
				t.Errorf("BumpPatch(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestCompare(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"1.0.0", "1.0.0", 0},
		{"1.0.0", "1.0.1", -1},
		{"1.0.1", "1.0.0", 1},
		{"2.0.0", "1.9.9", 1},
		{"0.1.0", "0.0.9", 1},
	}

	for _, tt := range tests {
		t.Run(tt.a+"_vs_"+tt.b, func(t *testing.T) {
			got := Compare(tt.a, tt.b)
			if got != tt.want {
				t.Errorf("Compare(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
			}
		})
	}
}
