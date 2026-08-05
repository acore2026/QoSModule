# adaptive-qos

`adaptive-qos` is a transport-neutral QoS policy module shared by the standalone
MASQUE backend and the free6gc adapter.

The root package contains the canonical intent, limits, policy, decision and
enforcement interfaces. It does not depend on MASQUE, UDP, free6gc or a
particular RAN implementation.

- `masqueapi` adapts the current project's JSON request into a canonical intent.
- `ranapi` adapts a canonical decision to `POST /api/v1/qos/update`.

Project-specific integrations should translate their wire format at the edge
and implement `LimitsProvider` or `Enforcer` when their resource model differs.
