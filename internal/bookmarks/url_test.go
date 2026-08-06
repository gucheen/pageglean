package bookmarks

import "testing"

func TestNormalizeURL(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"HTTPS://Example.COM", "https://example.com/"},
		{"https://example.com:443/a#section", "https://example.com/a"},
		{"http://EXAMPLE.com:8080/path?q=1", "http://example.com:8080/path?q=1"},
	}
	for _, test := range tests {
		_, got, err := NormalizeURL(test.input)
		if err != nil {
			t.Fatalf("NormalizeURL(%q): %v", test.input, err)
		}
		if got != test.want {
			t.Fatalf("NormalizeURL(%q) = %q, want %q", test.input, got, test.want)
		}
	}
}

func TestNormalizeURLRejectsUnsafeSchemes(t *testing.T) {
	if _, _, err := NormalizeURL("javascript:alert(1)"); err == nil {
		t.Fatal("expected unsafe scheme to be rejected")
	}
}
