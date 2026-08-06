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
// NOTE: the request body emitted by this enforcer is an intermediate AF JSON
// (see enforcer.go buildAppSessionBody). Adapting it to the exact 3GPP
// AppSessionContextReqData expected by a real free5gc PCF is the Phase 3 step
// documented in NGAP下发改造方案.md §8. Against the bundled mockpcf the
// intermediate format is sufficient.
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
}

func DefaultConfig() Config {
	return Config{
		Timeout:       5 * time.Second,
		DefaultFiveQI: 2,
		DefaultDNN:    "internet",
		DefaultSlice:  SNSSAI{SST: 1, SD: "000000"},
		ARP:           ARPConfig{PriorityLevel: 3, PreemptCap: true, PreemptVuln: false},
		EndGrace:      2 * time.Second,
	}
}

// resolveSUPI returns the SUPI configured for the given UE IP, or empty.
func (c Config) resolveSUPI(ueIP string) string {
	if c.SUPIMap == nil {
		return ""
	}
	return c.SUPIMap[ueIP]
}
