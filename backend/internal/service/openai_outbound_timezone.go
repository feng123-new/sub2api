package service

import (
	"net/http"
	"strings"
)

const OpenAIOutboundTimezone = "America/Los_Angeles"

func NormalizeOpenAIOutboundTimezone(headers http.Header) {
	for key := range headers {
		if strings.EqualFold(key, "X-Timezone") {
			delete(headers, key)
		}
	}
	headers.Set("X-Timezone", OpenAIOutboundTimezone)
}

func ApplyOpenAIOutboundTimezone(req *http.Request) {
	if req == nil || req.URL == nil {
		return
	}
	if HTTPUpstreamProfileFromContext(req.Context()) == HTTPUpstreamProfileOpenAIHarvest {
		return
	}
	host := strings.ToLower(req.URL.Hostname())
	if host != "openai.com" && !strings.HasSuffix(host, ".openai.com") && host != "chatgpt.com" && !strings.HasSuffix(host, ".chatgpt.com") {
		return
	}
	if req.Header == nil {
		req.Header = make(http.Header)
	}
	NormalizeOpenAIOutboundTimezone(req.Header)
}
