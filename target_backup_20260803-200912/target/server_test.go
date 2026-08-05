package target

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestServerRepliesToSender(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv := NewServer(Config{
		ListenAddr:     "127.0.0.1:0",
		ReplyPrefix:    "reply: ",
		ReadBufferSize: 2048,
	})
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		_ = srv.Close()
		<-errCh
	})

	addr := waitForAddr(t, srv)
	conn := dialUDP(t, addr)
	defer conn.Close()

	_, err := conn.Write([]byte("hello"))
	if err != nil {
		t.Fatalf("write request: %v", err)
	}

	buf := make([]byte, 64)
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("read reply: %v", err)
	}
	if got, want := string(buf[:n]), "reply: hello"; got != want {
		t.Fatalf("reply mismatch: got %q, want %q", got, want)
	}
}

func TestParseClientIPPayload(t *testing.T) {
	msg, ok := parseClientIPPayload([]byte("CLIENT-IP: 192.168.1.20\r\n\r\nhello"))
	if !ok {
		t.Fatal("parseClientIPPayload() did not recognize client IP header")
	}
	if got, want := msg.ClientIP, "192.168.1.20"; got != want {
		t.Fatalf("ClientIP = %q, want %q", got, want)
	}
	if got, want := string(msg.Payload), "hello"; got != want {
		t.Fatalf("Payload = %q, want %q", got, want)
	}
}

func TestServerRepliesToOriginalPayloadWhenClientIPHeaderExists(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv := NewServer(Config{
		ListenAddr:     "127.0.0.1:0",
		ReplyPrefix:    "reply: ",
		ReadBufferSize: 2048,
	})
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		_ = srv.Close()
		<-errCh
	})

	addr := waitForAddr(t, srv)
	conn := dialUDP(t, addr)
	defer conn.Close()

	assertReply(t, conn, "CLIENT-IP: 192.168.1.20\r\n\r\nhello", "reply: hello")
}

func TestServerRepliesToMultipleSenders(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv := NewServer(Config{
		ListenAddr:     "127.0.0.1:0",
		ReplyPrefix:    "reply: ",
		ReadBufferSize: 2048,
	})
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		_ = srv.Close()
		<-errCh
	})

	addr := waitForAddr(t, srv)
	first := dialUDP(t, addr)
	defer first.Close()
	second := dialUDP(t, addr)
	defer second.Close()

	assertReply(t, first, "one", "reply: one")
	assertReply(t, second, "two", "reply: two")
}

func waitForAddr(t *testing.T, srv *Server) string {
	t.Helper()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if addr := srv.Addr(); addr != "" {
			return addr
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("server did not start listening")
	return ""
}

func dialUDP(t *testing.T, addr string) *net.UDPConn {
	t.Helper()

	remote, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		t.Fatalf("resolve server addr: %v", err)
	}
	conn, err := net.DialUDP("udp", nil, remote)
	if err != nil {
		t.Fatalf("dial udp: %v", err)
	}
	return conn
}

func assertReply(t *testing.T, conn *net.UDPConn, payload, want string) {
	t.Helper()

	if _, err := conn.Write([]byte(payload)); err != nil {
		t.Fatalf("write %q: %v", payload, err)
	}
	buf := make([]byte, 64)
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("read reply for %q: %v", payload, err)
	}
	if got := string(buf[:n]); got != want {
		t.Fatalf("reply mismatch for %q: got %q, want %q", payload, got, want)
	}
}
