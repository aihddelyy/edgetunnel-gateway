package updater

import (
	"testing"
)

func TestCleanMirrorURL(t *testing.T) {
	cases := map[string]string{
		"https://ghfast.top":   "https://ghfast.top/",
		"ghfast.top/":          "https://ghfast.top/",
		"https://ghproxy.net/": "https://ghproxy.net/",
		"  ":                   "",
	}
	for in, expected := range cases {
		out := CleanMirrorURL(in)
		if out != expected {
			t.Errorf("CleanMirrorURL(%q) = %q; 期望 %q", in, out, expected)
		}
	}
}
