package target

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"
)

type ReliabilityConfig struct {
	TTL             time.Duration
	MaxEntries      int
	MaxPayloadBytes int
	MaxRetries      int
	Timeout         time.Duration
}

type reliabilityConfigFile struct {
	TTL             string `json:"ttl"`
	MaxEntries      int    `json:"max_entries"`
	MaxPayloadBytes int    `json:"max_payload"`
	MaxRetries      *int   `json:"max_retries"`
	Timeout         string `json:"timeout"`
}

type reliableEnvelope struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
	Payload   []byte `json:"payload"`
}

type reliableCache struct {
	cfg     ReliabilityConfig
	mu      sync.Mutex
	entries map[string]cacheEntry
	order   []string
}

type cacheEntry struct {
	response  []byte
	expiresAt time.Time
}

func DefaultReliabilityConfig() ReliabilityConfig {
	return ReliabilityConfig{
		TTL:             2 * time.Minute,
		MaxEntries:      10000,
		MaxPayloadBytes: 64 * 1024,
		MaxRetries:      3,
		Timeout:         time.Second,
	}
}

func LoadReliabilityConfig(path string, cfg ReliabilityConfig) (ReliabilityConfig, error) {
	if path == "" {
		return cfg, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ReliabilityConfig{}, fmt.Errorf("read reliability config: %w", err)
	}
	var raw reliabilityConfigFile
	if err := json.Unmarshal(data, &raw); err != nil {
		return ReliabilityConfig{}, fmt.Errorf("parse reliability config: %w", err)
	}
	if raw.TTL != "" {
		ttl, err := time.ParseDuration(raw.TTL)
		if err != nil {
			return ReliabilityConfig{}, fmt.Errorf("invalid ttl: %w", err)
		}
		cfg.TTL = ttl
	}
	if raw.MaxEntries > 0 {
		cfg.MaxEntries = raw.MaxEntries
	}
	if raw.MaxPayloadBytes > 0 {
		cfg.MaxPayloadBytes = raw.MaxPayloadBytes
	}
	if raw.MaxRetries != nil {
		cfg.MaxRetries = *raw.MaxRetries
	}
	if raw.Timeout != "" {
		timeout, err := time.ParseDuration(raw.Timeout)
		if err != nil {
			return ReliabilityConfig{}, fmt.Errorf("invalid timeout: %w", err)
		}
		cfg.Timeout = timeout
	}
	if err := validateReliabilityConfig(cfg); err != nil {
		return ReliabilityConfig{}, err
	}
	return cfg, nil
}

func validateReliabilityConfig(cfg ReliabilityConfig) error {
	if cfg.TTL <= 0 {
		return errors.New("ttl must be positive")
	}
	if cfg.MaxEntries <= 0 {
		return errors.New("max_entries must be positive")
	}
	if cfg.MaxPayloadBytes <= 0 {
		return errors.New("max_payload must be positive")
	}
	if cfg.MaxRetries < 0 {
		return errors.New("max_retries must be zero or positive")
	}
	if cfg.Timeout <= 0 {
		return errors.New("timeout must be positive")
	}
	return nil
}

func newReliableCache(cfg ReliabilityConfig) *reliableCache {
	cfg = normalizeReliabilityConfig(cfg)
	if err := validateReliabilityConfig(cfg); err != nil {
		cfg = DefaultReliabilityConfig()
	}
	return &reliableCache{
		cfg:     cfg,
		entries: make(map[string]cacheEntry),
	}
}

func normalizeReliabilityConfig(cfg ReliabilityConfig) ReliabilityConfig {
	defaults := DefaultReliabilityConfig()
	if cfg.TTL == 0 {
		cfg.TTL = defaults.TTL
	}
	if cfg.MaxEntries == 0 {
		cfg.MaxEntries = defaults.MaxEntries
	}
	if cfg.MaxPayloadBytes == 0 {
		cfg.MaxPayloadBytes = defaults.MaxPayloadBytes
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = defaults.Timeout
	}
	return cfg
}

func (c *reliableCache) reply(requestID string, build func() ([]byte, error)) ([]byte, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	c.evictExpired(now)
	if entry, ok := c.entries[requestID]; ok && now.Before(entry.expiresAt) {
		return append([]byte(nil), entry.response...), true, nil
	}
	response, err := build()
	if err != nil {
		return nil, false, err
	}
	response = append([]byte(nil), response...)
	c.entries[requestID] = cacheEntry{
		response:  response,
		expiresAt: now.Add(c.cfg.TTL),
	}
	c.order = append(c.order, requestID)
	c.evictOverflow()
	return append([]byte(nil), response...), false, nil
}

func (c *reliableCache) evictExpired(now time.Time) {
	filtered := c.order[:0]
	for _, requestID := range c.order {
		entry, ok := c.entries[requestID]
		if !ok {
			continue
		}
		if now.After(entry.expiresAt) {
			delete(c.entries, requestID)
			continue
		}
		filtered = append(filtered, requestID)
	}
	c.order = filtered
}

func (c *reliableCache) evictOverflow() {
	for len(c.entries) > c.cfg.MaxEntries && len(c.order) > 0 {
		requestID := c.order[0]
		c.order = c.order[1:]
		delete(c.entries, requestID)
	}
}

func encodeReliableRequest(requestID string, payload []byte) ([]byte, error) {
	return json.Marshal(reliableEnvelope{
		Type:      "request",
		RequestID: requestID,
		Payload:   payload,
	})
}

func decodeReliableRequest(data []byte) (string, []byte, bool) {
	var env reliableEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return "", nil, false
	}
	if env.Type != "request" || env.RequestID == "" {
		return "", nil, false
	}
	return env.RequestID, append([]byte(nil), env.Payload...), true
}

func encodeReliableResponse(requestID string, payload []byte) ([]byte, error) {
	return json.Marshal(reliableEnvelope{
		Type:      "response",
		RequestID: requestID,
		Payload:   payload,
	})
}

func decodeReliableResponse(data []byte) (string, []byte, bool) {
	var env reliableEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return "", nil, false
	}
	if env.Type != "response" || env.RequestID == "" {
		return "", nil, false
	}
	return env.RequestID, append([]byte(nil), env.Payload...), true
}
