package afenforcer

import (
	"log"
	"net/http"
	"time"
)

// SNSSAI is a minimal slice identifier for the AF request.
type SNSSAI struct {
	SST int32  `json:"sst"`
	SD  string `json:"sd,omitempty"`
}

// ARPConfig carries the ARP values the AF attaches to the requested QoS.
type ARPConfig struct {
	PriorityLevel uint8 `json:"priority"`
	PreemptCap    bool  `json:"preempt_cap"`
	PreemptVuln   bool  `json:"preempt_vuln"`
}

// Config is the AF enforcer configuration.
//
// PCFEndpoint is the full URL of the PCF PolicyAuthorization app-session
// collection, e.g. http://pcf.free5gc.org:8000/npcf-policyauthorization/v1/app-sessions.
//
// The enforcer emits the 3GPP AppSessionContext wrapper expected by the real
// free5gc PCF. The bundled mockpcf still models the older flat phase-2 payload
// and must be updated before it can validate this request in strict mode.
type Config struct {
	PCFEndpoint   string
	Timeout       time.Duration
	DefaultFiveQI uint8
	DefaultDNN    string
	DefaultSlice  SNSSAI
	ARP           ARPConfig
	SUPIMap       map[string]string // ue_ip -> supi
	HTTPClient    *http.Client
	EndGrace      time.Duration // extra grace before auto-terminating the app-session
	Logger        *log.Logger

	// NotifUri is the mandatory PCF notification callback URI (3GPP
	// AppSessionContextReqData.notifUri). The PCF POSTs QoS events here.
	// The QoS module does not yet serve this callback; set a placeholder
	// (create still succeeds, notifications are async).
	NotifUri string
	// SuppFeat is the mandatory hex feature bitmap (notifUri+suppFeat are the
	// two non-omitempty fields). "0" = no optional features.
	SuppFeat string
	// AfAppId identifies the AF application (free text).
	AfAppId string
}

func DefaultConfig() Config {
	return Config{
		Timeout:       5 * time.Second,
		DefaultFiveQI: 2,
		DefaultDNN:    "internet",
		DefaultSlice:  SNSSAI{SST: 1, SD: "000000"},
		ARP:           ARPConfig{PriorityLevel: 3, PreemptCap: true, PreemptVuln: false},
		EndGrace:      2 * time.Second,
		NotifUri:      "http://127.0.0.1:0/pcf-notif",
		SuppFeat:      "0",
		AfAppId:       "qos-module",
	}
}

// resolveSUPI returns the SUPI configured for the given UE IP, or empty.
func (c Config) resolveSUPI(ueIP string) string {
	if c.SUPIMap == nil {
		return ""
	}
	return c.SUPIMap[ueIP]
}
