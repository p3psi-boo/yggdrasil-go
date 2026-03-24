package core

import (
	"context"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const defaultFakeHTTPTTL = 3

type fakeHTTPRequest struct {
	peerURI         string
	targetHost      string
	targetIP        net.IP
	targetPort      int
	fakeHost        string
	sourceInterface string
	ttl             int
}

type fakeHTTPInjector interface {
	Inject(ctx context.Context, req fakeHTTPRequest) error
}

// parseFakeHTTPHost validates and normalizes the fakehttp host query value.
func parseFakeHTTPHost(raw string) (string, bool) {
	host := strings.ToLower(strings.TrimSpace(raw))
	switch {
	case host == "":
		return "", false
	case len(host) > 253:
		return "", false
	case strings.ContainsAny(host, "/\\?@"):
		return "", false
	case strings.HasPrefix(host, "."):
		return "", false
	case strings.HasSuffix(host, "."):
		return "", false
	case strings.Contains(host, ".."):
		return "", false
	case net.ParseIP(host) != nil:
		return "", false
	}

	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 {
			return "", false
		}
		if label[0] == '-' || label[len(label)-1] == '-' {
			return "", false
		}
		for _, ch := range label {
			switch {
			case ch >= 'a' && ch <= 'z':
			case ch >= '0' && ch <= '9':
			case ch == '-':
			default:
				return "", false
			}
		}
	}

	return host, true
}

func fakeHTTPSupportsScheme(scheme string) bool {
	switch strings.ToLower(scheme) {
	case "tcp", "tls", "ws", "wss", "socks", "sockstls":
		return true
	default:
		return false
	}
}

func (l *links) maybeInjectFakeHTTP(ctx context.Context, u *url.URL, info linkInfo, options linkOptions) {
	if options.fakeHTTPHost == "" {
		return
	}
	if !fakeHTTPSupportsScheme(u.Scheme) {
		l.warnFakeHTTPOnce("unsupported|"+u.Scheme, "Ignoring fakehttp query on non-TCP scheme %q", u.Redacted())
		return
	}
	hostname, port, ips, err := l.resolveFakeHTTPDestination(u.Host)
	if err != nil {
		l.core.log.Warnf("fakehttp injection skipped for peer %q: %s", u.Redacted(), err)
		return
	}
	raw := l.fakeHTTPRawInjector
	if raw == nil {
		raw = newFakeHTTPRawInjector()
		l.fakeHTTPRawInjector = raw
	}
	fallback := l.fakeHTTPFallbackInjector
	if fallback == nil {
		fallback = newFakeHTTPFallbackInjector(l)
		l.fakeHTTPFallbackInjector = fallback
	}

	var lastErr error
	for _, ip := range ips {
		req := fakeHTTPRequest{
			peerURI:         u.Redacted(),
			targetHost:      hostname,
			targetIP:        ip,
			targetPort:      port,
			fakeHost:        options.fakeHTTPHost,
			sourceInterface: info.sintf,
			ttl:             defaultFakeHTTPTTL,
		}
		if err := raw.Inject(ctx, req); err == nil {
			l.core.log.Debugf("fakehttp raw injection succeeded for peer %q", u.Redacted())
			return
		} else {
			lastErr = err
			l.core.log.Warnf("fakehttp raw injection failed for peer %q: %s", u.Redacted(), err)
		}
		if err := fallback.Inject(ctx, req); err == nil {
			l.core.log.Debugf("fakehttp fallback injection succeeded for peer %q", u.Redacted())
			return
		} else {
			lastErr = err
			l.core.log.Warnf("fakehttp fallback injection failed for peer %q: %s", u.Redacted(), err)
		}
	}
	if lastErr != nil {
		l.core.log.Warnf("fakehttp injection failed for peer %q: %s", u.Redacted(), lastErr)
	}
}

func (l *links) warnFakeHTTPOnce(key, format string, args ...interface{}) {
	if key == "" {
		l.core.log.Warnf(format, args...)
		return
	}
	if _, loaded := l.fakeHTTPWarned.LoadOrStore(key, struct{}{}); loaded {
		return
	}
	l.core.log.Warnf(format, args...)
}

func (l *links) resolveFakeHTTPDestination(hostport string) (hostname string, port int, ips []net.IP, err error) {
	host, p, err := net.SplitHostPort(hostport)
	if err != nil {
		return "", 0, nil, err
	}
	port, err = strconv.Atoi(p)
	if err != nil {
		return "", 0, nil, err
	}
	resp, err := net.LookupIP(host)
	if err != nil {
		return "", 0, nil, err
	}
	for _, ip := range resp {
		switch {
		case ip.IsUnspecified():
			continue
		case ip.IsMulticast():
			continue
		case ip.IsLinkLocalMulticast():
			continue
		case ip.IsInterfaceLocalMulticast():
			continue
		case l.core != nil && l.core.config.peerFilter != nil && !l.core.config.peerFilter(ip):
			continue
		}
		ips = append(ips, ip)
	}
	if len(ips) == 0 {
		return "", 0, nil, ErrLinkNoSuitableIPs
	}
	return host, port, ips, nil
}

type fallbackFakeHTTPInjector struct {
	links *links
}

func newFakeHTTPFallbackInjector(l *links) fakeHTTPInjector {
	return &fallbackFakeHTTPInjector{links: l}
}

func (i *fallbackFakeHTTPInjector) Inject(ctx context.Context, req fakeHTTPRequest) error {
	injectCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	dialer := &net.Dialer{}
	if i.links != nil && i.links.tcp != nil {
		tcpDialer, err := i.links.tcp.dialerFor(&net.TCPAddr{IP: req.targetIP, Port: req.targetPort}, req.sourceInterface)
		if err != nil {
			return err
		}
		dialer = tcpDialer
	}
	conn, err := dialer.DialContext(injectCtx, "tcp", net.JoinHostPort(req.targetIP.String(), strconv.Itoa(req.targetPort)))
	if err != nil {
		return err
	}
	defer conn.Close()
	if err = conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		return err
	}
	if _, err = conn.Write([]byte(buildFakeHTTPRequest(req.fakeHost))); err != nil {
		return err
	}
	return nil
}

func buildFakeHTTPRequest(host string) string {
	return "GET / HTTP/1.1\r\n" +
		"Host: " + host + "\r\n" +
		"Connection: close\r\n" +
		"\r\n"
}
