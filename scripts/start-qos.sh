#!/bin/bash
set -e

# ============================================================
#  QoS 模块启动脚本 — 支持三种下发模式
#  用法: ./start-qos.sh <ran|ran-udp|ngap|auto>
#
#  地址默认值已填入(改这里即可,无需每次传环境变量)
#  环境变量仍可覆盖默认值(如: SMF_ENDPOINT=http://x:8000 ./start-qos.sh ngap)
#
#  注意: 核心网容器 IP(10.100.200.x)在 restart-all 后可能变化,
#        若连不上,运行 docker inspect <nf> --format '{{...IPAddress}}' 查新 IP
# ============================================================

# ============================================================
#  默认地址(按需修改这里)
#
#  各模式需要的 IP:
#    ran      → QOS_BIND + RAN_URL
#    ran-udp  → QOS_BIND + RAN_UDP_ENDPOINT (+ RAN_UDP_ACK)
#    ngap     → QOS_BIND + SMF_IP + SUPI_MAP (+ DEFAULT_5QI/DNN)
#    auto     → 以上全部(HTTP→UDP→NGAP 依次回退)
# ============================================================

# ---- 公共(所有模式) ----
QOS_BIND="${QOS_BIND:-0.0.0.0:7400}"           # QoS 模块 UDP 监听(收 MASQUE 请求)

# ---- mode=ran(HTTP 直连 gNB) ----
RAN_URL="${RAN_URL:-http://10.88.120.212:80/api/v1/qos/update}"

# ---- mode=ran-udp(UDP 直连 gNB) ----
RAN_UDP_ENDPOINT="${RAN_UDP_ENDPOINT:-10.88.0.3:9999}"  # gNB UDP 地址
RAN_UDP_ACK="${RAN_UDP_ACK:-0}"                          # gNB 是否回应答(0=不等,1=等)

# ---- mode=ngap(经核心网,以下才需要) ----
SMF_IP="${SMF_IP:-10.100.200.5}"          # SMF 容器 IP;restart-all 后可能变(docker inspect smf 查)
SMF_ENDPOINT="${SMF_ENDPOINT:-http://${SMF_IP}:8000}"
PCF_IP="${PCF_IP:-10.100.200.12}"          # PCF 容器 IP(方案B 才用)
PCF_ENDPOINT="${PCF_ENDPOINT:-http://${PCF_IP}:8000/npcf-policyauthorization/v1/app-sessions}"
SUPI_MAP="${SUPI_MAP:-10.60.0.1=imsi-001012345678903}"   # UE IP→SUPI(ngap 才需要)
DEFAULT_5QI="${DEFAULT_5QI:-2}"
DEFAULT_DNN="${DEFAULT_DNN:-internet}"
# ---- 默认地址结束 ----

RED='\033[0;31m'; GREEN='\033[0;32m'; BLUE='\033[0;34m'; NC='\033[0m'
ok()   { echo -e "${GREEN}  ✓ $1${NC}"; }
info() { echo -e "${BLUE}  ℹ $1${NC}"; }
fail() { echo -e "${RED}  ✗ $1${NC}"; exit 1; }

MODE="${1:-ran}"
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

# 公共 flag(RAN 调度默认值)
COMMON_FLAGS="-mode qos -b $QOS_BIND"
COMMON_FLAGS="$COMMON_FLAGS -transit-ratio 0.8 -default-transit-delay 100ms"
COMMON_FLAGS="$COMMON_FLAGS -dl-max-mcs 28 -ul-max-mcs 28 -dl-max-rb 273 -ul-max-rb 273"
COMMON_FLAGS="$COMMON_FLAGS -dl-bler-upper 0.01 -ul-bler-upper 0.01 -dl-smooth 0.5 -ul-smooth 0.5"
COMMON_FLAGS="$COMMON_FLAGS -q-cap 1 -q-vul 0"

case "$MODE" in
  ran)
    info "mode=ran(HTTP 直连 gNB): $RAN_URL"
    exec "$BINARY" $COMMON_FLAGS \
      -core-mode ran \
      -ran-url "$RAN_URL" \
      -ran-timeout 3s
    ;;

  ran-udp)
    info "mode=ran-udp(UDP 直连 gNB): $RAN_UDP_ENDPOINT (ack=$RAN_UDP_ACK)"
    exec "$BINARY" $COMMON_FLAGS \
      -core-mode ran-udp \
      -ran-udp-endpoint "$RAN_UDP_ENDPOINT" \
      -ran-udp-ack=$RAN_UDP_ACK \
      -ran-timeout 3s
    ;;

  ngap)
    # 方案A:直连 SMF 的 /nsmf-oam/v1/qos-update(需 fork SMF 镜像已部署)
    NGAP_URL="$SMF_ENDPOINT/nsmf-oam/v1/qos-update"
    info "mode=ngap(方案A SMF 直连): $NGAP_URL"
    info "  supi_map=$SUPI_MAP  5qi=$DEFAULT_5QI  dnn=$DEFAULT_DNN"
    exec "$BINARY" $COMMON_FLAGS \
      -core-mode ngap \
      -pcf-endpoint "$NGAP_URL" \
      -supi-map "$SUPI_MAP" \
      -default-5qi "$DEFAULT_5QI" \
      -default-dnn "$DEFAULT_DNN" \
      -arp-priority 8 -arp-preempt-cap 1 -arp-preempt-vuln 0
    ;;

  auto)
    info "mode=auto(HTTP → UDP → NGAP 依次回退)"
    info "  HTTP: $RAN_URL"
    info "  UDP:  $RAN_UDP_ENDPOINT"
    info "  NGAP: $SMF_ENDPOINT/nsmf-oam/v1/qos-update"
    exec "$BINARY" $COMMON_FLAGS \
      -core-mode auto \
      -ran-url "$RAN_URL" \
      -ran-udp-endpoint "$RAN_UDP_ENDPOINT" \
      -ran-udp-ack=$RAN_UDP_ACK \
      -pcf-endpoint "$SMF_ENDPOINT/nsmf-oam/v1/qos-update" \
      -supi-map "$SUPI_MAP" \
      -default-5qi "$DEFAULT_5QI" \
      -default-dnn "$DEFAULT_DNN"
    ;;

  *)
    echo "用法: $0 <ran|ran-udp|ngap|auto>"
    echo ""
    echo "  ran      — HTTP 直连 gNB (POST /api/v1/qos/update)"
    echo "  ran-udp  — UDP 直连 gNB (同 JSON,走 UDP)"
    echo "  ngap     — 经核心网 NGAP (SMF /qos-update → AMF → gNB)"
    echo "  auto     — HTTP → UDP → NGAP 依次回退"
    echo ""
    echo "默认地址(改脚本顶部的变量,或用环境变量覆盖):"
    echo "  gNB HTTP:  $RAN_URL"
    echo "  gNB UDP:   $RAN_UDP_ENDPOINT"
    echo "  SMF:       $SMF_ENDPOINT"
    echo "  PCF:       $PCF_ENDPOINT"
    echo "  UE 映射:   $SUPI_MAP"
    exit 1
    ;;
esac
