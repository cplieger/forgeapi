package gitlab

import "testing"

// This product reports versions such as 19.5.0-ee and 19.5.0-rc42; the first two
// numbers decide, and a version whose first two do not parse is read as before.
func TestVersionAtLeast(t *testing.T) {
	for _, tc := range []struct {
		version string
		want    bool
	}{
		{"19.5.0", true},
		{"19.5.0-ee", true},
		{"19.5.0-rc42", true},
		{"19.5", true},
		{"19.12.3", true},
		{"20.0.0", true},
		{"19.4.9-ee", false},
		{"18.11.0", false},
		{"19", false},
		{"19.x.0", false},
		{"v19.5.0", false},
		{"", false},
	} {
		if got := versionAtLeast(tc.version, 19, 5); got != tc.want {
			t.Errorf("versionAtLeast(%q, 19, 5) = %v, want %v", tc.version, got, tc.want)
		}
	}
}
