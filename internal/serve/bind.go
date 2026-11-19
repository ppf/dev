package serve

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
)

// DefaultPort is the port of the default bind.
const DefaultPort = 8417

// LANNotice is printed at start on a private LAN bind.
const LANNotice = "plain HTTP on the local network is readable by other devices on that network: use it on a network you trust"

var tailnet = netip.MustParsePrefix("100.64.0.0/10")

// Kind is what an allowed bind address is: loopback, tailnet or a private LAN.
type Kind int

const (
	Loopback Kind = iota
	Tailnet
	LAN
)

// Check is addr's kind, an ip:port; an error says why it is refused: anything but an IPv4 loopback, tailnet
// (100.64.0.0/10) or RFC 1918 address, or IPv6 ::1.
func Check(addr string) (netip.AddrPort, Kind, error) {
	ap, err := netip.ParseAddrPort(addr)
	if err != nil {
		return ap, 0, fmt.Errorf("--addr %q: want ip:port, as 127.0.0.1:%d", addr, DefaultPort)
	}
	ip := ap.Addr().Unmap()
	ap = netip.AddrPortFrom(ip, ap.Port())
	switch {
	case ip.IsUnspecified():
		return ap, 0, fmt.Errorf("--addr %s listens on every interface: refused, pass one loopback, tailnet or private LAN address", addr)
	case ip.IsLoopback():
		return ap, Loopback, nil
	case ip.Is4() && tailnet.Contains(ip):
		return ap, Tailnet, nil
	case ip.Is4() && ip.IsPrivate():
		return ap, LAN, nil
	}
	return ap, 0, fmt.Errorf("--addr %s is not a loopback, tailnet (100.64.0.0/10) or private LAN (10/8, 172.16/12, 192.168/16) address: refused", addr)
}

// Default is the default bind: the Mac's tailnet address (a /32 in 100.64/10) among addrs when it has one, else route's (the private LAN
// address of the default route's interface), on DefaultPort.
func Default(addrs []net.Addr, route func() (netip.Addr, error)) (netip.AddrPort, Kind, error) {
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok {
			// Tailscale gives its address as a /32; a carrier-grade NAT one in 100.64/10 comes with a wider mask
			ones, bits := n.Mask.Size()
			if ip, ok := netip.AddrFromSlice(n.IP); ok && tailnet.Contains(ip.Unmap()) && ones == bits {
				return netip.AddrPortFrom(ip.Unmap(), DefaultPort), Tailnet, nil
			}
		}
	}
	ip, err := route()
	if err == nil && ip.Is4() && ip.IsPrivate() && onLAN(addrs, ip) {
		return netip.AddrPortFrom(ip, DefaultPort), LAN, nil
	}
	none := fmt.Errorf("no tailnet or private LAN address on this Mac: pass --addr <ip:port>, as 127.0.0.1:%d", DefaultPort)
	if err != nil {
		return netip.AddrPort{}, 0, errors.Join(none, err)
	}
	return netip.AddrPort{}, 0, none
}

// onLAN is whether ip is on an interface among addrs with a subnet, not a point-to-point /32: a full-tunnel VPN's
// address takes the default route, and a phone on the Wi-Fi can't reach it.
func onLAN(addrs []net.Addr, ip netip.Addr) bool {
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok {
			ones, bits := n.Mask.Size()
			if a, ok := netip.AddrFromSlice(n.IP); ok && a.Unmap() == ip && ones < bits {
				return true
			}
		}
	}
	return false
}

// DefaultRoute is the local address of the default route's interface: a UDP "connect" picks it and sends nothing.
func DefaultRoute() (netip.Addr, error) {
	conn, err := net.Dial("udp4", "192.0.2.1:9")
	if err != nil {
		return netip.Addr{}, fmt.Errorf("default route: %w", err)
	}
	defer func() { _ = conn.Close() }()
	return conn.LocalAddr().(*net.UDPAddr).AddrPort().Addr().Unmap(), nil
}
