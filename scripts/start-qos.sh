#!/bin/bash
set -e

# ============================================================
#  QoS 模块管理脚本 — 启动/停止/状态
#
#  用法:
#    ./start-qos.sh <ran|ran-udp|ngap|auto>   启动(自动后台)
#    ./start-qos.sh stop                       停止
#    ./start-qos.sh status                     查看状态
#    ./start-qos.sh restart <mode>             重启
#
#  地址默认值已填入(改脚本顶部即可)
#  环境变量仍可覆盖(如: SMF_IP=10.x.x.x ./start-qos.sh ngap)
#
#  注意: 核心网容器 IP(10.100.200.x)在 restart-all 后可能变化,
#        docker inspect <nf> --format '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' 查新 IP
# ============================================================

# ============================================================
#  默认地址(按需修改这里)
#
#  各模式需要的 IP:
#    ran      → QOS_BIND + RAN_URL
#    ran-udp  → QOS_BIND + RAN_UDP_ENDPOINT (+ RAN_UDP_ACK)
#    ngap     → QOS_BIND + SMF_ENDPOINT (方案A SMF /qos-update)
#    auto     → 以上全部(HTTP→UDP→SMF 依次回退)
# ============================================================

# ---- 公共(所有模式) ----
QOS_BIND="${QOS_BIND:-0.0.0.0:7400}"           # QoS 模块 UDP 监听(收 MASQUE 请求)

# ---- mode=ran(HTTP 直连 gNB) ----
RAN_URL="${RAN_URL:-http://10.88.120.212:80/api/v1/qos/update}"

# ---- mode=ran-udp(UDP 直连 gNB) ----
RAN_UDP_ENDPOINT="${RAN_UDP_ENDPOINT:-10.88.0.3:9999}"  # gNB UDP 地址
RAN_UDP_ACK="${RAN_UDP_ACK:-1}"                          # gNB 是否回应答(0=不等,1=等)

# ---- mode=ngap(经核心网,以下才需要) ----
SMF_IP="${SMF_IP:-10.100.200.8}"          # SMF 容器 IP(已在 docker-compose 固定)
SMF_ENDPOINT="${SMF_ENDPOINT:-http://${SMF_IP}:8000}"
DEFAULT_5QI="${DEFAULT_5QI:-2}"
# ---- 默认地址结束 ----

# ---- 运行时文件 ----
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
TARGET_DIR="$SCRIPT_DIR/../target/target"
BINARY="$TARGET_DIR/target"
PID_FILE="/tmp/qos-module.pid"
LOG_FILE="$SCRIPT_DIR/../logs/qos-module.log"

RED='\033[0;31m'; GREEN='\033[0;32m'; BLUE='\033[0;34m'; YELLOW='\033[1;33m'; NC='\033[0m'
ok()   { echo -e "${GREEN}  ✓ $1${NC}"; }
info() { echo -e "${BLUE}  ℹ $1${NC}"; }
warn() { echo -e "${YELLOW}  ⚠ $1${NC}"; }
fail() { echo -e "${RED}  ✗ $1${NC}"; exit 1; }

# ---- 子命令: stop / status ----
CMD="${1:-}"

case "$CMD" in
  stop)
    if [ -f "$PID_FILE" ]; then
      PID=$(cat "$PID_FILE")
      if kill -0 "$PID" 2>/dev/null; then
        kill "$PID"
        sleep 1
        ok "QoS 模块已停止 (pid=$PID)"
      else
        warn "PID $PID 已不存在,清理 pid 文件"
      fi
      rm -f "$PID_FILE"
    else
      # 兜底:按进程名找
      PIDS=$(pgrep -f "target.*-mode qos" 2>/dev/null || true)
      if [ -n "$PIDS" ]; then
        kill $PIDS 2>/dev/null || true
        sleep 1
        ok "QoS 模块已停止 (pgrep 兜底: $PIDS)"
      else
        warn "未找到运行中的 QoS 模块"
      fi
    fi
    exit 0
    ;;

  status)
    if [ -f "$PID_FILE" ]; then
      PID=$(cat "$PID_FILE")
      if kill -0 "$PID" 2>/dev/null; then
        ok "QoS 模块运行中 (pid=$PID)"
        info "日志: tail -f $LOG_FILE"
        exit 0
      fi
    fi
    PIDS=$(pgrep -f "target.*-mode qos" 2>/dev/null || true)
    if [ -n "$PIDS" ]; then
      ok "QoS 模块运行中 (pgrep: $PIDS)"
    else
      warn "QoS 模块未运行"
    fi
    exit 0
    ;;

  restart)
    MODE="${2:-ran-udp}"
    info "重启中..."
    "$0" stop 2>/dev/null || true
    sleep 1
    exec "$0" "$MODE"
    ;;
esac

# ---- 启动逻辑 ----
MODE="$CMD"

# 编译(如果二进制不存在)
if [ ! -f "$BINARY" ]; then
    info "编译 target 二进制..."
    (cd "$TARGET_DIR" && GOPROXY=https://goproxy.cn,direct go build -o target ./cmd/target)
    ok "编译完成"
fi

# 检查是否已在运行
if [ -f "$PID_FILE" ]; then
  PID=$(cat "$PID_FILE")
  if kill -0 "$PID" 2>/dev/null; then
    fail "QoS 模块已在运行 (pid=$PID),先执行: $0 stop"
  fi
  rm -f "$PID_FILE"
fi

# 公共 flag
COMMON_FLAGS="-mode qos -b $QOS_BIND"
COMMON_FLAGS="$COMMON_FLAGS -transit-ratio 0.8 -default-transit-delay 100ms"
COMMON_FLAGS="$COMMON_FLAGS -dl-max-mcs 28 -ul-max-mcs 28 -dl-max-rb 273 -ul-max-rb 273"
COMMON_FLAGS="$COMMON_FLAGS -dl-bler-upper 0.01 -ul-bler-upper 0.01 -dl-smooth 0.5 -ul-smooth 0.5"
COMMON_FLAGS="$COMMON_FLAGS -q-cap 1 -q-vul 0"

case "$MODE" in
  ran)
    info "mode=ran(HTTP 直连 gNB): $RAN_URL"
    RUN_FLAGS="$COMMON_FLAGS -core-mode ran -ran-url $RAN_URL -ran-timeout 3s"
    ;;

  ran-udp)
    info "mode=ran-udp(UDP 直连 gNB): $RAN_UDP_ENDPOINT (ack=$RAN_UDP_ACK)"
    RUN_FLAGS="$COMMON_FLAGS -core-mode ran-udp -ran-udp-endpoint $RAN_UDP_ENDPOINT -ran-udp-ack=$RAN_UDP_ACK -ran-timeout 3s"
    ;;

  ngap)
    SMF_OAM_URL="$SMF_ENDPOINT/nsmf-oam/v1/qos-update"
    info "mode=ngap(方案A SMF 直连): $SMF_OAM_URL"
    info "  5qi=$DEFAULT_5QI  arp_priority=8"
    RUN_FLAGS="$COMMON_FLAGS -core-mode ngap -smf-endpoint $SMF_OAM_URL -default-5qi $DEFAULT_5QI -arp-priority 8 -arp-preempt-cap 1 -arp-preempt-vuln 0"
    ;;

  auto)
    SMF_OAM_URL="$SMF_ENDPOINT/nsmf-oam/v1/qos-update"
    MOCK_RAN_URL="${MOCK_RAN_URL:-http://127.0.0.1:18081/api/v1/qos/update}"
    info "mode=auto(UDP → mock-ran → SMF 三档回退)"
    info "  UDP:      $RAN_UDP_ENDPOINT (ack=$RAN_UDP_ACK, 需开 ack 才能因无回包触发回退)"
    info "  mock-ran: $MOCK_RAN_URL"
    info "  SMF:      $SMF_OAM_URL"
    RUN_FLAGS="$COMMON_FLAGS -core-mode auto -ran-url $MOCK_RAN_URL -ran-udp-endpoint $RAN_UDP_ENDPOINT -ran-udp-ack=$RAN_UDP_ACK -ran-timeout 3s -smf-endpoint $SMF_OAM_URL -default-5qi $DEFAULT_5QI -arp-priority 8 -arp-preempt-cap 1 -arp-preempt-vuln 0"
    ;;

  *)
    echo "=========================================="
    echo "  QoS 模块管理脚本"
    echo "=========================================="
    echo ""
    echo "用法:"
    echo "  $0 <ran|ran-udp|ngap|auto>    启动(自动后台)"
    echo "  $0 stop                        停止"
    echo "  $0 status                      查看状态"
    echo "  $0 restart <mode>              重启"
    echo ""
    echo "模式:"
    echo "  ran      — HTTP 直连 gNB (POST /api/v1/qos/update)"
    echo "  ran-udp  — UDP 直连 gNB (同 JSON,走 UDP)"
    echo "  ngap     — 经核心网 NGAP (SMF /qos-update → AMF → gNB)"
    echo "  auto     — HTTP → UDP → NGAP 依次回退"
    echo ""
    echo "默认地址(改脚本顶部,或用环境变量覆盖):"
    echo "  gNB HTTP:  $RAN_URL"
    echo "  gNB UDP:   $RAN_UDP_ENDPOINT"
    echo "  SMF:       $SMF_ENDPOINT"
    echo ""
    echo "日志: tail -f $LOG_FILE"
    echo "PID:  cat $PID_FILE"
    exit 0
    ;;
esac

# 后台启动(nohup + PID 文件)
mkdir -p "$(dirname "$LOG_FILE")"
echo "=========================================="
echo "  QoS 模块启动 (mode=$MODE,后台)"
echo "=========================================="

nohup "$BINARY" $RUN_FLAGS > "$LOG_FILE" 2>&1 &
PID=$!
echo "$PID" > "$PID_FILE"

sleep 1
if kill -0 "$PID" 2>/dev/null; then
    ok "QoS 模块已启动 (pid=$PID)"
    info "日志: tail -f $LOG_FILE"
    info "停止: $0 stop"
    info "状态: $0 status"
else
    fail "启动失败,查看日志: $LOG_FILE"
    tail -5 "$LOG_FILE" 2>/dev/null
    rm -f "$PID_FILE"
    exit 1
fi
