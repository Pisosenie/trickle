package phone

import (
	"errors"
	"net"
	"slices"
	"strings"
)

var virtualPrefixes = []string{"docker", "veth", "virbr", "br-", "tailscale", "zt", "tun", "tap", "wg"}

func lanIPv4() (net.IP, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var wired net.IP
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 || isVirtual(iface.Name) {
			continue
		}
		ip := privateIPv4(iface)
		if ip == nil {
			continue
		}
		if strings.HasPrefix(iface.Name, "wl") {
			return ip, nil
		}
		if wired == nil {
			wired = ip
		}
	}
	if wired == nil {
		return nil, errors.New("not connected to a local network")
	}
	return wired, nil
}

func isVirtual(name string) bool {
	return slices.ContainsFunc(virtualPrefixes, func(prefix string) bool { return strings.HasPrefix(name, prefix) })
}

func privateIPv4(iface net.Interface) net.IP {
	addrs, err := iface.Addrs()
	if err != nil {
		return nil
	}
	for _, addr := range addrs {
		ipnet, ok := addr.(*net.IPNet)
		if !ok {
			continue
		}
		if ip4 := ipnet.IP.To4(); ip4 != nil && ip4.IsPrivate() {
			return ip4
		}
	}
	return nil
}
