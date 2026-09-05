package analytics

import (
	useragent "github.com/medama-io/go-useragent"

	"github.com/utkayd/qurator/internal/domain"
)

// UnknownFamily is the ua_family recorded when the browser cannot be identified,
// including for every bot: bots are a device category, not a browser family.
const UnknownFamily = "unknown"

// Classification is what a User-Agent string reduces to. Nothing else about the request
// is derived here.
type Classification struct {
	UAFamily       string
	DeviceCategory domain.DeviceCategory
	IsBot          bool
}

// Classifier wraps the medama-io/go-useragent trie parser (research.md §4: ~315ns,
// zero allocations). One Classifier is safe for concurrent use.
type Classifier struct {
	p *useragent.Parser
}

// NewClassifier builds the parser. It is a few hundred KB of trie; construct once.
func NewClassifier() *Classifier {
	return &Classifier{p: useragent.NewParser()}
}

// Classify maps a raw User-Agent header to a family and device category. Bots are tagged
// (IsBot=true, DeviceCategory=bot) and recorded normally — they are analytics, not noise
// to be discarded. Unrecognised agents are "unknown"/unknown.
func (c *Classifier) Classify(ua string) Classification {
	if ua == "" {
		return Classification{UAFamily: UnknownFamily, DeviceCategory: domain.DeviceUnknown}
	}
	r := c.p.Parse(ua)
	out := Classification{UAFamily: string(r.Browser())}
	if out.UAFamily == "" {
		out.UAFamily = UnknownFamily
	}
	switch {
	case r.IsBot():
		out.IsBot = true
		out.DeviceCategory = domain.DeviceBot
		out.UAFamily = UnknownFamily
	case r.IsTablet():
		out.DeviceCategory = domain.DeviceTablet
	case r.IsMobile():
		out.DeviceCategory = domain.DeviceMobile
	case r.IsTV():
		out.DeviceCategory = domain.DeviceTV
	case r.IsDesktop():
		out.DeviceCategory = domain.DeviceDesktop
	default:
		out.DeviceCategory = domain.DeviceUnknown
	}
	return out
}
