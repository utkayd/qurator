package domain

import (
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// ParseScanOrigin validates the operator-controlled origin encoded into dynamic QR
// images. Empty means unconfigured. Routes are rooted at /, so path prefixes are not
// supported. A request's Host or forwarded headers must never supply this value.
func ParseScanOrigin(raw string) (*url.URL, error) {
	if raw == "" {
		return nil, nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" ||
		u.User != nil || (u.Path != "" && u.Path != "/") || u.RawPath != "" ||
		u.RawQuery != "" || u.ForceQuery || strings.Contains(raw, "#") || strings.HasSuffix(u.Host, ":") {
		return nil, errors.New("must be an absolute http or https origin without credentials, path prefix, query, or fragment")
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return nil, errors.New("port must be between 1 and 65535")
		}
	}
	if strings.Contains(u.Hostname(), ":") && net.ParseIP(u.Hostname()) == nil {
		return nil, errors.New("invalid IP host")
	}
	u.Path = ""
	return u, nil
}
