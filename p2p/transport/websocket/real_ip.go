package websocket

import (
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
)

// DefaultGetRealAddr implements RFC 7239 Forwarded header parsing
func DefaultGetRealAddr(addr net.Addr, h http.Header) string {
	remoteAddr := addr.String()
	remoteIp := GetRealIPFromHeader(h)
	if remoteIp != nil {
		remoteTcpAddr, ok := addr.(*net.TCPAddr)
		if ok {
			remoteAddr = IpPort(remoteIp, strconv.Itoa(remoteTcpAddr.Port))
		} else {
			_, port, err := net.SplitHostPort(remoteAddr)
			if err == nil {
				remoteAddr = IpPort(remoteIp, port)
			}
		}
	}
	return remoteAddr

}

// GetRealIPFromHeader extracts the client's real IP address from HTTP request header.
// implements RFC 7239 Forwarded header parsing
func GetRealIPFromHeader(h http.Header) net.IP {
	// RFC 7239 Forwarded header
	if forwarded := h.Get("Forwarded"); forwarded != "" {
		if host := parseForwardedHeader(forwarded); host != "" {
			if ip := validateIp(host); ip != nil {
				return ip
			}
		}
	}

	// Fallback to X-Forwarded-For
	if xff := h.Get("X-Forwarded-For"); xff != "" {
		if host := parseXForwardedFor(xff); host != "" {
			if ip := validateIp(host); ip != nil {
				return ip
			}
		}
	}

	return nil
}

func parseForwardedHeader(value string) string {
	parts := strings.Split(value, ",")
	if len(parts) == 0 {
		return ""
	}

	pair := strings.TrimSpace(parts[0])
	for _, elem := range strings.Split(pair, ";") {
		if kv := strings.Split(strings.TrimSpace(elem), "="); len(kv) == 2 {
			if strings.ToLower(kv[0]) == "for" {
				host := strings.Trim(kv[1], "\"[]")
				return host
			}
		}
	}
	return ""
}

func parseXForwardedFor(value string) string {
	ips := strings.Split(value, ",")
	if len(ips) == 0 {
		return ""
	}
	return strings.TrimSpace(ips[0])
}

// validateIp checks if a string is a valid IP address
func validateIp(ip string) net.IP {
	if ip == "" {
		return nil
	}
	return net.ParseIP(ip)
}

func IpPort(ip net.IP, port string) string {
	if ip.To4() == nil {
		return fmt.Sprintf("[%s]:%s", ip.String(), port)
	}
	return fmt.Sprintf("%s:%s", ip.String(), port)
}
