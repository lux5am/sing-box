package rule

import (
	"strings"

	"github.com/sagernet/sing-box/adapter"
)

var _ RuleItem = (*DomainKeywordItem)(nil)

type DomainKeywordItem struct {
	keywords []string
}

func NewDomainKeywordItem(keywords []string) *DomainKeywordItem {
	return &DomainKeywordItem{keywords}
}

func (r *DomainKeywordItem) match(domainHost string) bool {
	for _, keyword := range r.keywords {
		if strings.Contains(domainHost, keyword) {
			return true
		}
	}
	return false
}

func (r *DomainKeywordItem) Match(metadata *adapter.InboundContext) bool {
	return (metadata.Destination.Fqdn != "" && r.match(strings.ToLower(metadata.Destination.Fqdn))) ||
		(metadata.SniffHost != "" && metadata.SniffHost != metadata.Destination.Fqdn && r.match(strings.ToLower(metadata.SniffHost))) ||
		(metadata.Domain != "" && metadata.Domain != metadata.SniffHost && metadata.Domain != metadata.Destination.Fqdn && r.match(strings.ToLower(metadata.Domain)))
}

func (r *DomainKeywordItem) String() string {
	kLen := len(r.keywords)
	if kLen == 1 {
		return "domain_keyword=" + r.keywords[0]
	} else if kLen > 3 {
		return "domain_keyword=[" + strings.Join(r.keywords[:3], " ") + "...]"
	} else {
		return "domain_keyword=[" + strings.Join(r.keywords, " ") + "]"
	}
}
