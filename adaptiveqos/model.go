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
