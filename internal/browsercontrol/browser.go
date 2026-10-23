// Package browsercontrol defines bounded browser operations shared by GUI/CLI.
package browsercontrol

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Request struct {
	Command   string `json:"command"`
	ID        string `json:"id,omitempty"`
	URL       string `json:"url,omitempty"`
	Beside    string `json:"beside,omitempty"`
	Host      string `json:"host,omitempty"`
	Direction string `json:"direction,omitempty"`
	Selector  string `json:"selector,omitempty"`
	Text      string `json:"text,omitempty"`
	TimeoutMS int64  `json:"timeoutMs,omitempty"`
}
type Info struct {
	ID      string `json:"id"`
	URL     string `json:"url"`
	Title   string `json:"title,omitempty"`
	Host    string `json:"host,omitempty"`
	Loading bool   `json:"loading"`
	Error   string `json:"error,omitempty"`
}
type Host struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Connected bool   `json:"connected"`
}
type Document struct {
	URL      string    `json:"url"`
	Title    string    `json:"title"`
	Text     string    `json:"text"`
	Elements []Element `json:"elements"`
}
type Element struct {
	Selector string `json:"selector"`
	Tag      string `json:"tag"`
	Text     string `json:"text"`
	Value    string `json:"value,omitempty"`
}
type Result struct {
	Browser  *Info     `json:"browser,omitempty"`
	Browsers []Info    `json:"browsers,omitempty"`
	Hosts    []Host    `json:"hosts,omitempty"`
	Document *Document `json:"document,omitempty"`
	Matched  bool      `json:"matched,omitempty"`
	PNG      []byte    `json:"png,omitempty"`
}

func (r Request) Validate() error {
	switch r.Command {
	case "list", "open", "status", "navigate", "read", "click", "type", "wait", "screenshot", "close":
	default:
		return errors.New("unknown browser command")
	}
	if len(r.ID) > 128 || len(r.Beside) > 128 || len(r.Host) > 256 || len(r.Selector) > 2048 || len(r.Text) > 65536 {
		return errors.New("browser request exceeds field limits")
	}
	if r.Command == "open" || r.Command == "navigate" {
		if _, err := NormalizeURL(r.URL); err != nil {
			return err
		}
	}
	if (r.Command == "click" || r.Command == "type") && r.Selector == "" {
		return errors.New("selector is required")
	}
	if r.TimeoutMS < 0 || r.TimeoutMS > 120000 {
		return errors.New("browser timeout must be between 0 and 120000 ms")
	}
	if r.Direction != "" && r.Direction != "right" && r.Direction != "down" {
		return errors.New("direction must be right or down")
	}
	return nil
}
func (r Request) Budget() time.Duration {
	if r.Command == "wait" {
		if r.TimeoutMS > 0 && r.TimeoutMS <= 120000 {
			return time.Duration(r.TimeoutMS)*time.Millisecond + time.Second
		}
		return 11 * time.Second
	}
	return 15 * time.Second
}

func NormalizeURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "about:blank" {
		return "about:blank", nil
	}
	if len(raw) > 8192 || strings.ContainsAny(raw, "\x00\r\n\t") {
		return "", errors.New("invalid browser URL")
	}
	if !strings.Contains(raw, "://") {
		if strings.HasPrefix(raw, "localhost:") || strings.HasPrefix(raw, "127.0.0.1:") || strings.HasPrefix(raw, "[::1]:") {
			raw = "http://" + raw
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return "", errors.New("browser URL must be http(s) or about:blank")
	}
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return "", errors.New("invalid URL port")
		}
	}
	return u.String(), nil
}

func LoopbackPort(raw string) (int, bool) {
	u, err := url.Parse(raw)
	if err != nil {
		return 0, false
	}
	host := strings.ToLower(u.Hostname())
	ip := net.ParseIP(host)
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return 0, false
	}
	port := 80
	if u.Scheme == "https" {
		port = 443
	}
	if u.Port() != "" {
		port, _ = strconv.Atoi(u.Port())
	}
	return port, port > 0 && port <= 65535
}

func RemapOrigin(raw, from, to string) string {
	u, e := url.Parse(raw)
	f, fe := url.Parse(from)
	target, te := url.Parse(to)
	if e != nil || fe != nil || te != nil || u.Scheme != f.Scheme || u.Host != f.Host {
		return raw
	}
	u.Scheme, u.Host = target.Scheme, target.Host
	return u.String()
}

func ReadScript() string {
	return `(() => {
const visible=e=>!!(e.getClientRects().length&&getComputedStyle(e).visibility!=='hidden');
const selector=e=>{if(e.id&&document.querySelectorAll('#'+CSS.escape(e.id)).length===1)return '#'+CSS.escape(e.id);let p=[];for(let n=e;n&&n.nodeType===1&&p.length<10;n=n.parentElement){let s=n.localName;const a=n.parentElement?[...n.parentElement.children].filter(x=>x.localName===s):[];if(a.length>1)s+=':nth-of-type('+(a.indexOf(n)+1)+')';p.unshift(s);if(n===document.body)break}return p.join(' > ')};
return {url:location.href,title:document.title,text:(document.body?.innerText||'').slice(0,32768),elements:[...document.querySelectorAll('a,button,input,textarea,select,[role=button],[contenteditable=true]')].filter(visible).slice(0,100).map(e=>({selector:selector(e),tag:e.localName,text:(e.getAttribute('aria-label')||e.innerText||e.placeholder||'').slice(0,200),value:e.type==='password'?'':String(e.value||'').slice(0,200)}))};
})()`
}

func (r Request) Script() (string, error) {
	if err := r.Validate(); err != nil {
		return "", err
	}
	selector, _ := json.Marshal(r.Selector)
	value, _ := json.Marshal(r.Text)
	base := fmt.Sprintf(`const all=document.querySelectorAll(%s); if(all.length!==1)throw new Error('selector must match exactly one element (found '+all.length+')'); const e=all[0]; if(!e.getClientRects().length||getComputedStyle(e).visibility==='hidden')throw new Error('element is not visible'); if(e.disabled||e.getAttribute('aria-disabled')==='true')throw new Error('element is disabled');`, selector)
	switch r.Command {
	case "read":
		return ReadScript(), nil
	case "click":
		return "(() => {" + base + `e.scrollIntoView({block:'center',inline:'nearest'});e.click();return true;})()`, nil
	case "type":
		return "(() => {" + base + fmt.Sprintf(`e.focus();const value=%s;if(e.isContentEditable){e.textContent=value;}else{const proto=e instanceof HTMLTextAreaElement?HTMLTextAreaElement.prototype:e instanceof HTMLInputElement?HTMLInputElement.prototype:e instanceof HTMLSelectElement?HTMLSelectElement.prototype:null;const setter=proto&&Object.getOwnPropertyDescriptor(proto,'value')?.set;if(!setter)throw new Error('element is not editable');if(e.readOnly)throw new Error('element is read-only');setter.call(e,value);}e.dispatchEvent(new Event('input',{bubbles:true}));e.dispatchEvent(new Event('change',{bubbles:true}));return true;})()`, value), nil
	case "wait":
		return fmt.Sprintf(`(() => {const selector=%s,text=%s;const e=selector?document.querySelector(selector):document.body;if(!e||!e.getClientRects().length||getComputedStyle(e).visibility==='hidden')return false;return (!text||(e.innerText||e.textContent||'').includes(text))&&document.readyState!=='loading';})()`, selector, value), nil
	}
	return "", errors.New("browser command has no DOM script")
}
