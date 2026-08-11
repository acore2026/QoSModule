#!/bin/bash
set -e

# QoS 模块启动脚本 — 支持三种下发模式
# 用法: ./start-qos.sh <mode> [options]
# mode: ran | ran-udp | ngap | auto
#
# 环境变量(可选,覆盖默认值):
#   QOS_BIND          UDP 监听地址(默认 0.0.0.0:7400)
#   RAN_URL           gNB HTTP 端点(mode=ran/auto)
#   RAN_UDP_ENDPOINT  gNB UDP 端点(mode=ran-udp/auto)
#   RAN_UDP_ACK       gNB UDP 是否回应答(1/0)
#   PCF_ENDPOINT      PCF PolicyAuthorization URL(mode=ngap/auto)
#   SUPI_MAP          静态 UE IP→SUPI 映射(mode=ngap),如 10.60.0.1=imsi-001
#   DEFAULT_5QI       默认 5QI(mode=ngap,默认 2)
#   DEFAULT_DNN       默认 DNN(mode=ngap,默认 internet)

RED='\033[0;31m'; GREEN='\033[0;32m'; BLUE='\033[0;34m'; NC='\033[0m'
ok()   { echo -e "${GREEN}  ✓ $1${NC}"; }
info() { echo -e "${BLUE}  ℹ $1${NC}"; }
fail() { echo -e "${RED}  ✗ $1${NC}"; exit 1; }

MODE="${1:-ran}"
BIND="${QOS_BIND:-0.0.0.0:7400}"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
TARGET_DIR="$SCRIPT_DIR/../target/target"
BINARY="$TARGET_DIR/target"

echo "=========================================="
echo "  QoS 模块启动 (mode=$MODE)"
echo "=========================================="

# 编译(如果二进制不存在)
if [ ! -f "$BINARY" ]; then
    info "编译 target 二进制..."
    (cd "$TARGET_DIR" && GOPROXY=https://goproxy.cn,direct go build -o target ./cmd/target)
    ok "编译完成"
fi

# 公共 flag
COMMON_FLAGS="-mode qos -b $BIND"
COMMON_FLAGS="$COMMON_FLAGS -transit-ratio 0.8 -default-transit-delay 100ms"
COMMON_FLAGS="$COMMON_FLAGS -dl-max-mcs 28 -ul-max-mcs 28 -dl-max-rb 273 -ul-max-rb 273"
COMMON_FLAGS="$COMMON_FLAGS -dl-bler-upper 0.01 -ul-bler-upper 0.01 -dl-smooth 0.5 -ul-smooth 0.5"
COMMON_FLAGS="$COMMON_FLAGS -q-cap 1 -q-vul 0"

case "$MODE" in
  ran)
    RAN_URL_VAL="${RAN_URL:-http://127.0.0.1:8080/api/v1/qos/update}"
    info "mode=ran(HTTP 直连 gNB): $RAN_URL_VAL"
    exec "$BINARY" $COMMON_FLAGS \
      -core-mode ran \
      -ran-url "$RAN_URL_VAL" \
      -ran-timeout 3s
    ;;
  ran-udp)
    UDP_EP="${RAN_UDP_ENDPOINT:-127.0.0.1:54003}"
    UDP_ACK="${RAN_UDP_ACK:-0}"
    info "mode=ran-udp(UDP 直连 gNB): $UDP_EP (ack=$UDP_ACK)"
    exec "$BINARY" $COMMON_FLAGS \
      -core-mode ran-udp \
      -ran-udp-endpoint "$UDP_EP" \
      -ran-udp-ack=$UDP_ACK \
      -ran-timeout 3s
    ;;
  ngap)
    PCF_VAL="${PCF_ENDPOINT:-}"
    SUPI_VAL="${SUPI_MAP:-}"
    FIVEQI="${DEFAULT_5QI:-2}"
    DNN_VAL="${DEFAULT_DNN:-internet}"
    if [ -z "$PCF_VAL" ] && [ -n "$SMF_ENDPOINT" ]; then
      # 方案 A:直连 SMF 的 /qos-update 端点(不经 PCF)
      info "mode=ngap(SMF 直连 方案A): $SMF_ENDPOINT"
      info "注意: 方案A 使用 SMF 的 /nsmf-oam/v1/qos-update,不走 PCF"
      info "需要 fork SMF 镜像(free5gc/smf:fork)已部署"
      # 方案A 的 enforcer 调 SMF,当前通过 afenforcer 发 PCF 格式或直调 SMF
      # 若用 SMF 直连,设 PCF_ENDPOINT 为 SMF 的 /nsmf-oam/v1/qos-update
      PCF_VAL="$SMF_ENDPOINT/nsmf-oam/v1/qos-update"
    fi
    [ -z "$PCF_VAL" ] && fail "ngap 模式需要 PCF_ENDPOINT 或 SMF_ENDPOINT 环境变量"
    info "mode=ngap(经核心网 NGAP): $PCF_VAL"
    [ -n "$SUPI_VAL" ] && info "SUPI map: $SUPI_VAL"
    exec "$BINARY" $COMMON_FLAGS \
      -core-mode ngap \
      -pcf-endpoint "$PCF_VAL" \
      -supi-map "$SUPI_VAL" \
      -default-5qi "$FIVEQI" \
      -default-dnn "$DNN_VAL" \
      -arp-priority 8 -arp-preempt-cap 1 -arp-preempt-vuln 0
    ;;
  auto)
    RAN_URL_VAL="${RAN_URL:-http://127.0.0.1:8080/api/v1/qos/update}"
    UDP_EP="${RAN_UDP_ENDPOINT:-}"
    PCF_VAL="${PCF_ENDPOINT:-}"
    info "mode=auto(HTTP → UDP → NGAP 依次试)"
    [ -n "$UDP_EP" ] && info "  UDP fallback: $UDP_EP"
    [ -n "$PCF_VAL" ] && info "  NGAP fallback: $PCF_VAL"
    AUTO_FLAGS="$COMMON_FLAGS -core-mode auto -ran-url $RAN_URL_VAL"
    [ -n "$UDP_EP" ] && AUTO_FLAGS="$AUTO_FLAGS -ran-udp-endpoint $UDP_EP"
    [ -n "$PCF_VAL" ] && AUTO_FLAGS="$AUTO_FLAGS -pcf-endpoint $PCF_VAL"
    exec "$BINARY" $AUTO_FLAGS
    ;;
  *)
    echo "用法: $0 <ran|ran-udp|ngap|auto>"
    echo ""
    echo "  ran      — HTTP 直连 gNB (POST /api/v1/qos/update)"
    echo "  ran-udp  — UDP 直连 gNB (同 JSON,走 UDP)"
    echo "  ngap     — 经核心网 NGAP (PCF 或 SMF /qos-update)"
    echo "  auto     — HTTP → UDP → NGAP 依次回退"
    echo ""
    echo "环境变量:"
    echo "  RAN_URL, RAN_UDP_ENDPOINT, RAN_UDP_ACK"
    echo "  PCF_ENDPOINT, SMF_ENDPOINT, SUPI_MAP, DEFAULT_5QI, DEFAULT_DNN"
    exit 1
    ;;
esac
