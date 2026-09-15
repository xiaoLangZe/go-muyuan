package security

import (
	"net"
	"testing"
)

func parseIPHelper(s string) net.IP { return net.ParseIP(s) }

func TestValidateURLRejectsPrivateAndBadScheme(t *testing.T) {
	cases := []struct {
		url     string
		wantErr bool
	}{
		{"http://127.0.0.1/x", true},
		{"http://localhost/x", true},
		{"http://[::1]/x", true},
		{"http://10.0.0.5/x", true},
		{"http://192.168.1.1/x", true},
		{"http://172.16.9.9/x", true},
		{"http://169.254.1.1/x", true},
		{"file:///etc/passwd", true},
		{"ftp://example.com/x", true},
		{"http://example.com/x", false},
	}
	for _, tc := range cases {
		err := ValidateURL(tc.url, false)
		if tc.wantErr && err == nil {
			t.Errorf("validateURL(%q) = nil, want error", tc.url)
		}
		if !tc.wantErr && err != nil {
			t.Errorf("validateURL(%q) = %v, want nil", tc.url, err)
		}
	}
	// allowPrivate 时应允许 loopback（用于 LAN 下载）。
	if err := ValidateURL("http://127.0.0.1:8080/x", true); err != nil {
		t.Errorf("allowPrivate still rejected loopback: %v", err)
	}
}

func TestValidateURLQuick(t *testing.T) {
	// 快检不做 DNS：主机名一律放行（权威检查在 ValidateURL）。
	if err := ValidateURLQuick("http://example.com/x", false); err != nil {
		t.Errorf("quick(public hostname) = %v, want nil", err)
	}
	// 但 IP 字面量形式的私有地址会被拒绝。
	if err := ValidateURLQuick("http://10.0.0.5/x", false); err == nil {
		t.Error("quick(private IP literal) = nil, want error")
	}
	// allowPrivate 放行一切主机检查。
	if err := ValidateURLQuick("http://10.0.0.5/x", true); err != nil {
		t.Errorf("quick(private, allowPrivate) = %v, want nil", err)
	}
	// scheme 仍被强制。
	if err := ValidateURLQuick("ftp://example.com/x", true); err == nil {
		t.Error("quick(ftp, allowPrivate) = nil, want error")
	}
}

func TestIsPublicIP(t *testing.T) {
	public := []string{"1.1.1.1", "8.8.8.8", "93.184.216.34", "2606:4700:4700::1111"}
	private := []string{"127.0.0.1", "10.1.2.3", "192.168.0.1", "172.20.0.1",
		"169.254.0.1", "0.0.0.0", "100.64.0.1", "fc00::1", "::1"}
	for _, s := range public {
		if !IsPublicIP(parseIPHelper(s)) {
			t.Errorf("%s should be public", s)
		}
	}
	for _, s := range private {
		if IsPublicIP(parseIPHelper(s)) {
			t.Errorf("%s should be non-public", s)
		}
	}
}
