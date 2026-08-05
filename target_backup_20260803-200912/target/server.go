package target

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
)

const defaultReadBufferSize = 64 * 1024

type Handler interface {
	Handle(context.Context, Message) ([]byte, error)
}

type HandlerFunc func(context.Context, Message) ([]byte, error)

func (f HandlerFunc) Handle(ctx context.Context, message Message) ([]byte, error) {
	return f(ctx, message)
}

type Config struct {
	ListenAddr     string
	ReplyPrefix    string
	ReadBufferSize int
	Logger         *log.Logger
	Handler        Handler
}

type Server struct {
	cfg Config

	mu   sync.Mutex
	conn *net.UDPConn
}

type Message struct {
	ClientIP string
	Payload  []byte
}

func NewServer(cfg Config) *Server {
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = "0.0.0.0:7400"
	}
	if cfg.ReadBufferSize <= 0 {
		cfg.ReadBufferSize = defaultReadBufferSize
	}
	return &Server{cfg: cfg}
}

func (s *Server) Serve(ctx context.Context) error {
	addr, err := net.ResolveUDPAddr("udp", s.cfg.ListenAddr)
	if err != nil {
		return fmt.Errorf("resolve listen address: %w", err)
	}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return fmt.Errorf("listen udp: %w", err)
	}

	s.mu.Lock()
	s.conn = conn
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.conn = nil
		s.mu.Unlock()
		_ = conn.Close()
	}()

	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()

	s.logf("Target UDP Server listening on %s", conn.LocalAddr())
	buf := make([]byte, s.cfg.ReadBufferSize)
	for {
		n, remote, err := conn.ReadFromUDP(buf)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("read udp: %w", err)
		}

		msg, _ := parseClientIPPayload(buf[:n])
		payload := append([]byte(nil), msg.Payload...)
		if msg.ClientIP != "" {
			s.logf("received from %s for client %s: %q", remote, msg.ClientIP, payload)
		} else {
			s.logf("received from %s: %q", remote, payload)
		}
		var reply []byte
		if s.cfg.Handler == nil {
			reply = append([]byte(s.cfg.ReplyPrefix), payload...)
		} else {
			reply, err = s.cfg.Handler.Handle(ctx, msg)
			if err != nil {
				s.logf("request from %s failed: %v", remote, err)
				continue
			}
		}
		if _, err := conn.WriteToUDP(reply, remote); err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("write udp reply to %s: %w", remote, err)
		}
	}
}

func parseClientIPPayload(data []byte) (Message, bool) {
	const prefix = "CLIENT-IP: "
	separator := []byte("\r\n\r\n")
	headerEnd := bytes.Index(data, separator)
	if headerEnd < 0 || !bytes.HasPrefix(data, []byte(prefix)) {
		return Message{Payload: append([]byte(nil), data...)}, false
	}
	clientIP := string(data[len(prefix):headerEnd])
	payload := data[headerEnd+len(separator):]
	return Message{
		ClientIP: clientIP,
		Payload:  append([]byte(nil), payload...),
	}, true
}

func (s *Server) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn == nil {
		return ""
	}
	return s.conn.LocalAddr().String()
}

func (s *Server) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn == nil {
		return nil
	}
	return s.conn.Close()
}

func (s *Server) logf(format string, args ...any) {
	if s.cfg.Logger == nil {
		return
	}
	s.cfg.Logger.Printf(format, args...)
}

func NewLogger(w io.Writer) *log.Logger {
	return log.New(w, "", log.LstdFlags)
}
