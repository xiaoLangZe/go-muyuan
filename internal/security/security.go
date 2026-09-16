// Package security 实现下载器的 SSRF 防护。
//
// 防护分三层：
//   - ValidateURLQuick：入队时的 DNS-free 快检，强制 http/https 并
//     拒绝以字面量形式给出的不可路由主机；
//   - ValidateURL：含 DNS 解析的权威主机校验；
//   - GuardedDial：拨号时再次校验 *已解析* 地址，防 DNS 重绑定。
package security

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// ValidateURLQuick 执行不做 DNS 的 SSRF 检查子集：强制 http/https
// scheme，并拒绝以字面量形式给出的明显不可路由主机（IP 字面量或
// "localhost"）。主机名在此被接受，稍后由 ValidateURL 解析。
//
// 它的存在是为了让批量入队大量 URL 的调用方能立即得到对畸形或
// 内网地址目标的反馈，而不必为每条付出一次 DNS 查询；权威检查
// （含拨号时强制）仍在下载开始时运行。
func ValidateURLQuick(raw string, allowPrivate bool) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("parse url: %w", err)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("scheme %q not allowed (only http/https)", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("empty host")
	}
	if allowPrivate {
		return nil
	}
	if strings.EqualFold(host, "localhost") {
		return fmt.Errorf("host %q is localhost", host)
	}
	if ip := net.ParseIP(host); ip != nil && !IsPublicIP(ip) {
		return fmt.Errorf("host %q is a non-public address", host)
	}
	return nil
}

// ValidateProxyURL 校验可选的代理 URL。scheme 仅允许 http、https、
// socks5 与 socks5h；主机不做公网强制，因为代理常位于内网。
func ValidateProxyURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("parse proxy url: %w", err)
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "socks5", "socks5h":
	default:
		return fmt.Errorf("scheme %q not allowed for proxy (http/https/socks5/socks5h)", u.Scheme)
	}
	if u.Hostname() == "" {
		return fmt.Errorf("empty proxy host")
	}
	return nil
}

// ValidateURL 强制本包的 SSRF 防护：
//   - scheme 必须是 http 或 https；
//   - 主机会被解析，若是 localhost、loopback、私有（RFC1918）、
//     link-local、multicast、unspecified 或其他保留地址则被拒绝；
//   - allowPrivate 为主机检查放行，用于合法的 LAN/loopback 场景。
//
// 主机既以字面量形式检查（"localhost"、IPv4 字面量、带方括号的
// IPv6），也在 DNS 解析后检查，因此 DNS 重绑定式目标也会被捕获。
func ValidateURL(raw string, allowPrivate bool) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("parse url: %w", err)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("scheme %q not allowed (only http/https)", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("empty host")
	}
	if !allowPrivate {
		if err := checkHostPublic(host); err != nil {
			return err
		}
	}
	return nil
}

// checkHostPublic 拒绝任何不是可解析公共地址的主机。
// 它接受主机名（随后解析）和 IP 字面量。
func checkHostPublic(host string) error {
	// 在任何查找之前，先拒绝字面量 "localhost"。
	if strings.EqualFold(host, "localhost") {
		return fmt.Errorf("host %q is localhost", host)
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("lookup host %q: %w", host, err)
	}
	for _, ip := range ips {
		if !IsPublicIP(ip) {
			return fmt.Errorf("host %q resolves to non-public %s", host, ip)
		}
	}
	return nil
}

// lookupIP 解析主机名。它是包变量以便测试注入假解析结果，
// 产品代码不修改它。
var lookupIP = func(ctx context.Context, host string) ([]net.IPAddr, error) {
	return net.DefaultResolver.LookupIPAddr(ctx, host)
}

// GuardedDial 返回一个在连接时再次校验 *已解析* 地址的 DialContext。
// 这是权威的 SSRF 强制点：它捕获 DNS 重绑定（一个在验证时解析为
// 公网、实际拨号时指向私网的名字）以及对内网目标的跳转。
//
// 当 allowPrivate 为 true 时，返回的函数不做任何检查。
func GuardedDial(allowPrivate bool, dialer *net.Dialer) func(ctx context.Context, network, addr string) (net.Conn, error) {
	if allowPrivate {
		return dialer.DialContext
	}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		ips, err := lookupIP(ctx, host)
		if err != nil {
			return nil, err
		}
		if len(ips) == 0 {
			return nil, fmt.Errorf("host %q resolved to no addresses", host)
		}
		for _, ip := range ips {
			if !IsPublicIP(ip.IP) {
				return nil, fmt.Errorf("connection to non-public address %s blocked (host %q)", ip.IP, host)
			}
		}
		return dialer.DialContext(ctx, network, addr)
	}
}

// IsPublicIP 报告 ip 是否为公共、可路由地址。任何处于
// loopback / 私有 / link-local / multicast / unspecified / 保留
// 范围的地址都视为非公共。
func IsPublicIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsUnspecified() || ip.IsLoopback() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsMulticast() {
		return false
	}
	if ip.IsPrivate() {
		return false
	}
	// net.IsPrivate 未覆盖的额外保留范围。
	if v4 := ip.To4(); v4 != nil {
		// 0.0.0.0/8, 100.64/10 (CGNAT), 192.0.0.0/24, 192.0.2.0/24 (TEST-NET-1),
		// 198.18.0.0/15, 198.51.100.0/24 (TEST-NET-2), 203.0.113.0/24 (TEST-NET-3),
		// 240.0.0.0/4 (保留/未来使用)。
		reserved := []string{
			"0.0.0.0/8",
			"100.64.0.0/10",
			"192.0.0.0/24",
			"192.0.2.0/24",
			"198.18.0.0/15",
			"198.51.100.0/24",
			"203.0.113.0/24",
			"240.0.0.0/4",
		}
		for _, cidr := range reserved {
			if _, n, err := net.ParseCIDR(cidr); err == nil && n.Contains(ip) {
				return false
			}
		}
		return true
	}
	// IPv6：除上述 link-local/multicast/unspecified 检查外，
	// 还将 ULA fc00::/7 和文档地址 2001:db8::/32 视为非公共。
	ula := []string{"fc00::/7", "2001:db8::/32"}
	for _, cidr := range ula {
		if _, n, err := net.ParseCIDR(cidr); err == nil && n.Contains(ip) {
			return false
		}
	}
	return true
}
