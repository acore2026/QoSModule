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
	Reliability    ReliabilityConfig
	Handler        Handler
}

type Server struct {
	cfg Config

	mu    sync.Mutex
	conn  *net.UDPConn
	cache *reliableCache
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
	cfg.Reliability = normalizeReliabilityConfig(cfg.Reliability)
	return &Server{
		cfg:   cfg,
		cache: newReliableCache(cfg.Reliability),
	}
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
		s.logReceivedMessage(remote, msg.ClientIP, payload)
		reply, err := s.replyPayload(ctx, msg)
		if err != nil {
			s.logf("request from %s failed: %v", remote, err)
			continue
		}
		if _, err := conn.WriteToUDP(reply, remote); err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("write udp reply to %s: %w", remote, err)
		}
		s.logf("sent response to %s bytes=%d", remote, len(reply))
	}
}

func (s *Server) logReceivedMessage(remote net.Addr, clientIP string, payload []byte) {
	requestID, reliablePayload, ok := decodeReliableRequest(payload)
	if ok {
		if clientIP != "" {
			s.logf("received reliable request from %s for client %s request_id=%s payload=%q", remote, clientIP, requestID, reliablePayload)
			return
		}
		s.logf("received reliable request from %s request_id=%s payload=%q", remote, requestID, reliablePayload)
		return
	}
	if clientIP != "" {
		s.logf("received from %s for client %s: %q", remote, clientIP, payload)
		return
	}
	s.logf("received from %s: %q", remote, payload)
}

func (s *Server) replyPayload(ctx context.Context, msg Message) ([]byte, error) {
	requestID, reliablePayload, ok := decodeReliableRequest(msg.Payload)
	if !ok {
		return s.handlePayload(ctx, msg)
	}
	if len(reliablePayload) > s.cfg.Reliability.MaxPayloadBytes {
		s.logf("reliable request rejected request_id=%s reason=payload_too_large payload_bytes=%d max_payload_bytes=%d", requestID, len(reliablePayload), s.cfg.Reliability.MaxPayloadBytes)
		response, err := encodeReliableResponse(requestID, []byte("error: payload too large"))
		if err != nil {
			return []byte("error: payload too large"), nil
		}
		return response, nil
	}
	reliableMsg := Message{ClientIP: msg.ClientIP, Payload: reliablePayload}
	responsePayload, duplicate, err := s.cache.reply(requestID, func() ([]byte, error) {
		return s.handlePayload(ctx, reliableMsg)
	})
	if err != nil {
		return nil, err
	}
	if duplicate {
		s.logf("duplicate reliable request request_id=%s", requestID)
	}
	response, err := encodeReliableResponse(requestID, responsePayload)
	if err != nil {
		return []byte("error: encode reliable response"), nil
	}
	s.logf("reliable response encoded request_id=%s payload_bytes=%d response_bytes=%d duplicate=%v", requestID, len(responsePayload), len(response), duplicate)
	return response, nil
}

func (s *Server) handlePayload(ctx context.Context, msg Message) ([]byte, error) {
	if s.cfg.Handler == nil {
		return append([]byte(s.cfg.ReplyPrefix), msg.Payload...), nil
	}
	return s.cfg.Handler.Handle(ctx, msg)
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
