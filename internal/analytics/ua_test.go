package analytics

import (
	"testing"

	"github.com/utkayd/qurator/internal/domain"
)

// T071: user-agent classification and referrer reduction.
func TestClassifyUA(t *testing.T) {
	cases := []struct {
		name, ua string
		device   domain.DeviceCategory
		family   string
		bot      bool
	}{
		{"googlebot", "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)", domain.DeviceBot, "unknown", true},
		{"facebook", "facebookexternalhit/1.1 (+http://www.facebook.com/externalhit_uatext.php)", domain.DeviceBot, "unknown", true},
		{"slack", "Slackbot-LinkExpanding 1.0 (+https://api.slack.com/robots)", domain.DeviceBot, "unknown", true},
		{"curl", "curl/8.4.0", domain.DeviceBot, "unknown", true},
		{"iphone safari", "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1", domain.DeviceMobile, "Safari", false},
		{"windows chrome", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36", domain.DeviceDesktop, "Chrome", false},
		{"ipad", "Mozilla/5.0 (iPad; CPU OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1", domain.DeviceTablet, "Safari", false},
		{"tizen tv", "Mozilla/5.0 (SMART-TV; Linux; Tizen 6.0) AppleWebKit/537.36 (KHTML, like Gecko) Version/3.0 Chrome/76.0 TV Safari/537.36", domain.DeviceTV, "unknown", false},
		{"empty", "", domain.DeviceUnknown, "unknown", false},
		{"garbage", "garbage", domain.DeviceUnknown, "unknown", false},
	}
	cl := NewClassifier()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := cl.Classify(tc.ua)
			if got.DeviceCategory != tc.device || got.UAFamily != tc.family || got.IsBot != tc.bot {
				t.Fatalf("Classify(%q) = %+v, want device=%s family=%s bot=%v", tc.ua, got, tc.device, tc.family, tc.bot)
			}
			if got.IsBot != (got.DeviceCategory == domain.DeviceBot) {
				t.Fatalf("IsBot and DeviceCategory disagree: %+v", got)
			}
		})
	}
}

func TestReferrerHost(t *testing.T) {
	cases := map[string]string{
		"https://a.example.com/path?token=secret":   "a.example.com",
		"https://A.Example.COM:8443/x#frag":         "a.example.com",
		"http://user:pass@b.example.com/":           "b.example.com",
		"https://[2001:db8::1]:443/p":               "2001:db8::1",
		"android-app://com.example.app/":            "com.example.app",
		"":                                          "",
		"not a url at all":                          "",
		"/relative/path?x=1":                        "",
		"https://":                                  "",
		"http://%zz.example.com/":                   "",
		"https://c.example.com/?token=secret#s=1":   "c.example.com",
		"HTTPS://d.example.com/./path/../other?q=1": "d.example.com",
	}
	for in, want := range cases {
		if got := ReferrerHost(in); got != want {
			t.Errorf("ReferrerHost(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEventFromRequestFieldsNeverCarryPathOrQuery(t *testing.T) {
	h := ReferrerHost("https://a.example.com/very/secret/path?token=abc&sid=123")
	for _, forbidden := range []string{"/", "?", "token", "secret", "sid"} {
		if containsFold(h, forbidden) {
			t.Fatalf("ReferrerHost leaked %q in %q", forbidden, h)
		}
	}
}

func containsFold(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
