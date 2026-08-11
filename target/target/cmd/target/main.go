package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	adaptiveqos "github.com/acore2026/adaptive-qos"
	"github.com/acore2026/adaptive-qos/afenforcer"
	"github.com/acore2026/adaptive-qos/ranapi"
	"github.com/acore2026/adaptive-qos/udpranenforcer"
	target "masque-target"
)

func main() {
	var bind, prefix, configPath, mode, ranEndpoint string
	var bufferSize int
	var quiet bool
	var ranTimeout time.Duration
	var transitDelayRatio float64
	var defaultTransitDelay time.Duration
	var ranMask string
	var qType, qCap, qVul uint
	var dlMaxMCS, ulMaxMCS, dlMaxRB, ulMaxRB uint
	var dlBLERUpper, ulBLERUpper, dlSmooth, ulSmooth float64
	var coreMode, pcfEndpoint, supiMap, defaultDNN string
	var defaultFiveQI, arpPriority uint
	var arpPreemptCap, arpPreemptVuln uint
	var ranUDPEndpoint string
	var ranUDPAck bool

	flag.StringVar(&configPath, "config", "", "reliability config JSON file")
	flag.StringVar(&bind, "b", "0.0.0.0:7400", "bind UDP listen address")
	flag.StringVar(&mode, "mode", "qos", "server mode: qos or echo")
	flag.StringVar(&prefix, "prefix", "reply: ", "reply prefix")
	flag.StringVar(&ranEndpoint, "ran-url", "http://127.0.0.1:8080/api/v1/qos/update", "RAN QoS update endpoint")
	flag.DurationVar(&ranTimeout, "ran-timeout", 3*time.Second, "RAN API timeout")
	flag.Float64Var(&transitDelayRatio, "transit-ratio", 0.8, "fallback transit delay ratio of E2E delay")
	flag.DurationVar(&defaultTransitDelay, "default-transit-delay", 100*time.Millisecond, "fallback transit delay")
	flag.IntVar(&bufferSize, "buf", 64*1024, "UDP read buffer size")
	flag.BoolVar(&quiet, "quiet", false, "disable request logs")
	flag.StringVar(&ranMask, "ran-mask", "auto", "RAN API mask value, or auto to derive it from emitted fields")
	flag.UintVar(&qType, "q-type", 0, "RAN QoS flow type")
	flag.UintVar(&qCap, "q-cap", 1, "RAN pre-emption capability")
	flag.UintVar(&qVul, "q-vul", 0, "RAN pre-emption vulnerability")
	flag.UintVar(&dlMaxMCS, "dl-max-mcs", 28, "RAN DL max MCS")
	flag.UintVar(&ulMaxMCS, "ul-max-mcs", 28, "RAN UL max MCS")
	flag.UintVar(&dlMaxRB, "dl-max-rb", 273, "RAN DL max RB")
	flag.UintVar(&ulMaxRB, "ul-max-rb", 273, "RAN UL max RB")
	flag.Float64Var(&dlBLERUpper, "dl-bler-upper", 0.01, "RAN DL BLER upper")
	flag.Float64Var(&ulBLERUpper, "ul-bler-upper", 0.01, "RAN UL BLER upper")
	flag.Float64Var(&dlSmooth, "dl-smooth", 0.5, "RAN DL smooth alpha")
	flag.Float64Var(&ulSmooth, "ul-smooth", 0.5, "RAN UL smooth alpha")
	flag.StringVar(&coreMode, "core-mode", "ran", "core enforcement mode: ran|ngap|auto")
	flag.StringVar(&pcfEndpoint, "pcf-endpoint", "", "PCF PolicyAuthorization app-session URL (for ngap/auto mode), e.g. http://pcf.free5gc.org:8000/npcf-policyauthorization/v1/app-sessions")
	flag.StringVar(&supiMap, "supi-map", "", "static UE IP→SUPI map, comma-separated (e.g. 10.60.0.2=imsi-001012345678903)")
	flag.StringVar(&defaultDNN, "default-dnn", "internet", "default DNN for AF app-session")
	flag.UintVar(&defaultFiveQI, "default-5qi", 2, "default 5QI for burst GBR flow")
	flag.UintVar(&arpPriority, "arp-priority", 3, "ARP priority level")
	flag.UintVar(&arpPreemptCap, "arp-preempt-cap", 1, "ARP pre-emption capability (1=may preempt)")
	flag.UintVar(&arpPreemptVuln, "arp-preempt-vuln", 0, "ARP pre-emption vulnerability (1=preemptable)")
	flag.StringVar(&ranUDPEndpoint, "ran-udp-endpoint", "", "gNB UDP QoS endpoint (for ran-udp mode), e.g. 10.88.120.212:54003")
	flag.BoolVar(&ranUDPAck, "ran-udp-ack", false, "whether the gNB UDP interface returns a reply")
	flag.Parse()

	var logger *log.Logger
	if !quiet {
		logger = target.NewLogger(os.Stdout)
	}
	reliability, err := target.LoadReliabilityConfig(configPath, target.DefaultReliabilityConfig())
	if err != nil {
		log.Fatalf("failed to load reliability config: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var handler target.Handler
	switch mode {
	case "qos":
		ranDefaults, err := buildRANDefaults(ranMask, qType, qCap, qVul, dlMaxMCS, ulMaxMCS, dlMaxRB, ulMaxRB, dlBLERUpper, ulBLERUpper, dlSmooth, ulSmooth)
		if err != nil {
			log.Fatalf("configure RAN defaults: %v", err)
		}
		policyCfg := adaptiveqos.DefaultBurstPolicyConfig()
		policyCfg.TransitDelayRatio = transitDelayRatio
		policyCfg.DefaultTransitDelayMS = uint64(defaultTransitDelay / time.Millisecond)

		afCfg := afenforcer.DefaultConfig()
		if pcfEndpoint != "" {
			afCfg.PCFEndpoint = pcfEndpoint
		}
		afCfg.DefaultFiveQI = uint8(defaultFiveQI)
		afCfg.DefaultDNN = defaultDNN
		if arpPriority > 15 {
			log.Fatalf("arp-priority must be in [0,15]")
		}
		if arpPreemptCap > 1 || arpPreemptVuln > 1 {
			log.Fatalf("arp-preempt-cap and arp-preempt-vuln must be 0 or 1")
		}
		afCfg.ARP = afenforcer.ARPConfig{
			PriorityLevel: uint8(arpPriority),
			PreemptCap:    arpPreemptCap == 1,
			PreemptVuln:   arpPreemptVuln == 1,
		}
		if supiMap != "" {
			afCfg.SUPIMap = parseSUPIMap(supiMap)
		}

		udpRanCfg := udpranenforcer.Config{
			Endpoint:   ranUDPEndpoint,
			Timeout:    ranTimeout,
			Defaults:   ranDefaults,
			WaitForAck: ranUDPAck,
		}

		qosHandler, err := target.NewQoSHandler(target.QoSConfig{
			RANEndpoint: ranEndpoint,
			RANTimeout:  ranTimeout,
			Policy:      policyCfg,
			RANDefaults: ranDefaults,
			Logger:      logger,
			CoreMode:    coreMode,
			AFConfig:    afCfg,
			UDPRANConfig: udpRanCfg,
		})
		if err != nil {
			log.Fatalf("configure QoS handler: %v", err)
		}
		handler = qosHandler
	case "echo":
	default:
		log.Fatalf("unsupported mode %q", mode)
	}

	server := target.NewServer(target.Config{
		ListenAddr:     bind,
		ReplyPrefix:    prefix,
		ReadBufferSize: bufferSize,
		Logger:         logger,
		Reliability:    reliability,
		Handler:        handler,
	})
	if err := server.Serve(ctx); err != nil {
		log.Fatalf("target server failed: %v", err)
	}
}

func buildRANDefaults(
	mask string,
	qType, qCap, qVul uint,
	dlMaxMCS, ulMaxMCS, dlMaxRB, ulMaxRB uint,
	dlBLERUpper, ulBLERUpper, dlSmooth, ulSmooth float64,
) (ranapi.RequestDefaults, error) {
	defaults := ranapi.DefaultRequestDefaults()
	switch mask {
	case "", "auto":
	default:
		value, err := strconv.ParseUint(mask, 10, 32)
		if err != nil {
			return defaults, fmt.Errorf("ran-mask must be auto or uint32: %w", err)
		}
		defaults.Mask = uint32(value)
		defaults.StaticMask = true
	}
	if qType > 1 {
		return defaults, errors.New("q-type must be in [0,1]")
	}
	if qCap > 1 {
		return defaults, errors.New("q-cap must be in [0,1]")
	}
	if qVul > 1 {
		return defaults, errors.New("q-vul must be in [0,1]")
	}
	if dlMaxMCS > 28 || ulMaxMCS > 28 {
		return defaults, errors.New("max MCS must be in [0,28]")
	}
	if dlMaxRB > 273 || ulMaxRB > 273 {
		return defaults, errors.New("max RB must be in [0,273]")
	}
	if !unitInterval(dlBLERUpper) || !unitInterval(ulBLERUpper) || !unitInterval(dlSmooth) || !unitInterval(ulSmooth) {
		return defaults, errors.New("BLER and smooth values must be in [0,1]")
	}
	defaults.QType = uint8(qType)
	defaults.QCap = uint8(qCap)
	defaults.QVul = uint8(qVul)
	defaults.DLMaxMCS = uint8(dlMaxMCS)
	defaults.ULMaxMCS = uint8(ulMaxMCS)
	defaults.DLMaxRB = uint16(dlMaxRB)
	defaults.ULMaxRB = uint16(ulMaxRB)
	defaults.DLBLERUpper = dlBLERUpper
	defaults.ULBLERUpper = ulBLERUpper
	defaults.DLSmooth = dlSmooth
	defaults.ULSmooth = ulSmooth
	return defaults, nil
}

func unitInterval(value float64) bool {
	return value >= 0 && value <= 1
}

// parseSUPIMap parses a "ip=supi,ip=supi" string into a map.
func parseSUPIMap(raw string) map[string]string {
	out := make(map[string]string)
	for _, pair := range strings.Split(raw, ",") {
		kv := strings.SplitN(strings.TrimSpace(pair), "=", 2)
		if len(kv) != 2 {
			continue
		}
		k := strings.TrimSpace(kv[0])
		v := strings.TrimSpace(kv[1])
		if k != "" && v != "" {
			out[k] = v
		}
	}
	return out
}
