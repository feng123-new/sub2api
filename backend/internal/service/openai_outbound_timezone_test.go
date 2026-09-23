package service

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenAIOutboundTimezoneHTTPBoundary(t *testing.T) {
	for _, host := range []string{"api.openai.com", "chatgpt.com", "auth.openai.com", "api.anthropic.com", "chatgpt.com.example.org"} {
		t.Run(host, func(t *testing.T) {
			req, _ := http.NewRequest("POST", "https://"+host+"/v1/responses", strings.NewReader(`{"input":"Keep 09:00 Asia/Tokyo unchanged"}`))
			req.Header = http.Header{"x-timezone": {"Asia/Shanghai"}, "X-Timezone": {"Asia/Tokyo", "UTC"}, "Authorization": {"Bearer test"}}
			ApplyOpenAIOutboundTimezone(req)
			if host == "api.anthropic.com" || host == "chatgpt.com.example.org" {
				if req.Header.Get("X-Timezone") != "Asia/Tokyo" {
					t.Fatal("unrelated host changed")
				}
				return
			}
			count := 0
			for k, v := range req.Header {
				if strings.EqualFold(k, "X-Timezone") {
					count += len(v)
				}
			}
			if count != 1 || req.Header.Get("X-Timezone") != OpenAIOutboundTimezone {
				t.Fatal(req.Header)
			}
			if req.Header.Get("Authorization") != "Bearer test" {
				t.Fatal("auth changed")
			}
			body, _ := io.ReadAll(req.Body)
			if !strings.Contains(string(body), "09:00 Asia/Tokyo") {
				t.Fatal("user content changed")
			}
		})
	}
	req, _ := http.NewRequest("GET", "https://chatgpt.com/", nil)
	req.Header = nil
	ApplyOpenAIOutboundTimezone(req)
	if req.Header.Get("X-Timezone") != OpenAIOutboundTimezone {
		t.Fatal("missing default")
	}
}

func TestOpenAIOutboundTimezoneWire(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Timezone") != OpenAIOutboundTimezone {
			t.Errorf("wire timezone: %q", r.Header.Get("X-Timezone"))
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	req, _ := http.NewRequest("GET", server.URL, nil)
	req.Header.Set("X-Timezone", "Asia/Shanghai")
	NormalizeOpenAIOutboundTimezone(req.Header)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
}

func TestOpenAIOutboundTimezoneLeavesHarvestUnchanged(t *testing.T) {
	for _, incoming := range []string{"", "Asia/Tokyo"} {
		req, _ := http.NewRequest("POST", "https://chatgpt.com/backend-api/codex/responses", nil)
		if incoming != "" {
			req.Header.Set("X-Timezone", incoming)
		}
		req = req.WithContext(WithHTTPUpstreamProfile(req.Context(), HTTPUpstreamProfileOpenAIHarvest))
		ApplyOpenAIOutboundTimezone(req)
		if req.Header.Get("X-Timezone") != incoming {
			t.Fatal("harvesting modified")
		}
	}
}
