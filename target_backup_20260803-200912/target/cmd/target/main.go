package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	adaptiveqos "github.com/acore2026/adaptive-qos"
	target "masque-target"
)

func main() {
	var bind, prefix, mode, ranEndpoint string
	var bufferSize int
	var quiet bool
	var ranTimeout time.Duration
	var transitDelayRatio float64
	var defaultTransitDelay time.Duration

	flag.StringVar(&bind, "b", "0.0.0.0:7400", "bind UDP listen address")
	flag.StringVar(&mode, "mode", "qos", "server mode: qos or echo")
	flag.StringVar(&prefix, "prefix", "reply: ", "reply prefix")
	flag.StringVar(&ranEndpoint, "ran-url", "http://127.0.0.1:8080/api/v1/qos/update", "RAN QoS update endpoint")
	flag.DurationVar(&ranTimeout, "ran-timeout", 3*time.Second, "RAN API timeout")
	flag.Float64Var(&transitDelayRatio, "transit-ratio", 0.8, "fallback transit delay ratio of E2E delay")
	flag.DurationVar(&defaultTransitDelay, "default-transit-delay", 100*time.Millisecond, "fallback transit delay")
	flag.IntVar(&bufferSize, "buf", 64*1024, "UDP read buffer size")
	flag.BoolVar(&quiet, "quiet", false, "disable request logs")
	flag.Parse()

	var logger *log.Logger
	if !quiet {
		logger = target.NewLogger(os.Stdout)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var handler target.Handler
	switch mode {
	case "qos":
		policyCfg := adaptiveqos.DefaultBurstPolicyConfig()
		policyCfg.TransitDelayRatio = transitDelayRatio
		policyCfg.DefaultTransitDelayMS = uint64(defaultTransitDelay / time.Millisecond)
		qosHandler, err := target.NewQoSHandler(target.QoSConfig{
			RANEndpoint: ranEndpoint,
			RANTimeout:  ranTimeout,
			Policy:      policyCfg,
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
		Handler:        handler,
	})
	if err := server.Serve(ctx); err != nil {
		log.Fatalf("target server failed: %v", err)
	}
}
