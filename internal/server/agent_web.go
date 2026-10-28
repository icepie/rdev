package server

// Web tools (omp web_search/read-URL parity): web_fetch retrieves one URL
// server-side and converts HTML to readable markdown-ish text; web_search
// runs a keyless DuckDuckGo HTML query and returns ranked results. Both run
// on the SERVER (not the device) — the device may have no internet access.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/html"
)

const (
	webFetchMaxBytes  = 1024 * 1024
	webFetchTextMax   = 24 * 1024
	webSearchMaxHits  = 10
	webRequestTimeout = 30 * time.Second
)

var webHTTPClient = &http.Client{Timeout: webRequestTimeout}

// --- web_fetch ---

type webFetchTool struct{}

func (t *webFetchTool) Name() string { return "web_fetch" }

func (t *webFetchTool) Description() string {
	return "Fetch a URL from the server (the device is not used). HTML pages are converted to readable text with links preserved as [text](href); other content types are returned raw (text) or described (binary). Capped at ~24KB of text."
}

func (t *webFetchTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"url": {"type": "string", "description": "http(s) URL to fetch"}
		},
		"required": ["url"]
	}`)
}

func (t *webFetchTool) Dangerous() bool { return false }

func (t *webFetchTool) Summary(raw json.RawMessage) string {
	var args struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return ""
	}
	return args.URL
}

func (t *webFetchTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %v", err)
	}
	u, err := url.Parse(args.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("invalid url %q (http/https only)", args.URL)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, args.URL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; rdev-agent/1.0)")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,text/plain,application/json;q=0.9,*/*;q=0.5")

	resp, err := webHTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch %q: %v", args.URL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("fetch %q: HTTP %d", args.URL, resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, webFetchMaxBytes+1))
	if err != nil {
		return "", fmt.Errorf("fetch %q: %v", args.URL, err)
	}
	truncated := len(body) > webFetchMaxBytes
	if truncated {
		body = body[:webFetchMaxBytes]
	}

	ctype := resp.Header.Get("Content-Type")
	var out string
	if strings.Contains(ctype, "html") {
		out = htmlToMarkdown(string(body), resp.Request.URL)
	} else if strings.Contains(ctype, "text") || strings.Contains(ctype, "json") ||
		strings.Contains(ctype, "xml") || strings.Contains(ctype, "javascript") || ctype == "" {
		out = string(body)
	} else {
		return fmt.Sprintf("[binary content, %s, %d bytes — not shown]", ctype, len(body)), nil
	}
	if len(out) > webFetchTextMax {
		out = out[:webFetchTextMax] + fmt.Sprintf("\n[truncated at %d chars]", webFetchTextMax)
	}
	if truncated {
		out += "\n[response body truncated at 1MB]"
	}
	return out, nil
}

// htmlToMarkdown extracts readable text from an HTML document: scripts,
// styles, nav chrome and hidden nodes are dropped; headings, lists, links
// and code survive as markdown-ish text.
func htmlToMarkdown(doc string, base *url.URL) string {
	root, err := html.Parse(strings.NewReader(doc))
	if err != nil {
		return doc
	}
	var b strings.Builder
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "script", "style", "noscript", "template", "svg", "head", "iframe", "form", "button", "select":
				return
			case "h1", "h2", "h3", "h4", "h5", "h6":
				level := int(n.Data[1] - '0')
				b.WriteString("\n\n" + strings.Repeat("#", level) + " ")
				walkChildren(n, walk)
				b.WriteString("\n")
				return
			case "p", "div", "section", "article", "header", "footer", "table", "tr", "ul", "ol", "blockquote", "pre":
				b.WriteString("\n")
				walkChildren(n, walk)
				b.WriteString("\n")
				return
			case "br", "hr", "td", "th", "li":
				if n.Data == "li" {
					b.WriteString("\n- ")
				} else {
					b.WriteString("\n")
				}
				walkChildren(n, walk)
				return
			case "a":
				href := ""
				for _, a := range n.Attr {
					if a.Key == "href" {
						href = a.Val
					}
				}
				var text strings.Builder
				collectText(n, &text)
				label := strings.TrimSpace(text.String())
				if label == "" || href == "" || strings.HasPrefix(href, "#") || strings.HasPrefix(href, "javascript:") {
					b.WriteString(label)
				} else {
					if ref, err := url.Parse(href); err == nil && base != nil {
						href = base.ResolveReference(ref).String()
					}
					fmt.Fprintf(&b, "[%s](%s)", label, href)
				}
				return
			case "code", "kbd", "samp":
				b.WriteByte('`')
				walkChildren(n, walk)
				b.WriteByte('`')
				return
			case "img":
				for _, a := range n.Attr {
					if a.Key == "alt" && a.Val != "" {
						fmt.Fprintf(&b, "[image: %s]", a.Val)
						break
					}
				}
				return
			}
		}
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
			return
		}
		walkChildren(n, walk)
	}
	walk(root)
	return strings.TrimSpace(collapseWhitespace(b.String()))
}

func walkChildren(n *html.Node, walk func(*html.Node)) {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		walk(c)
	}
}

func collectText(n *html.Node, b *strings.Builder) {
	if n.Type == html.TextNode {
		b.WriteString(n.Data)
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		collectText(c, b)
	}
}

// collapseWhitespace squeezes runs of spaces/tabs and 3+ newlines.
func collapseWhitespace(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	newlines := 0
	space := false
	for _, r := range s {
		switch {
		case r == '\n':
			newlines++
			space = false
			if newlines <= 2 {
				b.WriteByte('\n')
			}
		case r == ' ' || r == '\t' || r == '\r':
			space = true
		default:
			if space && newlines == 0 && b.Len() > 0 {
				b.WriteByte(' ')
			}
			space = false
			newlines = 0
			b.WriteRune(r)
		}
	}
	return b.String()
}

// --- web_search ---

type webSearchTool struct{}

func (t *webSearchTool) Name() string { return "web_search" }

func (t *webSearchTool) Description() string {
	return "Search the web from the server (keyless DuckDuckGo). Returns ranked results with title, URL and snippet. Follow up with web_fetch on a result URL to read the page."
}

func (t *webSearchTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"query": {"type": "string", "description": "Search query"},
			"limit": {"type": "integer", "description": "Max results (default 10, cap 10)"}
		},
		"required": ["query"]
	}`)
}

func (t *webSearchTool) Dangerous() bool { return false }

func (t *webSearchTool) Summary(raw json.RawMessage) string {
	var args struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return ""
	}
	return args.Query
}

func (t *webSearchTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %v", err)
	}
	if strings.TrimSpace(args.Query) == "" {
		return "", fmt.Errorf("query is required")
	}
	limit := args.Limit
	if limit <= 0 || limit > webSearchMaxHits {
		limit = webSearchMaxHits
	}

	endpoint := "https://html.duckduckgo.com/html/?q=" + url.QueryEscape(args.Query)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0 Safari/537.36")

	resp, err := webHTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("search: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("search: HTTP %d from backend", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return "", fmt.Errorf("search: %v", err)
	}

	hits := parseDDGResults(string(body), limit)
	if len(hits) == 0 {
		return fmt.Sprintf("no results for %q", args.Query), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "results for %q:\n", args.Query)
	for i, h := range hits {
		fmt.Fprintf(&b, "%d. %s\n   %s\n", i+1, h.title, h.url)
		if h.snippet != "" {
			fmt.Fprintf(&b, "   %s\n", h.snippet)
		}
	}
	return b.String(), nil
}

type ddgHit struct {
	title, url, snippet string
}

// parseDDGResults extracts results from the DuckDuckGo HTML endpoint:
// anchors with class result__a carry title+href (uddg-wrapped), result__snippet
// divs carry the snippet text.
func parseDDGResults(doc string, limit int) []ddgHit {
	root, err := html.Parse(strings.NewReader(doc))
	if err != nil {
		return nil
	}
	var hits []ddgHit
	var snippets []string
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			cls := attrOf(n, "class")
			switch {
			case n.Data == "a" && strings.Contains(cls, "result__a"):
				var text strings.Builder
				collectText(n, &text)
				hits = append(hits, ddgHit{title: strings.TrimSpace(text.String()), url: unwrapDDGURL(attrOf(n, "href"))})
			case strings.Contains(cls, "result__snippet"):
				var text strings.Builder
				collectText(n, &text)
				snippets = append(snippets, strings.TrimSpace(text.String()))
			}
		}
		walkChildren(n, walk)
	}
	walk(root)
	for i := range hits {
		if i < len(snippets) {
			hits[i].snippet = snippets[i]
		}
	}
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits
}

func attrOf(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

// unwrapDDGURL resolves DuckDuckGo's //duckduckgo.com/l/?uddg=<url> redirect
// wrappers to the target URL.
func unwrapDDGURL(href string) string {
	u, err := url.Parse(href)
	if err != nil {
		return href
	}
	if q := u.Query().Get("uddg"); q != "" {
		return q
	}
	return href
}
