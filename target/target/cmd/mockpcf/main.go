package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

// mockpcf is a Phase-2 stand-in for a real free5gc PCF Npcf_PolicyAuthorization
// service. It accepts the intermediate AF JSON emitted by afenforcer and
// records/terminates app-sessions so the QoS module can be exercised without
// a real core network. See NGAP下发改造方案.md §8 阶段2.
type serverConfig struct {
	createPath  string
	status      string
	message     string
	errorCode   string
	httpStatus  int
	delay       time.Duration
	strict      bool
	logger      *log.Logger
	mu          sync.Mutex
	sessions    map[string]string // ref -> request_id
	nextRef     uint64
}

type appSessionRequest struct {
	RequestID  string  `json:"request_id"`
	UEIP      string  `json:"ue_ip"`
	SUPI      string  `json:"supi,omitempty"`
	DNN       string  `json:"dnn,omitempty"`
	QFI       uint8   `json:"qfi,omitempty"`
	FiveQI    uint8   `json:"five_qi"`
	MBRUL     uint64  `json:"mbr_ul"`
	MBRDL     uint64  `json:"mbr_dl,omitempty"`
	GBRUL     uint64  `json:"gbr_ul,omitempty"`
	GBRDL     uint64  `json:"gbr_dl,omitempty"`
	DurationMs uint64 `json:"duration_ms"`
}

type createResponse struct {
	AppSessionID string `json:"app_session_id"`
	Status       string `json:"status"`
	Message      string `json:"message,omitempty"`
	ErrorCode    string `json:"error_code,omitempty"`
}

type terminateResponse struct {
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

func main() {
	var bind, status, message, errorCode, createPath string
	var httpStatus int
	var delay time.Duration
	var strict bool

	flag.StringVar(&bind, "b", "127.0.0.1:8090", "HTTP listen address")
	flag.StringVar(&createPath, "path", "/npcf-policyauthorization/v1/app-sessions", "app-session collection path")
	flag.StringVar(&status, "status", "started", "mock PCF create response status")
	flag.StringVar(&message, "message", "mock pcf accepted", "mock PCF create response message")
	flag.StringVar(&errorCode, "error-code", "", "mock PCF create response error_code")
	flag.IntVar(&httpStatus, "http-status", http.StatusCreated, "mock PCF create HTTP status")
	flag.DurationVar(&delay, "delay", 0, "artificial response delay")
	flag.BoolVar(&strict, "strict", true, "validate required AF request fields")
	flag.Parse()

	logger := log.New(os.Stdout, "[mock-pcf] ", log.LstdFlags|log.Lmicroseconds)
	cfg := &serverConfig{
		createPath: strings.TrimRight(createPath, "/"),
		status:     status,
		message:    message,
		errorCode:  errorCode,
		httpStatus: httpStatus,
		delay:      delay,
		strict:     strict,
		logger:     logger,
		sessions:   make(map[string]string),
	}

	mux := http.NewServeMux()
	mux.HandleFunc(cfg.createPath, cfg.handleCreate)            // POST collection
	mux.HandleFunc(cfg.createPath+"/", cfg.handleMember)        // DELETE member

	srv := &http.Server{Addr: bind, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		logger.Printf("mock pcf listening on %s (create %s)", bind, cfg.createPath)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Fatalf("listen failed: %v", err)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	_ = srv.Shutdown(context.Background())
}

func (c *serverConfig) handleCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if c.delay > 0 {
		time.Sleep(c.delay)
	}
	var req appSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		c.writeError(w, http.StatusBadRequest, "INVALID_PARAM", "decode: "+err.Error())
		return
	}
	if c.strict {
		if req.RequestID == "" {
			c.writeError(w, http.StatusBadRequest, "INVALID_PARAM", "request_id is required")
			return
		}
		if req.UEIP == "" {
			c.writeError(w, http.StatusBadRequest, "INVALID_PARAM", "ue_ip is required")
			return
		}
		if req.SUPI == "" {
			c.writeError(w, http.StatusBadRequest, "INVALID_PARAM", "supi is required (supi_map empty?)")
			return
		}
		if req.FiveQI == 0 {
			c.writeError(w, http.StatusBadRequest, "INVALID_PARAM", "five_qi is required")
			return
		}
	}

	c.mu.Lock()
	c.nextRef++
	ref := fmt.Sprintf("appsess-%d", c.nextRef)
	c.sessions[ref] = req.RequestID
	c.mu.Unlock()

	c.logger.Printf("create app-session request_id=%s ue=%s supi=%s qfi=%d five_qi=%d mbr_ul=%d mbr_dl=%d gbr_ul=%d gbr_dl=%d duration_ms=%d ref=%s",
		req.RequestID, req.UEIP, req.SUPI, req.QFI, req.FiveQI,
		req.MBRUL, req.MBRDL, req.GBRUL, req.GBRDL, req.DurationMs, ref)

	resp := createResponse{AppSessionID: ref, Status: c.status, Message: c.message, ErrorCode: c.errorCode}
	body, _ := json.Marshal(resp)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Location", c.createPath+"/"+ref)
	w.WriteHeader(c.httpStatus)
	_, _ = w.Write(body)
}

func (c *serverConfig) handleMember(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if c.delay > 0 {
		time.Sleep(c.delay)
	}
	ref := strings.TrimPrefix(r.URL.Path, c.createPath+"/")
	c.mu.Lock()
	reqID, ok := c.sessions[ref]
	if ok {
		delete(c.sessions, ref)
	}
	c.mu.Unlock()
	if !ok {
		c.writeError(w, http.StatusNotFound, "APP_SESSION_NOT_FOUND", "no such app-session "+ref)
		return
	}
	c.logger.Printf("terminate app-session request_id=%s ref=%s", reqID, ref)
	resp := terminateResponse{Status: "ended"}
	body, _ := json.Marshal(resp)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func (c *serverConfig) writeError(w http.ResponseWriter, status int, code, msg string) {
	resp := createResponse{Status: "rejected", ErrorCode: code, Message: msg}
	body, _ := json.Marshal(resp)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
