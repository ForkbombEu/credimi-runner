package runtimesupervisor

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

func localOriginURL(listen string) (string, error) {
	listen = strings.TrimSpace(listen)
	if listen == "" {
		return "", fmt.Errorf("empty API listen address")
	}
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "", fmt.Errorf("parse API listen address %q: %w", listen, err)
	}
	if port == "" {
		return "", fmt.Errorf("API listen address %q has no port", listen)
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 0 || portNumber > 65535 {
		return "", fmt.Errorf("API listen address %q has invalid port", listen)
	}
	switch host {
	case "", "0.0.0.0", "::":
		host = "127.0.0.1"
	}
	origin := "http://" + net.JoinHostPort(host, port)
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Scheme != "http" || parsed.Host == "" {
		if err == nil {
			err = fmt.Errorf("invalid origin")
		}
		return "", fmt.Errorf("build local API origin: %w", err)
	}
	return parsed.String(), nil
}
