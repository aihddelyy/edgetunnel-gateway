package updater

import (
	"net"
	"testing"
)

func TestIsFakeIP(t *testing.T) {
	fakeIP1 := net.ParseIP("198.18.0.1")
	fakeIP2 := net.ParseIP("198.19.255.254")
	realIP1 := net.ParseIP("20.205.243.166")
	realIP2 := net.ParseIP("127.0.0.1")

	if !isFakeIP(fakeIP1) {
		t.Errorf("198.18.0.1 应该被判定为 Fake-IP")
	}
	if !isFakeIP(fakeIP2) {
		t.Errorf("198.19.255.254 应该被判定为 Fake-IP")
	}
	if isFakeIP(realIP1) {
		t.Errorf("20.205.243.166 不应该是 Fake-IP")
	}
	if isFakeIP(realIP2) {
		t.Errorf("127.0.0.1 不应该是 Fake-IP")
	}
}

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
