package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/khangphantz/aatmt/internal/store"
)

// publicOnly refuses connections to loopback / private / link-local addresses,
// so web_fetch cannot be used to reach the local machine or the intranet.
func publicOnly(network, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() || ip.IsMulticast() {
		return fmt.Errorf("refusing non-public address %s", host)
	}
	return nil
}

var webClient = &http.Client{
	Timeout: 25 * time.Second,
	Transport: &http.Transport{
		Proxy:       http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{Timeout: 10 * time.Second, Control: publicOnly}).DialContext,
	},
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) > 5 {
			return errors.New("too many redirects")
		}
		if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
			return errors.New("redirect to a non-http URL")
		}
		return nil
	},
}

var (
	reScript = regexp.MustCompile(`(?is)<(script|style|noscript|svg|head)[^>]*>.*?</(script|style|noscript|svg|head)>`)
	reBlock  = regexp.MustCompile(`(?i)</?(p|div|br|li|tr|h[1-6]|section|article|pre|table|ul|ol)[^>]*>`)
	reTag    = regexp.MustCompile(`<[^>]+>`)
	reSpaces = regexp.MustCompile(`[ \t]+`)
	reLines  = regexp.MustCompile(`\n\s*\n+`)
)

func htmlToText(s string) string {
	s = reScript.ReplaceAllString(s, "")
	s = reBlock.ReplaceAllString(s, "\n")
	s = reTag.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	s = reSpaces.ReplaceAllString(s, " ")
	s = reLines.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}

func webFetch(ctx context.Context, raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", errors.New("web_fetch needs an absolute http(s) URL")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "AATMT/1.0 (+task-agent)")
	res, err := webClient.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 2<<20))
	if err != nil {
		return "", err
	}
	if res.StatusCode >= 400 {
		return "", fmt.Errorf("HTTP %d", res.StatusCode)
	}
	text := string(body)
	if ct := res.Header.Get("Content-Type"); strings.Contains(ct, "html") || strings.Contains(text[:min(len(text), 512)], "<html") {
		text = htmlToText(text)
	}
	return fmt.Sprintf("URL: %s\n\n%s", res.Request.URL, text), nil
}

func webSearch(ctx context.Context, st store.Settings, q string) (string, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return "", errors.New("empty query")
	}
	key := store.ResolveKey(st.WebSearchKey)
	if key == "" {
		return "", errors.New("web search API key is not configured")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var b strings.Builder
	switch st.WebSearch {
	case "tavily":
		payload, _ := json.Marshal(map[string]any{"query": q, "max_results": 6})
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.tavily.com/search", strings.NewReader(string(payload)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+key)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			return "", err
		}
		defer res.Body.Close()
		var out struct {
			Results []struct {
				Title, URL, Content string
			} `json:"results"`
		}
		if res.StatusCode != 200 {
			return "", fmt.Errorf("tavily HTTP %d", res.StatusCode)
		}
		if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
			return "", err
		}
		for _, r := range out.Results {
			fmt.Fprintf(&b, "- %s\n  %s\n  %s\n", r.Title, r.URL, excerpt(r.Content, 400))
		}
	case "brave":
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.search.brave.com/res/v1/web/search?count=6&q="+url.QueryEscape(q), nil)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("X-Subscription-Token", key)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			return "", err
		}
		defer res.Body.Close()
		if res.StatusCode != 200 {
			return "", fmt.Errorf("brave HTTP %d", res.StatusCode)
		}
		var out struct {
			Web struct {
				Results []struct {
					Title       string `json:"title"`
					URL         string `json:"url"`
					Description string `json:"description"`
				} `json:"results"`
			} `json:"web"`
		}
		if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
			return "", err
		}
		for _, r := range out.Web.Results {
			fmt.Fprintf(&b, "- %s\n  %s\n  %s\n", htmlToText(r.Title), r.URL, htmlToText(r.Description))
		}
	default:
		return "", errors.New("web search is disabled")
	}
	if b.Len() == 0 {
		return "no results", nil
	}
	return b.String(), nil
}
