package web

import "testing"

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.0.0", "1.0.0", 0},
		{"1.0.0", "1.0.1", -1},
		{"1.0.1", "1.0.0", 1},
		// The case a string comparison gets wrong.
		{"1.0.10", "1.0.9", 1},
		{"1.2.0", "1.10.0", -1},
		// Missing segments are zero, so these name the same release.
		{"1.0", "1.0.0", 0},
		{"2", "2.0.0", 0},
		// A pre-release sorts below the release it leads to.
		{"1.1.0-rc1", "1.1.0", -1},
		{"1.1.0", "1.1.0-rc1", 1},
		{"1.1.0-rc1", "1.1.0-rc2", -1},
		// Unparseable segments compare equal rather than guessing.
		{"nightly", "1.0.0", 0},
		// The release-candidate scheme this project tags with: an rc is
		// told about its release, never "upgraded" to an older rc.
		{"1.0.0-rc.1", "1.0.0-rc.2", -1},
		{"1.0.0-rc.2", "1.0.0-rc.1", 1},
		{"1.0.0-rc.1", "1.0.0-rc.1", 0},
		{"1.0.0-rc.1", "1.0.0", -1},
		{"1.0.0", "1.0.0-rc.9", 1},
		{"1.0.0-rc.1", "1.0.1", -1},
	}
	for _, c := range cases {
		if got := compareVersions(c.a, c.b); got != c.want {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}
