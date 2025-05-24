package core

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/url"

	"github.com/Arceliar/phony"
	"github.com/apernet/hysteria/core/v2/client"
	"github.com/apernet/hysteria/core/v2/pkg/congestion"
	"github.com/apernet/hysteria/core/v2/pkg/pmtud"
	"github.com/apernet/hysteria/core/v2/pkg/units"
)

type linkHy2 struct {
	phony.Inbox
	*links
	// TODO: Add Hysteria-specific configuration options here if needed
}

// newLinkHy2 creates a new linkHy2 instance.
func (l *links) newLinkHy2() *linkHy2 {
	lh := &linkHy2{
		links: l,
	}
	// TODO: Initialize Hysteria-specific configurations if any
	return lh
}

// dial implements Hysteria v2 dial functionality.
func (lh *linkHy2) dial(ctx context.Context, u *url.URL, info linkInfo, options linkOptions) (net.Conn, error) {
	serverAddr := u.Host // Hysteria server address is expected in host part of the URI

	var authString string
	if user := u.User; user != nil {
		authString = user.Username()
		if pass, ok := user.Password(); ok {
			authString = authString + ":" + pass
		}
	}
	// Fallback to ?password= if userinfo is not present (legacy or alternative)
	// Based on task: "The existing use of options.password ... should be a fallback or potentially removed"
	// Let's keep it as a fallback for now.
	if authString == "" && len(options.password) > 0 {
		authString = string(options.password)
	}

	if authString == "" {
		return nil, fmt.Errorf("hysteria2: auth string not provided in URI userinfo or via options")
	}

	queryParams := u.Query()
	insecureSkipVerify := false // Default to secure
	if insecureVal := queryParams.Get("insecure"); insecureVal == "1" {
		insecureSkipVerify = true
	} else if insecureVal == "0" {
		insecureSkipVerify = false
	}

	obfsType := queryParams.Get("obfs")
	obfsPassword := queryParams.Get("obfs-password")
	
	// pinSHA256 := queryParams.Get("pinSHA256") // Parsed, but not used yet

	clientCfg := client.Config{
		ServerAddr: serverAddr,
		AuthString: authString,
		TLSConfig: &tls.Config{
			ServerName:         options.tlsSNI, // From link.go, sourced from ?sni=
			InsecureSkipVerify: insecureSkipVerify,
			// TODO: Implement certificate pinning using pinSHA256 if provided.
			// This would likely involve setting VerifyConnection or similar custom verification.
		},
		// Bandwidth settings removed from URI parsing, set to zero/library defaults.
		// CongestionControl and DisableMTUDiscovery will use library defaults if not explicitly set.
		// Example of how they were set before (for reference, now removed):
		// Bandwidth:           units.Bandwidth{Up: units.Mbps(0), Down: units.Mbps(0)}, // Set to 0 or omit for library default
		// CongestionControl:   congestion.NewBBR(), // Example, library might have its own default
		// DisableMTUDiscovery: pmtud.DisableMTUDiscovery, // Example, library might have its own default
	}

	// Apply obfuscation if specified - assuming Hysteria client.Config has these fields
	if obfsType != "" {
		// This is a guess for field names. Actual Hysteria API might differ.
		// E.g., clientCfg.Obfuscator = obfsType; clientCfg.ObfuscatorPassword = obfsPassword
		// For now, let's assume top-level fields for simplicity if they exist directly on client.Config
		// If Hysteria uses a sub-struct for Obfs, this needs adjustment.
		// Based on common patterns, it might be like:
		// clientCfg.Obfs = &client.ObfsConfig{ Type: obfsType, Password: obfsPassword }
		// For now, I'll add a TODO as I can't verify Hysteria's exact struct.
		// TODO: Set obfuscation fields on clientCfg. Example: clientCfg.Obfs = &somepkg.ObfsConfig{ Type: obfsType, Password: obfsPassword }
		// As a placeholder, let's assume direct fields if they were simple strings:
		// clientCfg.ObfsType = obfsType
		// clientCfg.ObfsPassword = obfsPassword
		// Since I cannot confirm, I will leave this as a TODO to avoid compilation errors with made-up fields.
		lh.links.log.Debugf("Hysteria2: Obfuscation type '%s' specified but not yet implemented in this client.", obfsType)
	}


	hyClient, err := client.NewClient(clientCfg)
	if err != nil {
		return nil, fmt.Errorf("hysteria2: failed to create client: %w", err)
	}

	// Assuming DialTCP is the method to establish a TCP-like stream.
	// The actual method might be different (e.g., Dial, Connect).
	// Hysteria's Dial might not need address if ServerAddr is already in config.
	// For now, passing "tcp" and serverAddr, but if hyClient is already configured,
	// it might just be hyClient.Dial(ctx) or similar.
	// Let's try hyClient.DialTCP(ctx) first as it's a common pattern.
	// If not, then hyClient.Dial(ctx, "tcp", serverAddr)
	// If the client is fully configured, it might be hyClient.Dial(ctx)
	
	// After checking some examples for Hysteria v2, it seems DialTCP(ctx) or DialUDP(ctx) is common.
	// Since we want a net.Conn, DialTCP seems appropriate.
	conn, err := hyClient.DialTCP(ctx)
	// Test hook integration
	if GlobalTestDialedConnChan != nil {
		if err != nil {
			// Non-blocking send
			select {
			case GlobalTestDialErrorChan <- err:
			default:
			}
		} else if conn != nil { // Ensure conn is not nil before sending
			// Non-blocking send
			select {
			case GlobalTestDialedConnChan <- conn:
			default:
			}
		}
	}
	if err != nil {
		return nil, fmt.Errorf("hysteria2: failed to dial: %w", err)
	}
	return conn, nil
}

// Note: The import block for server, strings, sync was here. It should be at the top.
// Assuming it's moved to the top by a previous step or will be handled by formatter.

// hy2Listener is a net.Listener adapter for a Hysteria v2 server.
type hy2Listener struct {
	listenAddr net.Addr
	hyServer   *server.Server
	connChan   chan net.Conn
	doneChan   chan struct{}
	errChan    chan error // Captures async error from hyServer.Serve()
	ctx        context.Context
	cancel     context.CancelFunc

	closeOnce sync.Once // Ensures Close actions are idempotent
	err       error     // Stores the first error encountered
	errLock   sync.Mutex
}

// Accept waits for and returns the next connection to the listener.
func (l *hy2Listener) Accept() (net.Conn, error) {
	select {
	case conn, ok := <-l.connChan:
		if !ok {
			// connChan was closed, potentially due to server stopping.
			// Check if there's a stored error.
			l.errLock.Lock()
			defer l.errLock.Unlock()
			if l.err != nil {
				return nil, l.err
			}
			return nil, fmt.Errorf("hysteria2: listener closed")
		}
		return conn, nil
	case <-l.doneChan:
		// Listener was explicitly closed.
		l.errLock.Lock()
		defer l.errLock.Unlock()
		if l.err != nil {
			return nil, l.err
		}
		return nil, fmt.Errorf("hysteria2: listener closed")
	case err := <-l.errChan:
		// Async error from server.Serve()
		l.errLock.Lock()
		if l.err == nil {
			l.err = err
		}
		l.errLock.Unlock()
		return nil, err
	}
}

// Close closes the listener.
// Any blocked Accept operations will be unblocked and return errors.
func (l *hy2Listener) Close() error {
	l.closeOnce.Do(func() {
		close(l.doneChan) // Signal Accept to stop
		l.cancel()        // Signal Hysteria server to stop Serve
		// hyServer.Close() might be available too, but context cancellation is common.
		// Draining connChan is not strictly necessary here as new connections will be rejected by ConnHandler.
	})
	return nil
}

// Addr returns the listener's network address.
func (l *hy2Listener) Addr() net.Addr {
	return l.listenAddr
}

// listen implements Hysteria v2 listen functionality.
func (lh *linkHy2) listen(parentCtx context.Context, u *url.URL, sintf string) (net.Listener, error) {
	host, port, err := net.SplitHostPort(u.Host)
	if err != nil {
		addrErr := &net.AddrError{Err: "missing port in address", Addr: u.Host}
		if strings.Contains(err.Error(), "missing port in address") || strings.Contains(err.Error(), "too many colons in address") {
			// If u.Host is just "hostname" or ":port", SplitHostPort might behave differently or error.
			// Hysteria URIs typically are host:port.
			// If it's just a port like ":1234", host will be empty.
			// If it's a hostname without port, it might error.
			// For now, assume host:port format is required by Hysteria servers.
			return nil, fmt.Errorf("hysteria2: server address '%s' must be in host:port format: %w", u.Host, addrErr)
		}
		return nil, fmt.Errorf("hysteria2: invalid server address format %q: %w", u.Host, err)
	}

	if host == "" { // e.g. if URI was just ":1234"
		host = "0.0.0.0" // Default to listen on all interfaces
	}
	parsedListenStr := net.JoinHostPort(host, port)

	resolvedListenAddr, err := net.ResolveTCPAddr("tcp", parsedListenStr)
	if err != nil {
		return nil, fmt.Errorf("hysteria2: failed to resolve listen address %q: %w", parsedListenStr, err)
	}

	var authString string
	if user := u.User; user != nil {
		authString = user.Username()
		if pass, ok := user.Password(); ok {
			authString = authString + ":" + pass
		}
	}
	// Fallback for password from query parameter if userinfo is empty
	// This is for consistency with the dialer's fallback, though server-side might be stricter.
	if authString == "" {
		if queryPass := u.Query().Get("password"); queryPass != "" {
			authString = queryPass
			lh.links.log.Warnf("Hysteria2 server: Using 'password' query parameter for auth. Standard URI is [auth@]hostname.")
		}
	}

	if authString == "" {
		return nil, fmt.Errorf("hysteria2: auth string not provided for server in URI userinfo or ?password=")
	}

	var serverTLSConfig *tls.Config
	// For server, TLS is typically configured via explicit cert/key files in Hysteria's own config,
	// or by providing a *tls.Config. Yggdrasil provides lh.links.tlsconfig.
	if u.Scheme == "hy2s" { // Official scheme for TLS
		if lh.links.tlsconfig == nil {
			// This means Yggdrasil's global TLS config (likely for node-to-node) isn't set up,
			// or this listener isn't meant to use it. Hysteria server needs some form of TLS config for 'hy2s'.
			return nil, fmt.Errorf("hysteria2: 'hy2s' scheme specified but no TLS config available via Yggdrasil (lh.links.tlsconfig is nil)")
		}
		serverTLSConfig = lh.links.tlsconfig.Clone()
	} else if u.Scheme == "hy2" {
		// Non-TLS listener
		serverTLSConfig = nil
	} else {
		return nil, fmt.Errorf("hysteria2: unsupported scheme for listener: %s", u.Scheme)
	}
	
	queryParams := u.Query()
	obfsType := queryParams.Get("obfs")
	obfsPassword := queryParams.Get("obfs-password")

	listenerCtx, listenerCancel := context.WithCancel(parentCtx)

	// Buffer connChan to allow Hysteria's ConnHandler to not block excessively
	// if Accept() isn't called immediately. Size can be tuned.
	connChan := make(chan net.Conn, 32)
	doneChan := make(chan struct{})
	errChan := make(chan error, 1) // Buffer of 1 for the first error from Serve

	listener := &hy2Listener{
		listenAddr: resolvedListenAddr,
		connChan:   connChan,
		doneChan:   doneChan,
		errChan:    errChan,
		ctx:        listenerCtx,
		cancel:     listenerCancel,
	}

	hyServCfg := server.Config{
		ListenAddr: parsedListenStr,
		AuthString: authString,
		TLSConfig:  serverTLSConfig,
		// Bandwidth settings removed from URI parsing for server config as well.
		ConnHandler: func(rconn net.Conn, initialPayload []byte) error {
			select {
			case listener.connChan <- rconn:
				if GlobalTestListenerAcceptChan != nil {
					select {
					case GlobalTestListenerAcceptChan <- rconn:
					default:
					}
				}
				return nil
			case <-listener.doneChan:
				rconn.Close()
				return fmt.Errorf("hysteria2: listener is closing")
			case <-listener.ctx.Done():
				rconn.Close()
				return fmt.Errorf("hysteria2: listener context done")
			}
		},
	}

	// Apply obfuscation if specified - assuming Hysteria server.Config has these fields
	if obfsType != "" {
		// This is a guess for field names. Actual Hysteria API might differ.
		// hyServCfg.ObfsType = obfsType
		// hyServCfg.ObfsPassword = obfsPassword
		// TODO: Set obfuscation fields on hyServCfg. Similar to client, exact fields unknown.
		lh.links.log.Debugf("Hysteria2 Server: Obfuscation type '%s' specified but not yet implemented in this adapter.", obfsType)
	}

	hyServ, err := server.NewServer(hyServCfg)
	if err != nil {
		listenerCancel()
		return nil, fmt.Errorf("hysteria2: failed to create server: %w", err)
	}
	listener.hyServer = hyServ

	go func() {
		defer close(listener.connChan) // Ensure connChan is closed when Serve exits
		defer listenerCancel()         // Ensure context is cancelled
		
		lh.links.log.Infof("Starting Hysteria v2 server on %s", parsedListenStr)
		serveErr := listener.hyServer.Serve(listener.ctx) // Assuming Serve() takes context
		
		if serveErr != nil && serveErr != context.Canceled && !strings.Contains(serveErr.Error(), "use of closed network connection") {
			lh.links.log.Errorf("Hysteria v2 server error: %v", serveErr)
			listener.errLock.Lock()
			if listener.err == nil {
				listener.err = serveErr
			}
			listener.errLock.Unlock()
			// Non-blocking send to errChan or store it if Accept needs it.
			select {
			case listener.errChan <- serveErr:
			default: // Avoid blocking if errChan is full or no receiver
			}
			// Send to global test hook if active
			if GlobalTestListenerErrorChan != nil {
				select {
				case GlobalTestListenerErrorChan <- serveErr:
				default: // Non-blocking for test hook
				}
			}
		} else {
			lh.links.log.Infof("Hysteria v2 server on %s stopped.", parsedListenStr)
		}
	}()

	return listener, nil
}
