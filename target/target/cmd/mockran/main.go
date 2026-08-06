package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	adaptiveqos "github.com/acore2026/adaptive-qos"
	"github.com/acore2026/adaptive-qos/ranapi"
)

type serverConfig struct {
	path       string
	status     string
	message    string
	errorCode  string
	httpStatus int
	delay      time.Duration
	strict     bool
	logger     *log.Logger
}

type feedback struct {
	RequestID string `json:"request_id"`
	Status    string `json:"status"`
	ErrorCode string `json:"error_code,omitempty"`
	Message   string `json:"message"`
}

func main() {
	var bind, path, status, message, errorCode string
	var httpStatus int
	var delay time.Duration
	var strict bool

	flag.StringVar(&bind, "b", "127.0.0.1:8080", "HTTP listen address")
	flag.StringVar(&path, "path", ranapi.DefaultPath, "RAN QoS update path")
	flag.StringVar(&status, "status", adaptiveqos.StatusAccepted, "mock RAN response status")
	flag.StringVar(&message, "message", "mock ran accepted", "mock RAN response message")
	flag.StringVar(&errorCode, "error-code", "", "mock RAN response error_code")
	flag.IntVar(&httpStatus, "http-status", http.StatusOK, "mock RAN HTTP status")
	flag.DurationVar(&delay, "delay", 0, "artificial response delay")
	flag.BoolVar(&strict, "strict", true, "validate required RAN request fields")
	flag.Parse()

	logger := log.New(os.Stdout, "[mock-ran] ", log.LstdFlags|log.Lmicroseconds)
	cfg := serverConfig{
		path:       path,
		status:     status,
		message:    message,
		errorCode:  errorCode,
		httpStatus: httpStatus,
		delay:      delay,
		strict:     strict,
		logger:     logger,
	}
	mux := http.NewServeMux()
	mux.HandleFunc(path, handleUpdate(cfg))

	server := &http.Server{
		Addr:              bind,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	logger.Printf("listening addr=%s path=%s strict=%t status=%s http_status=%d delay=%s", bind, path, strict, status, httpStatus, delay)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Fatalf("server failed: %v", err)
	}
}

func handleUpdate(cfg serverConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var request ranapi.Request
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			writeFeedback(w, http.StatusBadRequest, feedback{
				Status:    adaptiveqos.StatusRejected,
				ErrorCode: "INVALID_JSON",
				Message:   fmt.Sprintf("decode RAN request: %v", err),
			})
			return
		}

		if cfg.logger != nil {
			cfg.logger.Printf(
				"received request_id=%s rnti=%d qfi=%d mask=%d mbr_ul=%d mbr_dl=%d gbr_ul=%d gbr_dl=%d pdb=%d ul_burst_size=%d ul_burst_duration=%d dl_burst_size=%d dl_burst_duration=%d",
				request.RequestID,
				request.RNTI,
				request.QFI,
				request.Mask,
				request.QMBRUL,
				request.QMBRDL,
				request.QGBRUL,
				request.QGBRDL,
				request.QPDB,
				request.BurstInfo.ULBurstSize,
				request.BurstInfo.ULBurstDuration,
				request.BurstInfo.DLBurstSize,
				request.BurstInfo.DLBurstDuration,
			)
		}

		if cfg.strict {
			if err := validateRequest(request); err != nil {
				writeFeedback(w, http.StatusBadRequest, feedback{
					RequestID: request.RequestID,
					Status:    adaptiveqos.StatusRejected,
					ErrorCode: "INVALID_PARAM",
					Message:   err.Error(),
				})
				return
			}
		}

		if cfg.delay > 0 {
			timer := time.NewTimer(cfg.delay)
			select {
			case <-r.Context().Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}

		writeFeedback(w, cfg.httpStatus, feedback{
			RequestID: request.RequestID,
			Status:    cfg.status,
			ErrorCode: cfg.errorCode,
			Message:   cfg.message,
		})
	}
}

func validateRequest(request ranapi.Request) error {
	switch {
	case request.RequestID == "":
		return errors.New("request_id is required")
	case request.RNTI == 0:
		return errors.New("rnti is required")
	case request.QFI == 0:
		return errors.New("q_qfi is required")
	case request.Mask == 0:
		return errors.New("mask is required")
	case request.QMBRUL == 0:
		return errors.New("q_mbr_ul is required")
	case request.QGBRUL == 0:
		return errors.New("q_gbr_ul is required")
	case request.QPDB == 0:
		return errors.New("q_pdb is required")
	case request.BurstInfo.ULBurstSize == 0:
		return errors.New("burst_info.ul_burst_size is required")
	case request.BurstInfo.ULBurstDuration == 0:
		return errors.New("burst_info.ul_burst_duration is required")
	}
	if request.BurstInfo.DLBurstSize == 0 && request.BurstInfo.DLBurstDuration > 0 {
		return errors.New("burst_info.dl_burst_size is required when dl_burst_duration is set")
	}
	if request.BurstInfo.DLBurstSize > 0 && request.BurstInfo.DLBurstDuration == 0 {
		return errors.New("burst_info.dl_burst_duration is required when dl_burst_size is set")
	}
	return nil
}

func writeFeedback(w http.ResponseWriter, httpStatus int, response feedback) {
	if httpStatus == 0 {
		httpStatus = http.StatusOK
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(httpStatus)
	_ = json.NewEncoder(w).Encode(response)
}
