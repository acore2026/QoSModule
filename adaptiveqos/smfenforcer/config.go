package smfenforcer

import (
	"log"
	"net/http"
	"time"
)

// ARPConfig carries the ARP allocation/retention values for the QoS flow the
// SMF installs via the OAM endpoint.
type ARPConfig struct {
	PriorityLevel uint8
	PreemptCap    bool // true=MAY_PREEMPT, false=NOT_PREEMPT
	PreemptVuln   bool // true=PREEMPTABLE, false=NOT_PREEMPTABLE
}

// Config configures the SMF enforcer.
//
// SMFEndpoint is the full URL of the fork SMF OAM endpoint, e.g.
// http://10.100.200.8:8000/nsmf-oam/v1/qos-update.
//
// The enforcer emits the oamQoSUpdateRequest body (ue_ip/five_qi/mbr_ul "X bps"
// /arp) expected by acore2026/smf HandleOAMQoSUpdate, which resolves the PDU
// session by UE IP (GetSMContextByPDUAddress) and pushes PFCP(QER)+N1N2(NGAP
// modify) to the UPF/AMF/gNB. SUPI is not needed: the SMF addresses the
// session by UE IP, so no supi-map is required (unlike the AF/PCF path).
type Config struct {
	SMFEndpoint   string
	Timeout       time.Duration
	DefaultFiveQI uint8
	ARP           ARPConfig
	HTTPClient    *http.Client
	Logger        *log.Logger
}

// DefaultConfig returns a Config that matches the verified 方案 A request
// (5QI=2 GBR, ARP priority 8, MAY_PREEMPT, NOT_PREEMPTABLE).
func DefaultConfig() Config {
	return Config{
		Timeout:       5 * time.Second,
		DefaultFiveQI: 2,
		ARP:           ARPConfig{PriorityLevel: 8, PreemptCap: true, PreemptVuln: false},
	}
}
