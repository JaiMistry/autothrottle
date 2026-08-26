package version

import "testing"

func TestLabel(t *testing.T) {
	original := Version
	defer func() { Version = original }()

	cases := []struct{ stamp, want string }{
		{"1.0.0", "v1.0.0"},
		// release.yml stamps the git ref verbatim, which carries the "v".
		{"v1.0.0", "v1.0.0"},
		{"dev", "dev"},
		{"", "dev"},
		// An untagged local build is stamped with what git describe
		// managed; it must not be printed as a release.
		{"f3d60f3-dirty", "f3d60f3-dirty"},
		// A sha that happens to start with a digit is still a sha.
		{"5812252-dirty", "5812252-dirty"},
		{"5812252", "5812252"},
		{"v1.0.0-3-gabc1234", "v1.0.0-3-gabc1234"},
	}
	for _, c := range cases {
		Version = c.stamp
		if got := Label(); got != c.want {
			t.Errorf("Label() with Version=%q = %q, want %q", c.stamp, got, c.want)
		}
	}
}

func TestNumber(t *testing.T) {
	original := Version
	defer func() { Version = original }()

	for stamp, want := range map[string]string{
		"v1.2.3":        "1.2.3",
		"1.2.3":         "1.2.3",
		"dev":           "",
		"f3d60f3-dirty": "",
		"5812252-dirty": "",
		"5812252":       "",
	} {
		Version = stamp
		if got := Number(); got != want {
			t.Errorf("Number() with Version=%q = %q, want %q", stamp, got, want)
		}
	}
}
