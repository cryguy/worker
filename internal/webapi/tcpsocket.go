package webapi

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/cryguy/worker/internal/core"
	"github.com/cryguy/worker/internal/eventloop"
)

const maxTCPSockets = 10
const maxTCPBufferSize = 1 * 1024 * 1024 // 1 MB

// tcpSocketBuffer provides a thread-safe read buffer for a TCP socket.
type tcpSocketBuffer struct {
	mu      sync.Mutex
	conn    net.Conn
	buf     []byte        // accumulated unread data
	err     error         // sticky read error (io.EOF, etc.)
	done    bool          // true once background reader exits
	hasData chan struct{} // signaled (non-blocking) when new data arrives or done
}

// readLoop reads from conn into the buffer in the background.
func (b *tcpSocketBuffer) readLoop() {
	tmp := make([]byte, 4096)
	for {
		n, err := b.conn.Read(tmp)
		b.mu.Lock()
		if n > 0 {
			if len(b.buf)+n > maxTCPBufferSize {
				b.err = fmt.Errorf("TCP: read buffer exceeded %d bytes", maxTCPBufferSize)
				b.done = true
				b.mu.Unlock()
				b.signal()
				return
			}
			b.buf = append(b.buf, tmp[:n]...)
		}
		if err != nil {
			b.err = err
			b.done = true
			b.mu.Unlock()
			b.signal()
			return
		}
		b.mu.Unlock()
		b.signal()
	}
}

// signal notifies any blocked reader that data (or EOF) is available.
func (b *tcpSocketBuffer) signal() {
	select {
	case b.hasData <- struct{}{}:
	default:
	}
}

// waitForData blocks until new data arrives or the timeout elapses.
func (b *tcpSocketBuffer) waitForData(timeout time.Duration) {
	select {
	case <-b.hasData:
	case <-time.After(timeout):
	}
}

// take returns up to maxBytes from the buffer as base64, or "" if empty.
// Returns (data, eof, error).
func (b *tcpSocketBuffer) take(maxBytes int) (string, bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if len(b.buf) == 0 {
		if b.done {
			return "", true, b.err
		}
		return "", false, nil
	}

	n := maxBytes
	if n > len(b.buf) {
		n = len(b.buf)
	}
	data := make([]byte, n)
	copy(data, b.buf[:n])
	b.buf = b.buf[n:]

	// Drain any pending signal so future signals from readLoop aren't blocked.
	select {
	case <-b.hasData:
	default:
	}

	eof := len(b.buf) == 0 && b.done
	return base64.StdEncoding.EncodeToString(data), eof, nil
}

// tcpSocketMap holds all TCP sockets for a request, stored via the
// RequestState extension map under the key "tcpSocketMap".
type tcpSocketMap struct {
	sockets  map[string]net.Conn
	buffers  map[string]*tcpSocketBuffer
	nextID   int64
}

// TcpSSRFEnabled controls SSRF protection for TCP socket connections.
var TcpSSRFEnabled = true

// tcpSocketJS is the pure JS polyfill for the connect() global and Socket class.
const tcpSocketJS = `
(function() {

globalThis.connect = function(address, options) {
	var requestID = String(globalThis.__requestID);
	var hostname, port;
	if (typeof address === 'string') {
		var colonIdx = address.lastIndexOf(':');
		if (colonIdx === -1) throw new Error('connect: invalid address format, expected "hostname:port"');
		hostname = address.substring(0, colonIdx);
		port = parseInt(address.substring(colonIdx + 1), 10);
	} else if (address && typeof address === 'object') {
		hostname = address.hostname;
		port = typeof address.port === 'string' ? parseInt(address.port, 10) : address.port;
	} else {
		throw new Error('connect: address must be a string or {hostname, port} object');
	}

	if (!hostname) throw new Error('connect: hostname is required');
	if (isNaN(port) || port < 1 || port > 65535) throw new Error('connect: port must be 1-65535');

	options = options || {};
	var secure = (options.secureTransport === 'on') ? 'on' : 'off';
	var allowHalfOpen = !!options.allowHalfOpen;

	var socketID = __tcpConnect(requestID, hostname, String(port), secure);

	var closedResolve, closedReject;
	var closedPromise = new Promise(function(resolve, reject) {
		closedResolve = resolve;
		closedReject = reject;
	});

	var socketClosed = false;

	var openedPromise = Promise.resolve({
		remoteAddress: hostname + ':' + port,
		localAddress: '0.0.0.0:0'
	});

	var readable = new ReadableStream({
		pull: function(controller) {
			if (socketClosed) { controller.close(); return; }
			var result = __tcpRead(requestID, socketID, 4096);
			if (result === '') return;
			if (result === 'EOF') {
				controller.close();
				if (!socketClosed && !allowHalfOpen) {
					socketClosed = true;
					try { __tcpClose(requestID, socketID); } catch(e) {}
					closedResolve();
				}
				return;
			}
			var raw = atob(result);
			var bytes = new Uint8Array(raw.length);
			for (var i = 0; i < raw.length; i++) bytes[i] = raw.charCodeAt(i);
			controller.enqueue(bytes);
		}
	});

	var writable = new WritableStream({
		write: function(chunk) {
			if (socketClosed) throw new Error('Socket is closed');
			var bytes;
			if (typeof chunk === 'string') { bytes = new TextEncoder().encode(chunk); }
			else if (chunk instanceof Uint8Array) { bytes = chunk; }
			else if (chunk instanceof ArrayBuffer) { bytes = new Uint8Array(chunk); }
			else if (ArrayBuffer.isView(chunk)) { bytes = new Uint8Array(chunk.buffer, chunk.byteOffset, chunk.byteLength); }
			else { throw new Error('write: unsupported chunk type'); }
			var b64 = btoa(String.fromCharCode.apply(null, bytes));
			__tcpWrite(requestID, socketID, b64);
		},
		close: function() {
			if (!socketClosed) {
				socketClosed = true;
				try { __tcpClose(requestID, socketID); } catch(e) {}
				closedResolve();
			}
		},
		abort: function(reason) {
			if (!socketClosed) {
				socketClosed = true;
				try { __tcpClose(requestID, socketID); } catch(e) {}
				closedReject(reason || new Error('Socket aborted'));
			}
		}
	});

	var socket = {
		readable: readable,
		writable: writable,
		closed: closedPromise,
		opened: openedPromise,
		close: function() {
			if (!socketClosed) {
				socketClosed = true;
				try { __tcpClose(requestID, socketID); } catch(e) {}
				closedResolve();
			}
			return closedPromise;
		},
		startTls: function() {
			if (socketClosed) throw new Error('Socket is closed');
			var newSocketID = __tcpStartTls(requestID, socketID, hostname);
			var tlsClosedResolve, tlsClosedReject;
			var tlsClosedPromise = new Promise(function(resolve, reject) {
				tlsClosedResolve = resolve;
				tlsClosedReject = reject;
			});
			var tlsClosed = false;
			var tlsReadable = new ReadableStream({
				pull: function(controller) {
					if (tlsClosed) { controller.close(); return; }
					var result = __tcpRead(requestID, newSocketID, 4096);
					if (result === '') return;
					if (result === 'EOF') {
						controller.close();
						if (!tlsClosed) { tlsClosed = true; try { __tcpClose(requestID, newSocketID); } catch(e) {} tlsClosedResolve(); }
						return;
					}
					var raw = atob(result);
					var bytes = new Uint8Array(raw.length);
					for (var i = 0; i < raw.length; i++) bytes[i] = raw.charCodeAt(i);
					controller.enqueue(bytes);
				}
			});
			var tlsWritable = new WritableStream({
				write: function(chunk) {
					if (tlsClosed) throw new Error('Socket is closed');
					var bytes;
					if (typeof chunk === 'string') { bytes = new TextEncoder().encode(chunk); }
					else if (chunk instanceof Uint8Array) { bytes = chunk; }
					else if (chunk instanceof ArrayBuffer) { bytes = new Uint8Array(chunk); }
					else if (ArrayBuffer.isView(chunk)) { bytes = new Uint8Array(chunk.buffer, chunk.byteOffset, chunk.byteLength); }
					else { throw new Error('write: unsupported chunk type'); }
					var b64 = btoa(String.fromCharCode.apply(null, bytes));
					__tcpWrite(requestID, newSocketID, b64);
				},
				close: function() {
					if (!tlsClosed) { tlsClosed = true; try { __tcpClose(requestID, newSocketID); } catch(e) {} tlsClosedResolve(); }
				}
			});
			return {
				readable: tlsReadable,
				writable: tlsWritable,
				closed: tlsClosedPromise,
				opened: Promise.resolve({ remoteAddress: hostname + ':' + port, localAddress: '0.0.0.0:0' }),
				close: function() {
					if (!tlsClosed) { tlsClosed = true; try { __tcpClose(requestID, newSocketID); } catch(e) {} tlsClosedResolve(); }
					return tlsClosedPromise;
				},
				startTls: function() { throw new Error('Already using TLS'); }
			};
		}
	};

	return socket;
};

})();
`

// getTCPSocketMap retrieves the tcpSocketMap from the request state
// extension map, creating it if necessary.
func getTCPSocketMap(state *core.RequestState) *tcpSocketMap {
	v := state.GetExt("tcpSocketMap")
	if v != nil {
		return v.(*tcpSocketMap)
	}
	tsm := &tcpSocketMap{
		sockets: make(map[string]net.Conn),
		buffers: make(map[string]*tcpSocketBuffer),
	}
	state.SetExt("tcpSocketMap", tsm)
	return tsm
}

// SetupTCPSocket registers Go-backed TCP helpers and evaluates the JS wrapper.
func SetupTCPSocket(rt core.JSRuntime, _ *eventloop.EventLoop) error {
	// __tcpConnect(reqIDStr, hostname, port, secure) -> socketID
	if err := rt.RegisterFunc("__tcpConnect", func(reqIDStr, hostname, port, secure string) (string, error) {
		reqID := core.ParseReqID(reqIDStr)
		state := core.GetRequestState(reqID)
		if state == nil {
			return "", fmt.Errorf("tcpConnect: invalid request state")
		}

		tsm := getTCPSocketMap(state)
		if len(tsm.sockets) >= maxTCPSockets {
			return "", fmt.Errorf("TCP: maximum socket limit reached")
		}

		var conn net.Conn
		var err error

		if secure == "on" {
			rawConn, dialErr := ssrfSafeTCPDial(context.Background(), hostname, port)
			if dialErr != nil {
				return "", fmt.Errorf("tcpConnect: %s", dialErr.Error())
			}
			tlsConn := tls.Client(rawConn, &tls.Config{
				ServerName: hostname,
			})
			if err = tlsConn.Handshake(); err != nil {
				_ = rawConn.Close()
				return "", fmt.Errorf("tcpConnect: TLS handshake failed: %s", err.Error())
			}
			conn = tlsConn
		} else {
			conn, err = ssrfSafeTCPDial(context.Background(), hostname, port)
			if err != nil {
				return "", fmt.Errorf("tcpConnect: %s", err.Error())
			}
		}

		tsm.nextID++
		socketID := fmt.Sprintf("tcp_%d", tsm.nextID)

		tsm.sockets[socketID] = conn

		buf := &tcpSocketBuffer{conn: conn, hasData: make(chan struct{}, 1)}
		tsm.buffers[socketID] = buf
		go buf.readLoop()

		// Register cleanup so ClearRequestState closes all TCP sockets.
		state.RegisterCleanup(func() {
			cleanupTCPSockets(state)
		})

		return socketID, nil
	}); err != nil {
		return err
	}

	// __tcpRead(reqIDStr, socketID, maxBytes) -> base64 data, "" for no data, "EOF" for closed
	if err := rt.RegisterFunc("__tcpRead", func(reqIDStr, socketID string, maxBytes int) (string, error) {
		reqID := core.ParseReqID(reqIDStr)
		state := core.GetRequestState(reqID)
		if state == nil {
			return "", fmt.Errorf("tcpRead: invalid request state")
		}
		tsm := getTCPSocketMap(state)
		buf, ok := tsm.buffers[socketID]
		if !ok {
			return "", fmt.Errorf("tcpRead: unknown socket ID")
		}

		for attempts := 0; attempts < 30; attempts++ {
			data, eof, _ := buf.take(maxBytes)
			if data != "" {
				return data, nil
			}
			if eof {
				return "EOF", nil
			}
			buf.waitForData(1 * time.Second)
		}
		return "", nil
	}); err != nil {
		return err
	}

	// __tcpWrite(reqIDStr, socketID, b64data)
	if err := rt.RegisterFunc("__tcpWrite", func(reqIDStr, socketID, b64data string) (int, error) {
		reqID := core.ParseReqID(reqIDStr)
		state := core.GetRequestState(reqID)
		if state == nil {
			return 0, fmt.Errorf("tcpWrite: invalid request state")
		}
		tsm := getTCPSocketMap(state)
		conn, ok := tsm.sockets[socketID]
		if !ok {
			return 0, fmt.Errorf("tcpWrite: unknown socket ID")
		}
		data, err := base64.StdEncoding.DecodeString(b64data)
		if err != nil {
			return 0, fmt.Errorf("tcpWrite: invalid base64: %s", err.Error())
		}
		if _, err := conn.Write(data); err != nil {
			return 0, fmt.Errorf("tcpWrite: %s", err.Error())
		}
		return 1, nil
	}); err != nil {
		return err
	}

	// __tcpClose(reqIDStr, socketID)
	if err := rt.RegisterFunc("__tcpClose", func(reqIDStr, socketID string) {
		reqID := core.ParseReqID(reqIDStr)
		state := core.GetRequestState(reqID)
		if state == nil {
			return
		}
		tsm := getTCPSocketMap(state)
		conn, ok := tsm.sockets[socketID]
		if !ok {
			return
		}
		_ = conn.Close()
		delete(tsm.sockets, socketID)
		delete(tsm.buffers, socketID)
	}); err != nil {
		return err
	}

	// __tcpStartTls(reqIDStr, socketID, hostname) -> new socketID
	if err := rt.RegisterFunc("__tcpStartTls", func(reqIDStr, socketID, hostname string) (string, error) {
		reqID := core.ParseReqID(reqIDStr)
		state := core.GetRequestState(reqID)
		if state == nil {
			return "", fmt.Errorf("tcpStartTls: invalid request state")
		}
		tsm := getTCPSocketMap(state)
		conn, ok := tsm.sockets[socketID]
		if !ok {
			return "", fmt.Errorf("tcpStartTls: unknown socket ID")
		}

		delete(tsm.sockets, socketID)
		delete(tsm.buffers, socketID)

		tlsConn := tls.Client(conn, &tls.Config{
			ServerName: hostname,
		})
		if err := tlsConn.Handshake(); err != nil {
			_ = conn.Close()
			return "", fmt.Errorf("tcpStartTls: TLS handshake failed: %s", err.Error())
		}

		tsm.nextID++
		newSocketID := fmt.Sprintf("tcp_%d", tsm.nextID)
		tsm.sockets[newSocketID] = tlsConn

		buf := &tcpSocketBuffer{conn: tlsConn, hasData: make(chan struct{}, 1)}
		tsm.buffers[newSocketID] = buf
		go buf.readLoop()

		return newSocketID, nil
	}); err != nil {
		return err
	}

	return rt.Eval(tcpSocketJS)
}

// ssrfSafeTCPDial performs SSRF-safe TCP connection by resolving DNS once
// and connecting directly to the validated IP, preventing DNS rebinding attacks.
func ssrfSafeTCPDial(ctx context.Context, hostname, port string) (net.Conn, error) {
	if !TcpSSRFEnabled {
		return net.Dial("tcp", net.JoinHostPort(hostname, port))
	}

	lower := strings.ToLower(hostname)
	if lower == "localhost" || strings.HasSuffix(lower, ".localhost") {
		return nil, fmt.Errorf("connections to private addresses are not allowed")
	}

	if ip := net.ParseIP(hostname); ip != nil {
		if IsPrivateIP(ip) {
			return nil, fmt.Errorf("connections to private addresses are not allowed")
		}
		dialer := &net.Dialer{}
		return dialer.DialContext(ctx, "tcp", net.JoinHostPort(hostname, port))
	}

	ips, err := net.DefaultResolver.LookupIPAddr(ctx, hostname)
	if err != nil {
		return nil, fmt.Errorf("DNS lookup failed: %w", err)
	}

	var safeIP net.IPAddr
	found := false
	for _, ip := range ips {
		if !IsPrivateIP(ip.IP) {
			safeIP = ip
			found = true
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("connections to private addresses are not allowed")
	}

	dialer := &net.Dialer{}
	return dialer.DialContext(ctx, "tcp", net.JoinHostPort(safeIP.IP.String(), port))
}

// cleanupTCPSockets closes all TCP sockets for a request state.
func cleanupTCPSockets(state *core.RequestState) {
	v := state.GetExt("tcpSocketMap")
	if v == nil {
		return
	}
	tsm := v.(*tcpSocketMap)
	for _, conn := range tsm.sockets {
		_ = conn.Close()
	}
	tsm.sockets = nil
	tsm.buffers = nil
}
