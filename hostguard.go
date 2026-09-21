package go_muyuan

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strings"
)

// The downloader fetches URLs that may come from an untrusted place: a config
// file, a user, a remote API. Left unchecked, such a URL can point at the local
// machine or at services reachable only from inside the network. Two layers
// stop that: the target is checked before a request is built, and the address
// is checked again at dial time, so a hostname that resolves differently on the
// second lookup cannot slip past.

// blockedPrefixes lists address ranges that are never valid download targets:
// special-purpose, documentation, benchmarking and shared address space that is
// either unroutable or reachable only from inside a network.
var blockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),       // "this network"
	netip.MustParsePrefix("100.64.0.0/10"),   // carrier-grade NAT
	netip.MustParsePrefix("192.0.0.0/24"),    // IETF protocol assignments
	netip.MustParsePrefix("192.0.2.0/24"),    // documentation (TEST-NET-1)
	netip.MustParsePrefix("192.88.99.0/24"),  // 6to4 relay anycast
	netip.MustParsePrefix("198.18.0.0/15"),   // benchmarking
	netip.MustParsePrefix("198.51.100.0/24"), // documentation (TEST-NET-2)
	netip.MustParsePrefix("203.0.113.0/24"),  // documentation (TEST-NET-3)
	netip.MustParsePrefix("240.0.0.0/4"),     // reserved, covers the broadcast address
	netip.MustParsePrefix("64:ff9b::/96"),    // NAT64 well-known prefix
	netip.MustParsePrefix("100::/64"),        // discard-only
	netip.MustParsePrefix("2001:db8::/32"),   // documentation
	netip.MustParsePrefix("2002::/16"),       // 6to4
}

// checkAddr reports whether a resolved address may be dialed.
func checkAddr(addr netip.Addr) error {
	if !addr.IsValid() {
		return fmt.Errorf("%w: unusable address", ErrBlockedHost)
	}
	// IPv4-mapped IPv6 addresses are judged as the IPv4 address they carry.
	ip := addr.Unmap()
	switch {
	case ip.IsLoopback():
		return fmt.Errorf("%w: %s is a loopback address", ErrBlockedHost, ip)
	case ip.IsPrivate():
		return fmt.Errorf("%w: %s is a private address", ErrBlockedHost, ip)
	case ip.IsUnspecified():
		return fmt.Errorf("%w: %s is an unspecified address", ErrBlockedHost, ip)
	case ip.IsLinkLocalUnicast(), ip.IsLinkLocalMulticast(), ip.IsInterfaceLocalMulticast():
		return fmt.Errorf("%w: %s is a link-local address", ErrBlockedHost, ip)
	case ip.IsMulticast():
		return fmt.Errorf("%w: %s is a multicast address", ErrBlockedHost, ip)
	}
	for _, prefix := range blockedPrefixes {
		if prefix.Contains(ip) {
			return fmt.Errorf("%w: %s is in reserved range %s", ErrBlockedHost, ip, prefix)
		}
	}
	return nil
}

// checkHost validates a URL host before any request is built. A hostname is
// rejected when any of the addresses it resolves to is off limits, because
// which one gets dialed is not under our control.
func checkHost(ctx context.Context, host string) error {
	host = strings.TrimSuffix(strings.TrimSpace(host), ".")
	if host == "" {
		return fmt.Errorf("%w: empty host", ErrBlockedHost)
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		return checkAddr(addr)
	}
	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return fmt.Errorf("%w: cannot resolve %q: %v", ErrBlockedHost, host, err)
	}
	if len(addrs) == 0 {
		return fmt.Errorf("%w: %q has no address", ErrBlockedHost, host)
	}
	for _, addr := range addrs {
		if err := checkAddr(addr); err != nil {
			return err
		}
	}
	return nil
}

// guardedDialer dials the addresses that passed checkAddr and nothing else.
// Re-checking here, rather than trusting the earlier lookup, closes the window
// in which a name resolves to a public address once and to an internal one the
// next time.
type guardedDialer struct {
	dialer *net.Dialer
}

func (d *guardedDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("%w: cannot resolve %q: %v", ErrBlockedHost, host, err)
	}
	var (
		conn    net.Conn
		lastErr error
	)
	for _, addr := range addrs {
		if err := checkAddr(addr); err != nil {
			lastErr = err
			continue
		}
		conn, err = d.dialer.DialContext(ctx, network, net.JoinHostPort(addr.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("%w: %q has no usable address", ErrBlockedHost, host)
	}
	return nil, lastErr
}
