package tools

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const (
	fetchMaxBytes = 1 << 20 // read at most 1 MB from the server
	fetchMaxChars = 4000    // return at most this much text: the result goes into an 8k-token context
)

// The URL comes from the model, and the model can be steered by the user's prompt.
// Without these checks it could reach internal services from inside Docker
// (lb:11434 = Ollama, prometheus:9090, grafana:3000) or cloud metadata at 169.254.169.254: SSRF.
var fetchTool = Tool{
	Name:        "fetch_url",
	Description: "Downloads a public web page or API over HTTP(S) with GET and returns its text (truncated). Use it when the user gives a URL or asks about content at a URL.",
	Parameters: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"url": map[string]any{"type": "string", "description": "absolute http:// or https:// URL"},
		},
		"required": []string{"url"},
	},
	Run: fetchURL,
}

// allowIP decides which resolved addresses may be dialled; tests swap it to reach httptest servers.
var allowIP = isPublicIP

func isPublicIP(ip net.IP) bool {
	cgnat := &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}
	return !(ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() ||
		cgnat.Contains(ip))
}

// safeDial resolves the host itself and checks every IP before connecting, so a hostname
// can't point at an internal address (that also covers redirects, which dial again).
func safeDial(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	for _, ip := range ips {
		if !allowIP(ip.IP) {
			return nil, fmt.Errorf("blocked: %s resolves to a non-public address", host)
		}
	}
	d := net.Dialer{Timeout: 5 * time.Second}
	return d.DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
}

var fetchClient = &http.Client{
	// shortcut: fixed timeout instead of the request's context (Tool.Run has no ctx); add one if tools get slow
	Timeout:   10 * time.Second,
	Transport: &http.Transport{DialContext: safeDial}, // no Proxy: a proxy would bypass the IP check
	CheckRedirect: func(_ *http.Request, via []*http.Request) error {
		if len(via) >= 3 {
			return errors.New("too many redirects")
		}
		return nil
	},
}

var (
	scriptOrStyle = regexp.MustCompile(`(?is)<script.*?</script>|<style.*?</style>`)
	htmlTag       = regexp.MustCompile(`(?s)<[^>]*>`)
	spaces        = regexp.MustCompile(`\s+`)
)

func fetchURL(args map[string]any) (string, error) {
	raw, err := stringArg(args, "url", true)
	if err != nil {
		return "", err
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("url must be an absolute http(s) URL, got %q", raw)
	}

	resp, err := fetchClient.Get(u.String())
	if err != nil {
		return "", fmt.Errorf("fetch failed: %w", err)
	}
	defer resp.Body.Close()

	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "text/") && !strings.Contains(ct, "json") && !strings.Contains(ct, "xml") {
		return "", fmt.Errorf("unsupported content type %q: only text, HTML, JSON and XML", ct)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, fetchMaxBytes))
	if err != nil {
		return "", fmt.Errorf("reading body: %w", err)
	}
	text := string(body)
	if strings.Contains(ct, "html") { // keep the readable text, drop markup
		text = htmlTag.ReplaceAllString(scriptOrStyle.ReplaceAllString(text, " "), " ")
	}
	text = strings.TrimSpace(spaces.ReplaceAllString(text, " "))

	if r := []rune(text); len(r) > fetchMaxChars {
		text = string(r[:fetchMaxChars]) + " …[truncated]"
	}
	return fmt.Sprintf("HTTP %d\n\n%s", resp.StatusCode, text), nil
}
