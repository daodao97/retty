// Package devserver recognizes advertised development URLs in bounded terminal
// text. A candidate is not a claim that a process is listening on the port.
package devserver

import (
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

const MaxURLs = 8

var advertised = regexp.MustCompile(`(?i)(https?://)?(localhost|127\.[0-9]+\.[0-9]+\.[0-9]+|0\.0\.0\.0|\[::1\]|\[::\]):([0-9]+)(/[^\s<>"'` + "`" + `]*)?`)

type URL struct {
	URL   string
	Label string
	Port  int
}

func Extract(text string) []URL {
	if len(text) > 64<<10 {
		text = text[len(text)-(64<<10):]
	}
	all := advertised.FindAllStringIndex(text, -1)
	seen := make(map[string]bool)
	var out []URL
	for i := len(all) - 1; i >= 0 && len(out) < MaxURLs; i-- {
		a, b := all[i][0], all[i][1]
		if a > 0 {
			r, _ := utf8.DecodeLastRuneInString(text[:a])
			if strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_./@%-", r) {
				continue
			}
		}
		if b < len(text) && strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_.:@%-", rune(text[b])) {
			continue
		}
		raw := strings.TrimRight(text[a:b], ".,;!?)\u3002\uff0c\uff1b\uff09")
		if len(raw) > 2048 {
			continue
		}
		if !strings.Contains(raw, "://") {
			raw = "http://" + raw
		}
		u, err := url.Parse(raw)
		if err != nil || u.User != nil || strings.ContainsAny(raw, "\x00\r\n\x1b") {
			continue
		}
		port, err := strconv.Atoi(u.Port())
		if err != nil || port < 1 || port > 65535 {
			continue
		}
		host := strings.ToLower(u.Hostname())
		ip := net.ParseIP(host)
		if host != "localhost" && host != "0.0.0.0" && host != "::" && (ip == nil || !ip.IsLoopback()) {
			continue
		}
		if host == "0.0.0.0" || host == "::" {
			host = "localhost"
		}
		u.Scheme = strings.ToLower(u.Scheme)
		u.Host = net.JoinHostPort(host, strconv.Itoa(port))
		key := u.Scheme + "://" + u.Host
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, URL{URL: u.String(), Label: u.Scheme + "://" + u.Host + u.EscapedPath(), Port: port})
	}
	return out
}
