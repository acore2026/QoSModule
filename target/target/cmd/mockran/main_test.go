package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	adaptiveqos "github.com/acore2026/adaptive-qos"
	"github.com/acore2026/adaptive-qos/ranapi"
)

func TestHandleUpdateAcceptsValidRequest(t *testing.T) {
	handler := handleUpdate(serverConfig{
		status:     adaptiveqos.StatusAccepted,
		message:    "ok",
		httpStatus: http.StatusOK,
		strict:     true,
	})
	request := ranapi.Request{
		RequestID: "req-1",
		Mask:      123,
		RNTI:      4660,
		QFI:       9,
		QPDB:      75,
		QMBRUL:    9600000,
		QGBRUL:    7680000,
		BurstInfo: ranapi.BurstInfo{
			ULBurstSize:     120000,
			ULBurstDuration: 100,
		},
	}

	recorder := httptest.NewRecorder()
	body, _ := json.Marshal(request)
	req := httptest.NewRequest(http.MethodPost, ranapi.DefaultPath, bytes.NewReader(body))
	handler(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d, body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	var response feedback
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.RequestID != "req-1" || response.Status != adaptiveqos.StatusAccepted || response.Message != "ok" {
		t.Fatalf("unexpected response: %+v", response)
	}
}

func TestHandleUpdateRejectsInvalidRequestInStrictMode(t *testing.T) {
	handler := handleUpdate(serverConfig{strict: true})
	request := ranapi.Request{RequestID: "req-1"}

	recorder := httptest.NewRecorder()
	body, _ := json.Marshal(request)
	req := httptest.NewRequest(http.MethodPost, ranapi.DefaultPath, bytes.NewReader(body))
	handler(recorder, req)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status code = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
	var response feedback
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Status != adaptiveqos.StatusRejected || response.ErrorCode != "INVALID_PARAM" {
		t.Fatalf("unexpected response: %+v", response)
	}
}

func TestHandleUpdateAllowsInvalidRequestWhenStrictDisabled(t *testing.T) {
	handler := handleUpdate(serverConfig{
		status:     adaptiveqos.StatusAccepted,
		message:    "accepted",
		httpStatus: http.StatusOK,
		strict:     false,
	})
	request := ranapi.Request{RequestID: "req-1"}

	recorder := httptest.NewRecorder()
	body, _ := json.Marshal(request)
	req := httptest.NewRequest(http.MethodPost, ranapi.DefaultPath, bytes.NewReader(body))
	handler(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", recorder.Code, http.StatusOK)
	}
}

func TestValidateRequestRequiresDLPairTogether(t *testing.T) {
	request := ranapi.Request{
		RequestID: "req-1",
		Mask:      123,
		RNTI:      4660,
		QFI:       9,
		QPDB:      75,
		QMBRUL:    9600000,
		QGBRUL:    7680000,
		BurstInfo: ranapi.BurstInfo{
			ULBurstSize:     120000,
			ULBurstDuration: 100,
			DLBurstSize:     200000,
		},
	}
	if err := validateRequest(request); err == nil {
		t.Fatal("validateRequest() accepted partial DL burst")
	}
	request.BurstInfo.DLBurstDuration = 100
	if err := validateRequest(request); err != nil {
		t.Fatalf("validateRequest() error = %v", err)
	}
}
