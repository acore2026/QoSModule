package adaptiveqos

import "context"

const (
	StatusAccepted = "ACCEPTED"
	StatusRejected = "REJECTED"
)

type FlowSelector struct {
	RNTI      uint32
	QFI       uint8
	UEAddress string
	SEID      uint64
}

// FlowFilter carries the service data flow 5-tuple for the AF/PCF path.
// It mirrors masqueapi.PacketFilter but lives in the model package to avoid
// an import cycle (masqueapi imports adaptiveqos).
type FlowFilter struct {
	SrcIP    string
	DstIP    string
	SrcPort  uint16
	DstPort  uint16
	Protocol uint8
}

func (f FlowFilter) Present() bool {
	return f.SrcIP != "" || f.DstIP != "" || f.SrcPort != 0 || f.DstPort != 0 || f.Protocol != 0
}

type BurstDemand struct {
	SizeKB     uint64
	DurationMS uint64
}

func (b BurstDemand) Present() bool {
	return b.SizeKB > 0 || b.DurationMS > 0
}

func (b BurstDemand) Complete() bool {
	return b.SizeKB > 0 && b.DurationMS > 0
}

type Intent struct {
	RequestID string
	Flow      FlowSelector

	ULBurst BurstDemand
	DLBurst BurstDemand

	E2EDelayMS       uint64
	ULTransitDelayMS uint64
	DLTransitDelayMS uint64

	// Filter carries the SDF 5-tuple for the AF/PCF path (may be zero value
	// when the upstream request does not include a packet filter).
	Filter FlowFilter
	// ServiceType is the upstream service hint (e.g. "voice", "video"); used
	// by enforcers to derive 5QI when the policy does not carry one.
	ServiceType string
}

type Range struct {
	Min uint64
	Max uint64
}

func (r Range) Clip(value uint64) uint64 {
	if r.Min > 0 && value < r.Min {
		return r.Min
	}
	if r.Max > 0 && value > r.Max {
		return r.Max
	}
	return value
}

type Limits struct {
	MBRUL    Range
	MBRDL    Range
	GBRUL    Range
	GBRDL    Range
	PDB      Range
	Priority Range
}

func DefaultRANLimits() Limits {
	return Limits{
		MBRUL:    Range{Max: 4_000_000_000},
		MBRDL:    Range{Max: 4_000_000_000},
		GBRUL:    Range{Max: 100_000},
		GBRDL:    Range{Max: 100_000},
		PDB:      Range{Min: 10, Max: 300},
		Priority: Range{Min: 1, Max: 15},
	}
}

type QoSValues struct {
	MBRULKbps uint64
	MBRDLKbps uint64
	GBRULKbps uint64
	GBRDLKbps uint64
	PDBMS     uint64
	Priority  uint8
}

type Calculation struct {
	Target           QoSValues
	ULTransitDelayMS uint64
	DLTransitDelayMS uint64
}

type Decision struct {
	QoSValues
	Calculation Calculation
}

type Policy interface {
	Generate(context.Context, Intent, Limits) (Decision, error)
}

type LimitsProvider interface {
	Limits(context.Context, FlowSelector) (Limits, error)
}

type Enforcer interface {
	Apply(context.Context, Intent, Decision) (ApplyResult, error)
}

type ApplyResult struct {
	Status      string
	ErrorCode   string
	Message     string
	HTTPStatus  int
	RawResponse []byte
}

type Outcome struct {
	Intent   Intent
	Decision Decision
	Apply    ApplyResult
}

type StaticLimits struct {
	Value Limits
}

func (s StaticLimits) Limits(context.Context, FlowSelector) (Limits, error) {
	return s.Value, nil
}
