package core

import (
	"context"
	"errors"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yggdrasil-network/yggdrasil-go/src/config"
)

func TestParseFakeHTTPHost(t *testing.T) {
	tests := []struct {
		name  string
		input string
		ok    bool
		want  string
	}{
		{name: "valid hostname", input: "example.com", ok: true, want: "example.com"},
		{name: "trim and lower", input: "  ExAmPlE.COM ", ok: true, want: "example.com"},
		{name: "single label", input: "gateway", ok: true, want: "gateway"},
		{name: "empty", input: "", ok: false},
		{name: "ip rejected", input: "1.2.3.4", ok: false},
		{name: "path rejected", input: "example.com/path", ok: false},
		{name: "invalid rune", input: "exa_mple.com", ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseFakeHTTPHost(tt.input)
			if ok != tt.ok {
				t.Fatalf("ok mismatch: got %v want %v", ok, tt.ok)
			}
			if got != tt.want {
				t.Fatalf("host mismatch: got %q want %q", got, tt.want)
			}
		})
	}
}

func TestListenIgnoresFakeHTTPQuery(t *testing.T) {
	cfg := config.GenerateConfig()
	c, err := New(cfg.Certificate, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Stop()

	u, err := url.Parse("tcp://127.0.0.1:0?fakehttp=example.com")
	if err != nil {
		t.Fatal(err)
	}

	l, err := c.Listen(u, "")
	if err != nil {
		t.Fatal(err)
	}
	l.Cancel()
}

type mockFakeHTTPInjector struct {
	err   error
	calls int
	last  fakeHTTPRequest
}

func (m *mockFakeHTTPInjector) Inject(_ context.Context, req fakeHTTPRequest) error {
	m.calls++
	m.last = req
	return m.err
}

func TestMaybeInjectFakeHTTPUsesFallback(t *testing.T) {
	raw := &mockFakeHTTPInjector{err: errors.New("no raw")}
	fallback := &mockFakeHTTPInjector{}
	l := &links{
		core:                     &Core{log: GetLoggerWithPrefix("", false)},
		fakeHTTPRawInjector:      raw,
		fakeHTTPFallbackInjector: fallback,
	}
	u, _ := url.Parse("tcp://127.0.0.1:1234")
	l.maybeInjectFakeHTTP(context.Background(), u, linkInfo{}, linkOptions{fakeHTTPHost: "example.com"})
	if raw.calls != 1 {
		t.Fatalf("expected raw injector call, got %d", raw.calls)
	}
	if fallback.calls != 1 {
		t.Fatalf("expected fallback injector call, got %d", fallback.calls)
	}
	if fallback.last.targetPort != 1234 {
		t.Fatalf("unexpected fallback target port: %d", fallback.last.targetPort)
	}
	if fallback.last.fakeHost != "example.com" {
		t.Fatalf("unexpected fallback fake host: %q", fallback.last.fakeHost)
	}
}

func TestMaybeInjectFakeHTTPSkipsNonTCP(t *testing.T) {
	raw := &mockFakeHTTPInjector{}
	fallback := &mockFakeHTTPInjector{}
	l := &links{
		core:                     &Core{log: GetLoggerWithPrefix("", false)},
		fakeHTTPRawInjector:      raw,
		fakeHTTPFallbackInjector: fallback,
	}
	u, _ := url.Parse("quic://127.0.0.1:1234")
	l.maybeInjectFakeHTTP(context.Background(), u, linkInfo{}, linkOptions{fakeHTTPHost: "example.com"})
	if raw.calls != 0 || fallback.calls != 0 {
		t.Fatalf("expected no injectors to be called, got raw=%d fallback=%d", raw.calls, fallback.calls)
	}
}

func TestMaybeInjectFakeHTTPEveryAttempt(t *testing.T) {
	raw := &mockFakeHTTPInjector{}
	fallback := &mockFakeHTTPInjector{}
	l := &links{
		core:                     &Core{log: GetLoggerWithPrefix("", false)},
		fakeHTTPRawInjector:      raw,
		fakeHTTPFallbackInjector: fallback,
	}
	u, _ := url.Parse("tcp://127.0.0.1:1234")
	opt := linkOptions{fakeHTTPHost: "example.com"}
	l.maybeInjectFakeHTTP(context.Background(), u, linkInfo{}, opt)
	l.maybeInjectFakeHTTP(context.Background(), u, linkInfo{}, opt)
	if raw.calls != 2 {
		t.Fatalf("expected raw injector called for each attempt, got %d", raw.calls)
	}
}

func TestFallbackFakeHTTPInjectorSendsRequest(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	reqCh := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			errCh <- err
			return
		}
		defer conn.Close()
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		buf, err := io.ReadAll(conn)
		if err != nil {
			errCh <- err
			return
		}
		reqCh <- string(buf)
	}()

	tcpAddr := ln.Addr().(*net.TCPAddr)
	injector := newFakeHTTPFallbackInjector(nil)
	err = injector.Inject(context.Background(), fakeHTTPRequest{
		targetIP:   net.ParseIP("127.0.0.1"),
		targetPort: tcpAddr.Port,
		fakeHost:   "example.com",
	})
	if err != nil {
		t.Fatal(err)
	}

	select {
	case req := <-reqCh:
		if !strings.Contains(req, "GET / HTTP/1.1\r\n") {
			t.Fatalf("unexpected request line: %q", req)
		}
		if !strings.Contains(req, "Host: example.com\r\n") {
			t.Fatalf("missing host header: %q", req)
		}
	case err := <-errCh:
		t.Fatal(err)
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for request")
	}
}

func TestBuildFakeHTTPRequestTemplate(t *testing.T) {
	req := buildFakeHTTPRequest("example.com")
	if !strings.Contains(req, "GET / HTTP/1.1\r\n") {
		t.Fatalf("request line missing: %q", req)
	}
	if !strings.Contains(req, "Host: example.com\r\n") {
		t.Fatalf("host header missing: %q", req)
	}
	if !strings.HasSuffix(req, "\r\n\r\n") {
		t.Fatalf("request should end with CRLF CRLF: %q", strconv.Quote(req))
	}
}
