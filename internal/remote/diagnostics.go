package remote

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/tailscale/tailcat"
	"retty/internal/rex"
)

type ConnectionStage string

const (
	StageLink    ConnectionStage = "解析连接码"
	StageNetwork ConnectionStage = "准备系统网络"
	// StageRelay is the relayed meow handshake, after which the desktop has
	// added this device as a peer; StageTunnel is the WireGuard and TCP dial
	// that follows. A new tunnel times them apart.
	StageRelay    ConnectionStage = "中继握手"
	StageTunnel   ConnectionStage = "建立加密隧道"
	StageHello    ConnectionStage = "等待桌面响应"
	StageProtocol ConnectionStage = "检查协议版本"
	StageSessions ConnectionStage = "读取会话列表"
)

type DiagnosticReport struct {
	Started time.Time
	Result  string
	Stage   ConnectionStage
	Text    string
}

// Diagnostics collects only approved metadata and error codes, never raw error
// strings, keys, links, device tokens, session IDs or terminal content.
// Finish freezes it before tunnel cleanup or subsequent session-stream dials.
type Diagnostics struct {
	mu             sync.Mutex
	started        time.Time
	mode           string
	attempt        int
	budget         time.Duration
	relay, desktop string
	protocol       int
	protocolRead   bool
	steps          []string
	events         []string
	failed         ConnectionStage
	finished       bool
}

func NewDiagnostics(recovering bool, attempt int, budget time.Duration) *Diagnostics {
	mode := "连接桌面"
	if recovering {
		mode = "恢复连接"
	}
	return &Diagnostics{started: time.Now(), mode: mode, attempt: max(attempt, 1), budget: budget}
}

// NewPreviewDiagnostics times a home-page availability probe. It builds the
// tunnel that a later tap reuses, so on a poor network its tunnel stages are
// where the time goes.
func NewPreviewDiagnostics(budget time.Duration) *Diagnostics {
	return &Diagnostics{started: time.Now(), mode: "首页探测", attempt: 1, budget: budget}
}

// Measure also annotates failures when diagnostics are disabled. A caller can
// still show a useful stage without ever exposing the underlying error text.
func (d *Diagnostics) Measure(stage ConnectionStage, fn func() error) error {
	started := time.Now()
	err := fn()
	if d != nil {
		d.mu.Lock()
		if !d.finished {
			status := "完成"
			if err != nil {
				status = "失败 / " + diagnosticCause(err)
				d.failed = stage
			}
			if len(d.steps) < 8 {
				d.steps = append(d.steps, fmt.Sprintf("%s：%s（%d ms）", stage, status, time.Since(started).Milliseconds()))
			}
		}
		d.mu.Unlock()
	}
	if err != nil {
		return &ConnectionError{Kind: Failure(err), Cause: err, Stage: stage}
	}
	return nil
}

func (d *Diagnostics) target(addr tailcat.Addr) {
	if d == nil {
		return
	}
	ci, err := tailcat.ParseAddr(addr)
	if err != nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	fingerprint := sha256.Sum256(ci.ServerPublic.AppendTo(nil))
	d.desktop = fmt.Sprintf("%x", fingerprint[:6])
	// The URL is used only for its public hostname. Never copy credentials,
	// path or query from an untrusted link into the report.
	if u, err := url.Parse(derpURL(addr)); err == nil {
		host := u.Hostname()
		if len(host) <= 253 {
			valid := host != ""
			for _, label := range strings.Split(host, ".") {
				if len(label) == 0 || len(label) > 63 {
					valid = false
				}
			}
			for _, c := range host {
				if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '-') {
					valid = false
				}
			}
			if valid {
				d.relay = host
			}
		}
	}
}

func (d *Diagnostics) ProtocolVersion(version int) {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.finished {
		d.protocol = version
		d.protocolRead = true
	}
}

// TransportLog recognizes a small set of upstream formats. The original
// formatted message is never retained, including with RETTY_DEBUG_TUNNEL.
func (d *Diagnostics) TransportLog(format string, args ...any) string {
	event := ""
	switch {
	case strings.HasPrefix(format, "magicsock: derp-%d connected;"):
		event = "中继已连接"
	case strings.Contains(format, "connecting to derp-%d"):
		event = "开始连接中继"
	case strings.HasPrefix(format, "magicsock:") && strings.Contains(format, "derp.Recv(derp-%d)"):
		event = "中继连接异常"
		for _, arg := range args {
			if err, ok := arg.(error); ok {
				event += " / " + diagnosticCause(err)
				break
			}
		}
	}
	if event == "" || d == nil {
		return event
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.finished {
		if len(d.events) == 12 {
			d.events = d.events[1:]
		}
		d.events = append(d.events, fmt.Sprintf("+%d ms %s", time.Since(d.started).Milliseconds(), event))
	}
	return event
}

func (d *Diagnostics) Finish(err error) DiagnosticReport {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.finished = true
	result := "连接成功"
	if err != nil {
		result = (&ConnectionError{Kind: Failure(err)}).Error()
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Retty 连接诊断 v1\n时间：%s\n平台：%s/%s\n模式：%s / 第 %d 次\n总耗时：%d ms / 限时 %d ms\n结果：%s", d.started.Format(time.RFC3339Nano), runtime.GOOS, runtime.GOARCH, d.mode, d.attempt, time.Since(d.started).Milliseconds(), d.budget.Milliseconds(), result)
	if d.failed != "" {
		fmt.Fprintf(&b, "\n失败阶段：%s", d.failed)
	}
	if err != nil {
		fmt.Fprintf(&b, "\n原因代码：%s", diagnosticCause(err))
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			if setting.Key == "vcs.revision" {
				fmt.Fprintf(&b, "\n构建：%.12s", setting.Value)
			}
			if setting.Key == "vcs.modified" {
				fmt.Fprintf(&b, "\n含本地修改：%s", setting.Value)
			}
		}
	}
	if d.desktop != "" {
		fmt.Fprintf(&b, "\n桌面指纹：%s", d.desktop)
	}
	if d.relay != "" {
		fmt.Fprintf(&b, "\n中继：%s", d.relay)
	}
	fmt.Fprintf(&b, "\n隧道限时：最多 15000 ms（受总限时约束）\n控制请求限时：最多 15000 ms（受总限时约束）\n协议：客户端 %d–%d / 桌面 ", rex.MinProtocolVersion, rex.ProtocolVersion)
	if d.protocolRead {
		fmt.Fprint(&b, d.protocol)
	} else {
		b.WriteString("未读取")
	}
	if runtime.GOOS == "ios" {
		b.WriteString("\n中继路由：系统 DNS（域名）")
		b.WriteString("\n系统网络准备可能复用缓存，不代表本次中继探测")
	}
	b.WriteString("\n阶段：\n" + strings.Join(d.steps, "\n"))
	if len(d.events) > 0 {
		b.WriteString("\n隧道事件：\n" + strings.Join(d.events, "\n"))
	}
	return DiagnosticReport{Started: d.started, Result: result, Stage: d.failed, Text: b.String()}
}

func diagnosticCause(err error) string {
	if err == nil {
		return "ok"
	}
	if cause := systemNetworkCause(err); cause != "" {
		return cause
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "deadline_exceeded"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		if dns.IsTimeout {
			return "dns_timeout"
		}
		if dns.IsNotFound {
			return "dns_not_found"
		}
		return "dns_failed"
	}
	var cert *tls.CertificateVerificationError
	var unknown x509.UnknownAuthorityError
	var hostname x509.HostnameError
	if errors.As(err, &cert) || errors.As(err, &unknown) || errors.As(err, &hostname) {
		return "tls_certificate_invalid"
	}
	var network net.Error
	if errors.As(err, &network) && network.Timeout() {
		return "network_timeout"
	}
	for _, v := range []struct {
		err  error
		code string
	}{
		{syscall.ENETUNREACH, "network_unreachable"}, {syscall.ENETDOWN, "network_down"},
		{syscall.ECONNREFUSED, "connection_refused"}, {syscall.ECONNRESET, "connection_reset"},
		{syscall.EACCES, "permission_denied"}, {syscall.EHOSTUNREACH, "host_unreachable"},
		{io.EOF, "connection_closed"}, {net.ErrClosed, "connection_closed"},
	} {
		if errors.Is(err, v.err) {
			return v.code
		}
	}
	var failure *ConnectionError
	if errors.As(err, &failure) {
		switch failure.Kind {
		case InvalidLink:
			return "invalid_link"
		case ProtocolMismatch:
			return "protocol_mismatch"
		case ConnectionTimeout:
			return "network_timeout"
		}
	}
	return "unclassified_error"
}
