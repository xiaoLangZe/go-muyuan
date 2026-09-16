package security

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
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

// TestIsPublicIPReservedRanges 对保留段做穷举边界测试，覆盖
// IsPublicIP 里除 net.IsPrivate 之外手工维护的 CIDR 清单。
func TestIsPublicIPReservedRanges(t *testing.T) {
	nonPublic := []string{
		// IPv4 保留段
		"0.1.2.3", "100.64.0.1", "100.127.255.254", "192.0.0.1",
		"192.0.2.123",  // TEST-NET-1
		"198.18.0.1",   // benchmark
		"198.51.100.7", // TEST-NET-2
		"203.0.113.9",  // TEST-NET-3
		"240.0.0.1", "255.255.255.255", "224.0.0.1", "239.255.255.250",
		// IPv6 保留段
		"2001:db8::1",      // 文档地址
		"fe80::1",          // 链路本地
		"ff02::1",          // 组播
		"::",               // 未指定
		"fc00::2",          // ULA
		"fd12:3456::1",     // ULA 高段
		"::ffff:127.0.0.1", // v4-mapped 回环
	}
	for _, s := range nonPublic {
		if IsPublicIP(parseIPHelper(s)) {
			t.Errorf("%s should be non-public", s)
		}
	}
	if IsPublicIP(nil) {
		t.Error("nil IP should be non-public")
	}

	// 保留段边界外应仍判为公共。
	stillPublic := []string{"192.0.3.1", "100.128.0.1", "223.255.255.255"}
	for _, s := range stillPublic {
		if !IsPublicIP(parseIPHelper(s)) {
			t.Errorf("%s should be public", s)
		}
	}
}

// TestValidateURLEdgeInputs 覆盖 SSRF 绕过常见形态。
func TestValidateURLEdgeInputs(t *testing.T) {
	bad := []string{
		"http://",           // 空 host
		"HTTP://10.0.0.1/x", // 大写 scheme（先小写化，仍应拒绝私网）
		"GOPHER://example.com/x",
		"//10.0.0.1/x", // 无 scheme
		" http://example.com/x",
	}
	for _, u := range bad {
		if err := ValidateURL(u, false); err == nil {
			t.Errorf("ValidateURL(%q) = nil, want error", u)
		}
	}
	// Quick 检查同样处理空 host 与缺 scheme。
	for _, u := range []string{"http://", "//x/y", "gopher://x"} {
		if err := ValidateURLQuick(u, false); err == nil {
			t.Errorf("ValidateURLQuick(%q) = nil, want error", u)
		}
	}
	// userinfo 不影响主机校验，仍是合法公网 URL。
	if err := ValidateURL("http://user:pass@example.com/x", false); err != nil {
		t.Errorf("ValidateURL(userinfo) = %v, want nil", err)
	}
}

// lookupStub 替换 lookupIP 变量，避免真实 DNS。
type lookupStub struct {
	ips []net.IPAddr
	err error
}

func (s lookupStub) lookup(ctx context.Context, host string) ([]net.IPAddr, error) {
	return s.ips, s.err
}

func withLookup(t *testing.T, stub lookupStub) {
	t.Helper()
	old := lookupIP
	lookupIP = stub.lookup
	t.Cleanup(func() { lookupIP = old })
}

// TestGuardedDialBlocksPrivateResolved 验证拨号时对私网解析结果的拒绝。
func TestGuardedDialBlocksPrivateResolved(t *testing.T) {
	withLookup(t, lookupStub{ips: []net.IPAddr{{IP: parseIPHelper("192.168.1.1")}}})

	d := &net.Dialer{Timeout: time.Second}
	dial := GuardedDial(false, d)
	_, err := dial(context.Background(), "tcp", "cdn.example.com:80")
	if err == nil || !strings.Contains(err.Error(), "non-public") {
		t.Fatalf("err = %v, want non-public rejection", err)
	}
}

// TestGuardedDialMixedIPs 任一私网 IP 会导致整组拒绝。
func TestGuardedDialMixedIPs(t *testing.T) {
	withLookup(t, lookupStub{ips: []net.IPAddr{
		{IP: parseIPHelper("8.8.8.8")},
		{IP: parseIPHelper("10.0.0.7")},
	}})

	d := &net.Dialer{Timeout: time.Second}
	dial := GuardedDial(false, d)
	if _, err := dial(context.Background(), "tcp", "cdn.example.com:80"); err == nil {
		t.Fatal("mixed public/private resolution was allowed")
	}
}

// TestGuardedDialNoAddresses 解析为空时拒绝。
func TestGuardedDialNoAddresses(t *testing.T) {
	withLookup(t, lookupStub{ips: nil})
	d := &net.Dialer{Timeout: time.Second}
	dial := GuardedDial(false, d)
	_, err := dial(context.Background(), "tcp", "cdn.example.com:80")
	if err == nil || !strings.Contains(err.Error(), "no addresses") {
		t.Fatalf("err = %v, want no-addresses error", err)
	}
}

// TestGuardedDialLookupError 解析失败透传。
func TestGuardedDialLookupError(t *testing.T) {
	withLookup(t, lookupStub{err: errors.New("dns boom")})
	d := &net.Dialer{Timeout: time.Second}
	dial := GuardedDial(false, d)
	if _, err := dial(context.Background(), "tcp", "cdn.example.com:80"); err == nil || !strings.Contains(err.Error(), "dns boom") {
		t.Fatalf("err = %v, want dns boom", err)
	}
}

// TestGuardedDialBadAddr SplitHostPort 失败原样返回。
func TestGuardedDialBadAddr(t *testing.T) {
	withLookup(t, lookupStub{}) // 不应被调用
	d := &net.Dialer{Timeout: time.Second}
	dial := GuardedDial(false, d)
	if _, err := dial(context.Background(), "tcp", "not-a-valid-addr"); err == nil {
		t.Fatal("malformed addr accepted")
	}
}

// TestGuardedDialPublicProceeds 校验通过后真实拨号（拨本地监听端口）。
func TestGuardedDialPublicProceeds(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	// 假解析返回公网 IP，使校验通过；随后的真实拨号
	// 打到真实目标（测试里的回环监听端口）。
	withLookup(t, lookupStub{ips: []net.IPAddr{{IP: parseIPHelper("8.8.8.8")}}})
	d := &net.Dialer{Timeout: 2 * time.Second}
	dial := GuardedDial(false, d)
	conn, err := dial(context.Background(), "tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("guarded dial to listener: %v", err)
	}
	conn.Close()
}

// TestGuardedDialAllowPrivatePassthrough allowPrivate 时直接返回原始
// DialContext，可拨回环目标。
func TestGuardedDialAllowPrivatePassthrough(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	d := &net.Dialer{Timeout: 2 * time.Second}
	dial := GuardedDial(true, d)
	conn, err := dial(context.Background(), "tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("allow-private dial to loopback: %v", err)
	}
	conn.Close()
}
