#!/bin/bash
set -e

# ============================================================
#  QoS 模块管理脚本 — 启动/停止/状态
#
#  用法:
#    ./start-qos.sh <ran|ran-udp|mock-ran|auto>   启动(自动后台)
#    ./start-qos.sh stop                       停止
#    ./start-qos.sh status                     查看状态
#    ./start-qos.sh restart <mode>             重启
#
#  地址默认值已填入(改脚本顶部即可)
#  环境变量仍可覆盖(如: RAN_UDP_ENDPOINT=10.x.x.x:9999 ./start-qos.sh ran-udp)
#
#  注: SMF/ngap 方案已废弃, 不再提供 mode=ngap, auto 也不再带 SMF 第3档。
# ============================================================

# ============================================================
#  默认地址(按需修改这里)
#
#  各模式需要的地址:
#    ran       → QOS_BIND + RAN_URL (HTTP 直连 gNB; RAN_URL 改指 mock-ran 也行)
#    ran-udp   → QOS_BIND + RAN_UDP_ENDPOINT (+ RAN_UDP_ACK)
#    mock-ran  → QOS_BIND + MOCK_RAN_URL (自动起 mock-ran)
#    auto      → QOS_BIND + RAN_UDP_ENDPOINT + MOCK_RAN_URL (UDP 真 gNB → mock-ran 回退)
# ============================================================

# ---- 公共(所有模式) ----
QOS_BIND="${QOS_BIND:-0.0.0.0:7400}"           # QoS 模块 UDP 监听(收 MASQUE 请求)

# ---- mode=ran(HTTP 直连 gNB) / 也可改指 mock-ran ----
RAN_URL="${RAN_URL:-http://10.88.120.212:80/api/v1/qos/update}"  # 真 gNB HTTP

# ---- mode=ran-udp(UDP 直连 gNB) ----
RAN_UDP_ENDPOINT="${RAN_UDP_ENDPOINT:-10.88.0.3:9999}"  # gNB UDP 地址
RAN_UDP_ACK="${RAN_UDP_ACK:-1}"                          # gNB 是否回应答(0=不等,1=等)

# ---- mock-ran(本地模拟 gNB; mode=mock-ran / auto 第2档用) ----
MOCK_RAN_PORT="${MOCK_RAN_PORT:-18081}"
MOCK_RAN_URL="${MOCK_RAN_URL:-http://127.0.0.1:${MOCK_RAN_PORT}/api/v1/qos/update}"  # target -ran-url 用(带路径)
MOCK_RAN_BASE="${MOCK_RAN_BASE:-http://127.0.0.1:${MOCK_RAN_PORT}}"                    # collector 用(base, 它自拼 /metrics)
# ---- 默认地址结束 ----

# ---- 运行时文件 ----
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
TARGET_DIR="$SCRIPT_DIR/../target/target"
BINARY="$TARGET_DIR/target"
PID_FILE="/tmp/qos-module.pid"
LOG_FILE="$SCRIPT_DIR/../logs/qos-module.log"
MOCK_RAN_SCRIPT="$SCRIPT_DIR/../ranreporter/mock_ran.py"
MOCK_RAN_PID_FILE="/tmp/qos-mock-ran.pid"
MOCK_RAN_LOG="$SCRIPT_DIR/../logs/mock-ran.log"

# ---- collector(RANReporter 指标上报)----
# 与 restart-all.sh step 10 共用 pid 文件 + pkill 去重, 谁后跑谁覆盖, 不重复。
COLLECTOR="$SCRIPT_DIR/../ranreporter/collector.py"
COLLECTOR_LOG="$SCRIPT_DIR/../logs/collector.log"
COLLECTOR_PID_FILE="/tmp/ranreporter-collector.pid"
COLLECTOR_URL="${COLLECTOR_URL:-http://192.168.1.10:28448/api/v1/qos}"  # 前端上报目标
GNB_HOST="${GNB_HOST:-10.88.120.212}"   # collector real 档 SSH 真 gNB 用

RED='\033[0;31m'; GREEN='\033[0;32m'; BLUE='\033[0;34m'; YELLOW='\033[1;33m'; NC='\033[0m'
ok()   { echo -e "${GREEN}  ✓ $1${NC}"; }
info() { echo -e "${BLUE}  ℹ $1${NC}"; }
warn() { echo -e "${YELLOW}  ⚠ $1${NC}"; }
fail() { echo -e "${RED}  ✗ $1${NC}"; exit 1; }

# ---- mock-ran 子进程管理 ----
# 判断 URL 是否指向本机 mock-ran(host=127.0.0.1|localhost 且 port=MOCK_RAN_PORT)
url_uses_mock_ran() {
  case "$1" in
    *127.0.0.1:${MOCK_RAN_PORT}*|*localhost:${MOCK_RAN_PORT}*) return 0;;
    *) return 1;;
  esac
}
start_mock_ran() {
  if [ -f "$MOCK_RAN_PID_FILE" ] && kill -0 "$(cat "$MOCK_RAN_PID_FILE")" 2>/dev/null; then
    info "mock-ran 已在运行 (pid=$(cat "$MOCK_RAN_PID_FILE"), :$MOCK_RAN_PORT)"
    return 0
  fi
  command -v python3 >/dev/null 2>&1 || { warn "无 python3, 跳过 mock-ran (auto 第2档将失败)"; return 1; }
  [ -f "$MOCK_RAN_SCRIPT" ] || { warn "mock-ran 脚本不存在: $MOCK_RAN_SCRIPT"; return 1; }
  mkdir -p "$(dirname "$MOCK_RAN_LOG")"
  nohup python3 "$MOCK_RAN_SCRIPT" --port "$MOCK_RAN_PORT" > "$MOCK_RAN_LOG" 2>&1 &
  echo $! > "$MOCK_RAN_PID_FILE"
  sleep 0.6
  if kill -0 "$(cat "$MOCK_RAN_PID_FILE")" 2>/dev/null; then
    ok "mock-ran 已启动 (pid=$(cat "$MOCK_RAN_PID_FILE"), :$MOCK_RAN_PORT, 日志 $MOCK_RAN_LOG)"
  else
    warn "mock-ran 启动失败, 查看 $MOCK_RAN_LOG (auto 第2档回退将失败)"
  fi
}
stop_mock_ran() {
  if [ -f "$MOCK_RAN_PID_FILE" ]; then
    PID=$(cat "$MOCK_RAN_PID_FILE")
    if kill -0 "$PID" 2>/dev/null; then
      kill "$PID" 2>/dev/null
      ok "mock-ran 已停止 (pid=$PID)"
    else
      warn "mock-ran pid=$PID 已不存在"
    fi
    rm -f "$MOCK_RAN_PID_FILE"
  fi
}

# ---- collector 子进程管理 ----
# collector flag 按模式选:
#   mock-ran → --mock-ran(静态读 mock /metrics, 含 IDLE 基线; 该模式本就纯 mock 无需跟随)
#   其余     → --auto-ran(每轮读 qos-module.log 跟随实际生效档 mock/real)
# ran-udp 模式跳过(针对模拟 gNB, 采真基站 trace 无意义)——由调用方判定, 本函数不判。
# start-qos.sh 自身 cmdline 不含 "ranreporter/collector.py", pkill 安全不误杀自己。
start_collector() {
  command -v python3 >/dev/null 2>&1 || { warn "无 python3, 跳过 collector"; return 1; }
  [ -f "$COLLECTOR" ] || { warn "collector 脚本不存在: $COLLECTOR"; return 1; }
  # 幂等: 已在运行就不重起(避免与 restart-all.sh step 10 / 多次调用重复起)
  if [ -f "$COLLECTOR_PID_FILE" ] && kill -0 "$(cat "$COLLECTOR_PID_FILE")" 2>/dev/null; then
    info "collector 已在运行 (pid=$(cat "$COLLECTOR_PID_FILE"))"
    return 0
  fi
  mkdir -p "$(dirname "$COLLECTOR_LOG")"
  local flag="--auto-ran"
  [ "$1" = "mock-ran" ] && flag="--mock-ran"
  nohup python3 "$COLLECTOR" $flag "$MOCK_RAN_BASE" --host "$GNB_HOST" --url "$COLLECTOR_URL" > "$COLLECTOR_LOG" 2>&1 &
  echo $! > "$COLLECTOR_PID_FILE"
  sleep 1
  if kill -0 "$(cat "$COLLECTOR_PID_FILE")" 2>/dev/null; then
    ok "collector 已启动 (pid=$(cat "$COLLECTOR_PID_FILE"), $flag $MOCK_RAN_BASE, 日志 $COLLECTOR_LOG)"
  else
    warn "collector 启动失败, 查看 $COLLECTOR_LOG"
  fi
}
stop_collector() {
  if [ -f "$COLLECTOR_PID_FILE" ]; then
    PID=$(cat "$COLLECTOR_PID_FILE")
    if kill -0 "$PID" 2>/dev/null; then
      kill "$PID" 2>/dev/null
      ok "collector 已停止 (pid=$PID)"
    else
      warn "collector pid=$PID 已不存在"
    fi
    rm -f "$COLLECTOR_PID_FILE"
  fi
  # 兜底: pid 文件丢失但进程还在
  if pkill -f "QoSModule/ranreporter/collector.py" 2>/dev/null; then
    ok "collector 兜底停止 (pkill)"
  fi
}

# ---- 子命令: stop / status ----
CMD="${1:-}"

case "$CMD" in
  stop)
    stop_collector
    stop_mock_ran
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
      else
        warn "QoS 模块未运行(pid 文件残留已清理)"
        rm -f "$PID_FILE"
      fi
    else
      PIDS=$(pgrep -f "target.*-mode qos" 2>/dev/null || true)
      if [ -n "$PIDS" ]; then
        ok "QoS 模块运行中 (pgrep: $PIDS)"
      else
        warn "QoS 模块未运行"
      fi
    fi
    if [ -f "$MOCK_RAN_PID_FILE" ] && kill -0 "$(cat "$MOCK_RAN_PID_FILE")" 2>/dev/null; then
      ok "mock-ran 运行中 (pid=$(cat "$MOCK_RAN_PID_FILE"), :$MOCK_RAN_PORT, 日志 $MOCK_RAN_LOG)"
    else
      info "mock-ran 未运行"
    fi
    if [ -f "$COLLECTOR_PID_FILE" ] && kill -0 "$(cat "$COLLECTOR_PID_FILE")" 2>/dev/null; then
      cflag="--auto-ran"
      case "$(ps -o cmd= -p "$(cat "$COLLECTOR_PID_FILE")" 2>/dev/null)" in
        *--mock-ran*) cflag="--mock-ran" ;;
        *--smf-mock-ran*) cflag="--smf-mock-ran" ;;
      esac
      ok "collector 运行中 (pid=$(cat "$COLLECTOR_PID_FILE"), $cflag, 日志 $COLLECTOR_LOG)"
    else
      info "collector 未运行"
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
    if url_uses_mock_ran "$RAN_URL"; then start_mock_ran; fi
    RUN_FLAGS="$COMMON_FLAGS -core-mode ran -ran-url $RAN_URL -ran-timeout 3s"
    ;;

  ran-udp)
    info "mode=ran-udp(UDP 直连 gNB): $RAN_UDP_ENDPOINT (ack=$RAN_UDP_ACK)"
    RUN_FLAGS="$COMMON_FLAGS -core-mode ran-udp -ran-udp-endpoint $RAN_UDP_ENDPOINT -ran-udp-ack=$RAN_UDP_ACK -ran-timeout 3s"
    ;;

  mock-ran)
    info "mode=mock-ran(HTTP 直连 mock-ran): $MOCK_RAN_URL"
    start_mock_ran
    RUN_FLAGS="$COMMON_FLAGS -core-mode ran -ran-url $MOCK_RAN_URL -ran-timeout 3s"
    ;;

  auto)
    info "mode=auto(UDP 真 gNB → mock-ran 两档回退, SMF 已废弃)"
    info "  UDP:      $RAN_UDP_ENDPOINT (ack=$RAN_UDP_ACK, 需开 ack 才能因无回包触发回退)"
    info "  mock-ran: $MOCK_RAN_URL"
    # auto 第2档是 mock-ran, 必须先起来, 否则 UDP 失败后无回退
    start_mock_ran
    RUN_FLAGS="$COMMON_FLAGS -core-mode auto -ran-url $MOCK_RAN_URL -ran-udp-endpoint $RAN_UDP_ENDPOINT -ran-udp-ack=$RAN_UDP_ACK -ran-timeout 3s"
    ;;

  *)
    echo "=========================================="
    echo "  QoS 模块管理脚本"
    echo "=========================================="
    echo ""
    echo "用法:"
    echo "  $0 <ran|ran-udp|mock-ran|auto>    启动(自动后台)"
    echo "  $0 stop                        停止"
    echo "  $0 status                      查看状态"
    echo "  $0 restart <mode>              重启"
    echo ""
    echo "模式:"
    echo "  ran       — HTTP 直连 gNB (POST /api/v1/qos/update); RAN_URL 指 mock-ran 时自动起 mock-ran"
    echo "  ran-udp   — UDP 直连 gNB (同 JSON,走 UDP)"
    echo "  mock-ran  — HTTP 直连 mock-ran (本地模拟 gNB, 自动起 mock-ran)"
    echo "  auto      — UDP 真 gNB → mock-ran 两档回退 (自动起 mock-ran 作第2档; SMF 已废弃)"
    echo ""
    echo "默认地址(改脚本顶部,或用环境变量覆盖):"
    echo "  gNB HTTP:   $RAN_URL"
    echo "  gNB UDP:    $RAN_UDP_ENDPOINT"
    echo "  mock-ran:   $MOCK_RAN_URL (端口 $MOCK_RAN_PORT)"
    echo "  collector:  $COLLECTOR_URL (--auto-ran; ran-udp 模式不起)"
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
    # collector: ran-udp 跳过(模拟 gNB 无真基站 trace); 其余模式起 collector
    if [ "$MODE" != "ran-udp" ]; then
      start_collector "$MODE"
    else
      info "mode=ran-udp 跳过 collector (模拟 gNB, 采真基站 trace 无意义)"
    fi
else
    fail "启动失败,查看日志: $LOG_FILE"
    tail -5 "$LOG_FILE" 2>/dev/null
    rm -f "$PID_FILE"
    exit 1
fi
