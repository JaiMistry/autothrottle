package web

import "testing"

func TestSameEndpoint(t *testing.T) {
	same := [][2]string{
		{"http://192.168.1.60:8080", "http://192.168.1.60:8080/"},
		{"http://192.168.1.60:8080", "HTTP://192.168.1.60:8080"},
		{"http://Plex.local:32400/", "http://plex.local:32400"},
		// Default ports are implied, not different addresses.
		{"http://media.lan:80", "http://media.lan"},
		{"https://media.lan:443/", "https://media.lan"},
	}
	for _, c := range same {
		if !sameEndpoint(c[0], c[1]) {
			t.Errorf("sameEndpoint(%q, %q) = false, want true", c[0], c[1])
		}
	}

	different := [][2]string{
		{"http://192.168.1.60:8080", "http://192.168.1.60:9090"},
		{"http://192.168.1.60:8080", "http://192.168.1.61:8080"},
		// Scheme matters: these reach the service differently.
		{"http://media.lan:32400", "https://media.lan:32400"},
	}
	for _, c := range different {
		if sameEndpoint(c[0], c[1]) {
			t.Errorf("sameEndpoint(%q, %q) = true, want false", c[0], c[1])
		}
	}
}

func TestSameName(t *testing.T) {
	if !sameName("  Living Room Plex ", "living room plex") {
		t.Error("names differing only by case and padding should clash")
	}
	if sameName("Plex", "Plex 2") {
		t.Error("distinct names should not clash")
	}
}
