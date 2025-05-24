package core_test

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings" // Import strings
	"sync"
	"testing"
	"time"

	"github.com/yggdrasil-network/yggdrasil-go/src/config"
	"github.com/yggdrasil-network/yggdrasil-go/src/core"
	"github.com/yggdrasil-network/yggdrasil-go/src/logging"
)

// NOTE TO REVIEWER:
// This test relies on test hooks defined in `src/core/test_hooks.go` and used in `src/core/link_hy2.go`.
// Hooks:
// - core.GlobalTestDialedConnChan
// - core.GlobalTestDialErrorChan
// - core.GlobalTestListenerAcceptChan
// - core.GlobalTestListenerErrorChan
// Ensure `core.InitTestHooks()` and `core.ClearTestHooks()` are called.

func TestHy2LinkSimple(t *testing.T) {
	// Setup logger
	logCfg := &logging.LogConfig{LogType: "testing", LogLevel: "debug"}
	logging.Init(logCfg)
	log := logging.Get()

	// Initialize test hooks
	core.InitTestHooks()
	defer core.ClearTestHooks()

	// Server Core
	sCfg := config.GenerateConfig()
	sCfg.AdminListen = "none"
	sCfg.IfName = "none"
	sCfg.NewPrivateKey()

	sCore := &core.Core{}
	if err := sCore.Init(sCfg); err != nil {
		t.Fatalf("Server Core Init failed: %v", err)
	}
	sCore.Start()
	defer sCore.Stop()

	// Listener Setup
	listenAuth := "testpassword123" // Password only, no username
	listenURIStr := fmt.Sprintf("hy2://%s@127.0.0.1:0", listenAuth) // Port 0 for OS to pick
	listenURL, err := url.Parse(listenURIStr)
	if err != nil {
		t.Fatalf("Failed to parse listen URI: %v", err)
	}

	listener, err := sCore.GetLinks().Listen(listenURL, "", false)
	if err != nil {
		t.Fatalf("links.Listen failed: %v", err)
	}
	defer listener.Cancel()

	actualListenAddr := listener.Addr().String()
	log.Infof("Hysteria server listening on %s", actualListenAddr)

	var wg sync.WaitGroup
	wg.Add(1)

	// Server Goroutine
	go func() {
		defer wg.Done()
		var srvConn net.Conn
		var err error

		select {
		case srvConn = <-core.GlobalTestListenerAcceptChan:
			log.Infof("Server: Accepted connection via test hook from %s", srvConn.RemoteAddr())
		case err = <-core.GlobalTestListenerErrorChan:
			t.Errorf("Server: Error from listener error hook: %v", err)
			return
		case <-time.After(10 * time.Second):
			t.Errorf("Server: Timeout waiting for connection via listener accept hook")
			if listener.Ctx().Err() != nil {
				t.Logf("Server: Listener context was done: %v", listener.Ctx().Err())
			}
			return
		}
		defer srvConn.Close()

		buf := make([]byte, 1024)
		n, err := srvConn.Read(buf)
		if err != nil {
			if err == io.EOF || strings.Contains(err.Error(), "use of closed network connection") || strings.Contains(err.Error(), "connection reset by peer") {
				log.Warnf("Server: Read EOF or connection closed by peer: %v", err)
				return
			}
			t.Errorf("Server: Read failed: %v", err)
			return
		}
		receivedMsg := string(buf[:n])
		log.Infof("Server: Received '%s'", receivedMsg)
		if receivedMsg != "ping" {
			t.Errorf("Server: expected 'ping', got '%s'", receivedMsg)
			return
		}

		_, err = srvConn.Write([]byte("pong"))
		if err != nil {
			t.Errorf("Server: Write failed: %v", err)
			return
		}
		log.Infof("Server: Sent 'pong'")
	}()

	// Client Core
	cCfg := config.GenerateConfig()
	cCfg.AdminListen = "none"
	cCfg.IfName = "none"
	cCfg.NewPrivateKey()

	cCore := &core.Core{}
	if err := cCore.Init(cCfg); err != nil {
		t.Fatalf("Client Core Init failed: %v", err)
	}
	cCore.Start()
	defer cCore.Stop()

	dialAuth := listenAuth // Use the same auth string
	dialURIStr := fmt.Sprintf("hy2://%s@%s", dialAuth, actualListenAddr)
	dialURL, err := url.Parse(dialURIStr)
	if err != nil {
		t.Fatalf("Failed to parse dial URI: %v", err)
	}

	if err := cCore.GetLinks().Add(dialURL, "", core.LinkTypePersistent); err != nil {
		t.Fatalf("links.Add failed for client: %v", err)
	}

	var clientConn net.Conn
	select {
	case clientConn = <-core.GlobalTestDialedConnChan:
		log.Infof("Client: Successfully dialed and received connection via test hook.")
	case err := <-core.GlobalTestDialErrorChan:
		t.Fatalf("Client: Dial failed via test hook: %v", err)
	case <-time.After(15 * time.Second):
		t.Fatalf("Client: Timeout waiting for connection via dial test hook.")
	}
	defer clientConn.Close()

	log.Infof("Client: Sending 'ping'")
	_, err = clientConn.Write([]byte("ping"))
	if err != nil {
		t.Fatalf("Client: Write 'ping' failed: %v", err)
	}

	buf := make([]byte, 1024)
	n, err := clientConn.Read(buf)
	if err != nil {
		t.Fatalf("Client: Read 'pong' failed: %v", err)
	}
	receivedMsg := string(buf[:n])
	log.Infof("Client: Received '%s'", receivedMsg)
	if receivedMsg != "pong" {
		t.Fatalf("Client: expected 'pong', got '%s'", receivedMsg)
	}

	log.Info("Client: Data exchange successful")
	if err := clientConn.Close(); err != nil { // Close earlier to signal server
		t.Logf("Client: error closing client connection: %v", err)
	}
	wg.Wait()
	log.Info("TestHy2LinkSimple completed")
}

func TestHy2LinkAdvancedParams(t *testing.T) {
	logCfg := &logging.LogConfig{LogType: "testing", LogLevel: "debug"}
	logging.Init(logCfg)
	log := logging.Get()

	core.InitTestHooks()
	defer core.ClearTestHooks()

	sCfg := config.GenerateConfig()
	sCfg.AdminListen = "none"
	sCfg.IfName = "none"
	sCfg.NewPrivateKey()
	sCore := &core.Core{}
	if err := sCore.Init(sCfg); err != nil {
		t.Fatalf("Server Core Init failed: %v", err)
	}
	sCore.Start()
	defer sCore.Stop()

	serverAuth := "user123:securepass456"
	// Listen with hy2s (TLS). Yggdrasil core generates a self-signed cert by default.
	listenURIStr := fmt.Sprintf("hy2s://%s@127.0.0.1:0", serverAuth)
	listenURL, err := url.Parse(listenURIStr)
	if err != nil {
		t.Fatalf("Failed to parse listen URI: %v", err)
	}

	listener, err := sCore.GetLinks().Listen(listenURL, "", false)
	if err != nil {
		t.Fatalf("links.Listen failed for hy2s: %v", err)
	}
	defer listener.Cancel()
	actualListenAddr := listener.Addr().String()
	log.Infof("Hysteria (hy2s) server listening on %s", actualListenAddr)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() { // Server goroutine
		defer wg.Done()
		var srvConn net.Conn
		select {
		case srvConn = <-core.GlobalTestListenerAcceptChan:
			log.Infof("Server (Advanced): Accepted connection from %s", srvConn.RemoteAddr())
		case err := <-core.GlobalTestListenerErrorChan:
			t.Errorf("Server (Advanced): Error from listener error hook: %v", err)
			return
		case <-time.After(10 * time.Second):
			t.Errorf("Server (Advanced): Timeout waiting for connection.")
			return
		}
		defer srvConn.Close()
		// Simple echo for this test
		buf := make([]byte, 64)
		n, err := srvConn.Read(buf)
		if err != nil {
			if err != io.EOF { t.Errorf("Server (Advanced): Read error: %v", err) }
			return
		}
		if _, err = srvConn.Write(buf[:n]); err != nil {
			t.Errorf("Server (Advanced): Write error: %v", err)
		}
	}()

	cCfg := config.GenerateConfig()
	cCfg.AdminListen = "none"
	cCfg.IfName = "none"
	cCfg.NewPrivateKey()
	cCore := &core.Core{}
	if err := cCore.Init(cCfg); err != nil {
		t.Fatalf("Client Core Init failed: %v", err)
	}
	cCore.Start()
	defer cCore.Stop()

	// Client dials with insecure=1, obfs, and pinSHA256 (pin won't be verified yet)
	clientAuth := serverAuth
	dialURIStr := fmt.Sprintf("hy2s://%s@%s?insecure=1&obfs=salamander&obfs-password=dummyobfspass&pinSHA256=dummyvalue", clientAuth, actualListenAddr)
	dialURL, err := url.Parse(dialURIStr)
	if err != nil {
		t.Fatalf("Failed to parse advanced dial URI: %v", err)
	}

	if err := cCore.GetLinks().Add(dialURL, "", core.LinkTypePersistent); err != nil {
		t.Fatalf("links.Add failed for client (Advanced): %v", err)
	}

	var clientConn net.Conn
	select {
	case clientConn = <-core.GlobalTestDialedConnChan:
		log.Infof("Client (Advanced): Successfully dialed.")
	case err := <-core.GlobalTestDialErrorChan:
		// This test expects success due to insecure=1 with a self-signed cert.
		t.Fatalf("Client (Advanced): Dial failed via test hook: %v", err)
	case <-time.After(15 * time.Second):
		t.Fatalf("Client (Advanced): Timeout waiting for connection.")
	}
	defer clientConn.Close()

	// Test data exchange
	testMsg := "hello advanced"
	log.Infof("Client (Advanced): Sending '%s'", testMsg)
	if _, err = clientConn.Write([]byte(testMsg)); err != nil {
		t.Fatalf("Client (Advanced): Write failed: %v", err)
	}
	readBuf := make([]byte, len(testMsg)+10)
	n, err := clientConn.Read(readBuf)
	if err != nil {
		t.Fatalf("Client (Advanced): Read failed: %v", err)
	}
	if string(readBuf[:n]) != testMsg {
		t.Fatalf("Client (Advanced): Expected '%s', got '%s'", testMsg, string(readBuf[:n]))
	}
	log.Info("Client (Advanced): Data exchange successful.")

	if err := clientConn.Close(); err != nil {
		t.Logf("Client (Advanced): error closing client connection: %v", err)
	}
	wg.Wait()
	log.Info("TestHy2LinkAdvancedParams completed")
}
