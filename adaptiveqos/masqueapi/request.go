package masqueapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	adaptiveqos "github.com/acore2026/adaptive-qos"
)

var ErrInvalidRequest = errors.New("invalid masque qos request")

type PacketFilter struct {
	SrcIP    string `json:"src_ip,omitempty"`
	DstIP    string `json:"dst_ip,omitempty"`
	SrcPort  uint16 `json:"src_port,omitempty"`
	DstPort  uint16 `json:"dst_port,omitempty"`
	Protocol uint8  `json:"protocol,omitempty"`
}

type BurstInfo struct {
	ULBurstSize     *uint64 `json:"ul_burst_size"`
	ULBurstDuration *uint64 `json:"ul_burst_duration"`
	DLBurstSize     *uint64 `json:"dl_burst_size"`
	DLBurstDuration *uint64 `json:"dl_burst_duration"`

	ArriveTimeToNextBurst uint64 `json:"arrive_time_to_next_burst,omitempty"`

	ULTransitDelay       uint64 `json:"ul_transit_delay,omitempty"`
	DLTransitDelay       uint64 `json:"dl_transit_delay,omitempty"`
	ULTransmissionDelay  uint64 `json:"ul_transmission_delay,omitempty"`
	DLTransmissionDelay  uint64 `json:"dl_transmission_delay,omitempty"`
	ULRealTransmissionMS uint64 `json:"ul_real_transmission_delay,omitempty"`
	DLRealTransmissionMS uint64 `json:"dl_real_transmission_delay,omitempty"`
}

type ServiceExperience struct {
	MOSScore float64 `json:"mos_score,omitempty"`
}

type ServiceInfo struct {
	ServiceType       string             `json:"service_type,omitempty"`
	FrameRate         uint64             `json:"frame_rate,omitempty"`
	MaxFrameSize      uint64             `json:"max_frame_size,omitempty"`
	Resolution        string             `json:"resolution,omitempty"`
	CodeRate          uint64             `json:"code_rate,omitempty"`
	E2EDelay          uint64             `json:"e2e_delay,omitempty"`
	ServiceExperience *ServiceExperience `json:"service_experience,omitempty"`
	OtherMetrics      map[string]any     `json:"other_metrics,omitempty"`
}

type Request struct {
	RequestID      string        `json:"request_id"`
	RNTI           *uint32       `json:"rnti"`
	QFI            *uint8        `json:"qfi"`
	PacketFilter   *PacketFilter `json:"packet_filter,omitempty"`
	TrafficPattern string        `json:"traffic_pattern,omitempty"`
	SourceAddress  string        `json:"source_address,omitempty"`
	BurstInfo      *BurstInfo    `json:"burst_info"`
	ServiceInfo    *ServiceInfo  `json:"service_info,omitempty"`
}

func Decode(payload []byte, proxyClientIP string) (Request, bool, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil {
		return Request{}, false, nil
	}
	recognized := hasAllFields(fields, "request_id", "rnti", "qfi", "burst_info")
	if !recognized {
		return Request{}, false, nil
	}

	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var request Request
	if err := decoder.Decode(&request); err != nil {
		return Request{}, true, fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	if request.SourceAddress == "" {
		request.SourceAddress = proxyClientIP
	}
	if err := request.Validate(); err != nil {
		return request, true, err
	}
	return request, true, nil
}

func (r Request) Validate() error {
	switch {
	case r.RequestID == "":
		return fmt.Errorf("%w: request_id is required", ErrInvalidRequest)
	case len(r.RequestID) > 32:
		return fmt.Errorf("%w: request_id exceeds 32 characters", ErrInvalidRequest)
	case r.RNTI == nil:
		return fmt.Errorf("%w: rnti is required", ErrInvalidRequest)
	case *r.RNTI > 65_535:
		return fmt.Errorf("%w: rnti must be in [0,65535]", ErrInvalidRequest)
	case r.QFI == nil:
		return fmt.Errorf("%w: qfi is required", ErrInvalidRequest)
	case *r.QFI > 63:
		return fmt.Errorf("%w: qfi must be in [0,63]", ErrInvalidRequest)
	case r.ServiceInfo == nil || r.ServiceInfo.E2EDelay == 0:
		return fmt.Errorf("%w: service_info.e2e_delay is required", ErrInvalidRequest)
	case r.BurstInfo == nil:
		return fmt.Errorf("%w: burst_info is required", ErrInvalidRequest)
	case r.BurstInfo.ULBurstSize == nil || *r.BurstInfo.ULBurstSize == 0:
		return fmt.Errorf("%w: ul_burst_size is required", ErrInvalidRequest)
	case r.BurstInfo.ULBurstDuration == nil || *r.BurstInfo.ULBurstDuration == 0:
		return fmt.Errorf("%w: ul_burst_duration is required", ErrInvalidRequest)
	}
	if r.BurstInfo.DLBurstSize != nil || r.BurstInfo.DLBurstDuration != nil {
		switch {
		case r.BurstInfo.DLBurstSize == nil || *r.BurstInfo.DLBurstSize == 0:
			return fmt.Errorf("%w: dl_burst_size must be greater than 0 when dl burst is provided", ErrInvalidRequest)
		case r.BurstInfo.DLBurstDuration == nil || *r.BurstInfo.DLBurstDuration == 0:
			return fmt.Errorf("%w: dl_burst_duration must be greater than 0 when dl burst is provided", ErrInvalidRequest)
		}
	}
	return nil
}

func (r Request) Intent() adaptiveqos.Intent {
	var e2eDelay uint64
	var serviceType string
	if r.ServiceInfo != nil {
		e2eDelay = r.ServiceInfo.E2EDelay
		serviceType = r.ServiceInfo.ServiceType
	}
	burst := r.BurstInfo
	ueAddress := r.SourceAddress
	if r.PacketFilter != nil && r.PacketFilter.SrcIP != "" {
		ueAddress = r.PacketFilter.SrcIP
	}
	intent := adaptiveqos.Intent{
		RequestID: r.RequestID,
		Flow: adaptiveqos.FlowSelector{
			RNTI:      dereference(r.RNTI),
			QFI:       dereference(r.QFI),
			UEAddress: ueAddress,
		},
		ULBurst: adaptiveqos.BurstDemand{
			SizeKB:     dereference(burst.ULBurstSize),
			DurationMS: dereference(burst.ULBurstDuration),
		},
		DLBurst: adaptiveqos.BurstDemand{
			SizeKB:     dereference(burst.DLBurstSize),
			DurationMS: dereference(burst.DLBurstDuration),
		},
		E2EDelayMS:       e2eDelay,
		ULTransitDelayMS: firstNonZero(burst.ULTransitDelay, burst.ULTransmissionDelay, burst.ULRealTransmissionMS),
		DLTransitDelayMS: firstNonZero(burst.DLTransitDelay, burst.DLTransmissionDelay, burst.DLRealTransmissionMS),
		ServiceType:      serviceType,
	}
	if r.PacketFilter != nil {
		intent.Filter = adaptiveqos.FlowFilter{
			SrcIP:    r.PacketFilter.SrcIP,
			DstIP:    r.PacketFilter.DstIP,
			SrcPort:  r.PacketFilter.SrcPort,
			DstPort:  r.PacketFilter.DstPort,
			Protocol: r.PacketFilter.Protocol,
		}
	}
	return intent
}

type Feedback struct {
	RequestID     string            `json:"request_id,omitempty"`
	Status        string            `json:"status"`
	ErrorCode     string            `json:"error_code,omitempty"`
	Message       string            `json:"message,omitempty"`
	AppliedConfig map[string]uint64 `json:"applied_config,omitempty"`
}

func ErrorFeedback(requestID, errorCode string, err error) []byte {
	message := ""
	if err != nil {
		message = err.Error()
	}
	payload, marshalErr := json.Marshal(Feedback{
		RequestID: requestID,
		Status:    adaptiveqos.StatusRejected,
		ErrorCode: errorCode,
		Message:   message,
	})
	if marshalErr != nil {
		return []byte(`{"status":"REJECTED","error_code":"INTERNAL_ERROR"}`)
	}
	return payload
}

func hasAllFields(fields map[string]json.RawMessage, names ...string) bool {
	for _, name := range names {
		if !fieldExists(fields, name) {
			return false
		}
	}
	return true
}

func fieldExists(fields map[string]json.RawMessage, name string) bool {
	if _, ok := fields[name]; ok {
		return true
	}
	for key := range fields {
		if strings.EqualFold(key, name) {
			return true
		}
	}
	return false
}

func firstNonZero(values ...uint64) uint64 {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func dereference[T uint8 | uint32 | uint64](value *T) T {
	if value == nil {
		return 0
	}
	return *value
}
