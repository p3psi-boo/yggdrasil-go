package core

import "net"

// GlobalTestDialedConnChan is used in tests to get the net.Conn from a dial operation.
var GlobalTestDialedConnChan chan net.Conn

// GlobalTestDialErrorChan is used in tests to get an error from a dial operation.
var GlobalTestDialErrorChan chan error

// GlobalTestListenerAcceptChan is used in tests to get the net.Conn from a listener accept operation.
var GlobalTestListenerAcceptChan chan net.Conn

// GlobalTestListenerErrorChan is used in tests to get an error from a listener accept operation.
var GlobalTestListenerErrorChan chan error

// InitTestHooks initializes the test channels. Call this in test setup.
// It's crucial that these are buffered channels (size 1 is often sufficient for simple cases)
// to prevent the main code from blocking if the test isn't immediately ready to receive.
func InitTestHooks() {
	GlobalTestDialedConnChan = make(chan net.Conn, 1)
	GlobalTestDialErrorChan = make(chan error, 1)
	GlobalTestListenerAcceptChan = make(chan net.Conn, 1)
	GlobalTestListenerErrorChan = make(chan error, 1)
}

// ClearTestHooks nils out the test channels. Call this in test teardown.
func ClearTestHooks() {
	GlobalTestDialedConnChan = nil
	GlobalTestDialErrorChan = nil
	GlobalTestListenerAcceptChan = nil
	GlobalTestListenerErrorChan = nil
}
