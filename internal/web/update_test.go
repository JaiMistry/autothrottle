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
	}
	for _, c := range cases {
		if got := compareVersions(c.a, c.b); got != c.want {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

// The behaviour the update card exists to get right: a build that is
// current, or ahead of the newest release, must never advertise an
// update.
func TestUpdateStateForRunningVersion(t *testing.T) {
	cases := []struct {
		running, latest, want string
	}{
		{"1.0.0", "1.0.0", "current"},
		{"1.0.1", "1.0.0", "current"}, // ahead of the release: nothing to fetch
		{"1.0.0", "1.0.1", "available"},
		{"1.0.9", "1.0.10", "available"},
	}
	for _, c := range cases {
		got := "available"
		if compareVersions(c.running, c.latest) >= 0 {
			got = "current"
		}
		if got != c.want {
			t.Errorf("running %s vs latest %s = %s, want %s", c.running, c.latest, got, c.want)
		}
	}
}
