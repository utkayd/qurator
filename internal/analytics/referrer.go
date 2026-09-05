package analytics

import (
	"net/url"
	"strings"
)

// ReferrerHost reduces a Referer header to its lowercase host and nothing else. Path,
// query, fragment, port and userinfo never leave this function (FR-022 in spirit: the
// referrer is the one place a scan could smuggle a per-person token into storage). An
// unparsable or hostless value yields "", which the rollups record as a direct scan.
func ReferrerHost(referer string) string {
	if referer == "" {
		return ""
	}
	u, err := url.Parse(referer)
	if err != nil {
		return ""
	}
	host := u.Hostname()
	if host == "" {
		return ""
	}
	return strings.ToLower(host)
}
