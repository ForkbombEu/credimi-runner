package config

import (
	"net"
	"strings"
)

const DefaultWiFiPort = "5555"

// AndroidWiFiSerial derives the canonical ADB endpoint for a Wi-Fi device.
func AndroidWiFiSerial(ip, port string) string {
	ip = strings.TrimSpace(ip)
	port = strings.TrimSpace(port)
	if ip == "" {
		return ""
	}
	if port == "" {
		port = DefaultWiFiPort
	}
	return net.JoinHostPort(ip, port)
}
