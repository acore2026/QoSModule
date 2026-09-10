#!/bin/bash
set -e

# ============================================================
#  QoS 模块管理脚本 — 启动/停止/状态
#
#  用法:
#    ./start-qos.sh <ran-udp|mock-ran>         启动(自动后台)
#    ./start-qos.sh stop                       停止
#    ./start-qos.sh status                     查看状态
#    ./start-qos.sh restart <mode>             重启
#
#  地址默认值已填入(改脚本顶部即可)
#  环境变量仍可覆盖(如: RAN_UDP_ENDPOINT=10.x.x.x:9999 ./start-qos.sh ran-udp)
#
#  注: 上报由下发目标自己负责, 不再有中间采集器——
#      mock-ran 模式 → mock-ran 自报前端; ran-udp 模式 → 远程基站自报前端。
#      二者互斥, 故前端不会同时收到两路数据。
#      SMF/ngap 方案已废弃; auto 三档回退已退役(它会导致两个上报源同时活着)。
# ============================================================

# ============================================================
#  默认地址(按需修改这里)
#
#  各模式需要的地址:
#    ran-udp   → QOS_BIND + RAN_UDP_ENDPOINT (+ RAN_UDP_ACK); 远程基站自报前端
#    mock-ran  → QOS_BIND + MOCK_RAN_URL + FRONTEND_URL (自动起 mock-ran, 由它自报前端)
# ============================================================

# ---- 公共(所有模式) ----
QOS_BIND="${QOS_BIND:-0.0.0.0:7400}"           # QoS 模块 UDP 监听(收 MASQUE 请求)

# ---- mode=ran-udp(UDP 直连远程基站) ----
RAN_UDP_ENDPOINT="${RAN_UDP_ENDPOINT:-10.88.0.3:9999}"  # 远程基站 UDP 地址
RAN_UDP_ACK="${RAN_UDP_ACK:-1}"                          # 基站是否回应答(0=不等,1=等)

# ---- mock-ran(本地模拟 gNB) ----
MOCK_RAN_PORT="${MOCK_RAN_PORT:-18081}"
MOCK_RAN_URL="${MOCK_RAN_URL:-http://127.0.0.1:${MOCK_RAN_PORT}/api/v1/qos/update}"  # target -ran-url 用(带路径)

# ---- 前端上报目标(mock-ran 自报用; ran-udp 模式由远程基站自己报, 不经此处) ----
FRONTEND_URL="${FRONTEND_URL:-http://192.168.1.10:28448/api/v1/qos}"
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

RED='\033[0;31m'; GREEN='\033[0;32m'; BLUE='\033[0;34m'; YELLOW='\033[1;33m'; NC='\033[0m'
ok()   { echo -e "${GREEN}  ✓ $1${NC}"; }
info() { echo -e "${BLUE}  ℹ $1${NC}"; }
warn() { echo -e "${YELLOW}  ⚠ $1${NC}"; }
fail() { echo -e "${RED}  ✗ $1${NC}"; exit 1; }

# ---- mock-ran 子进程管理 ----
start_mock_ran() {
  if [ -f "$MOCK_RAN_PID_FILE" ] && kill -0 "$(cat "$MOCK_RAN_PID_FILE")" 2>/dev/null; then
    info "mock-ran 已在运行 (pid=$(cat "$MOCK_RAN_PID_FILE"), :$MOCK_RAN_PORT)"
    return 0
  fi
  command -v python3 >/dev/null 2>&1 || { warn "无 python3, 无法起 mock-ran"; return 1; }
  [ -f "$MOCK_RAN_SCRIPT" ] || { warn "mock-ran 脚本不存在: $MOCK_RAN_SCRIPT"; return 1; }
  mkdir -p "$(dirname "$MOCK_RAN_LOG")"
  # env -u: 剥掉继承来的代理变量。mock-ran 自报的前端是内网直连目标, 走 http_proxy 必然超时
  # (.bashrc 里硬编码的代理 IP 会随宿主变更失效)。mock_ran.py 内部已用空 ProxyHandler 兜底,
  # 此处是双保险。
  env -u http_proxy -u https_proxy -u all_proxy \
    nohup python3 "$MOCK_RAN_SCRIPT" --port "$MOCK_RAN_PORT" \
      --frontend-url "$FRONTEND_URL" > "$MOCK_RAN_LOG" 2>&1 &
  echo $! > "$MOCK_RAN_PID_FILE"
  sleep 0.6
  if kill -0 "$(cat "$MOCK_RAN_PID_FILE")" 2>/dev/null; then
    ok "mock-ran 已启动 (pid=$(cat "$MOCK_RAN_PID_FILE"), :$MOCK_RAN_PORT, 自报 -> $FRONTEND_URL)"
    info "日志: tail -f $MOCK_RAN_LOG"
  else
    warn "mock-ran 启动失败, 查看 $MOCK_RAN_LOG"
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
  # 兜底: pid 文件丢失但进程还在(异常退出/手工起过)。必须清干净, 否则它会继续
  # 自报前端, 与 ran-udp 模式的远程基站形成双源。
  if pgrep -f "ranreporter/mock_ran.py" >/dev/null 2>&1; then
    pkill -f "ranreporter/mock_ran.py" 2>/dev/null || true
    sleep 0.3
    ok "mock-ran 兜底停止 (pkill 遗留实例)"
  fi
}

# ---- 子命令: stop / status ----
CMD="${1:-}"

usage() {
  echo "=========================================="
  echo "  QoS 模块管理脚本"
  echo "=========================================="
  echo ""
  echo "用法:"
  echo "  $0 <ran-udp|mock-ran>          启动(自动后台)"
  echo "  $0 stop                        停止"
  echo "  $0 status                      查看状态"
  echo "  $0 restart <mode>              重启"
  echo ""
  echo "模式(二选一, 上报由下发目标自己负责):"
  echo "  ran-udp   — UDP 直连远程基站; 远程基站自己上报前端"
  echo "  mock-ran  — HTTP 直连本地 mock-ran; mock-ran 自己上报前端"
  echo ""
  echo "默认地址(改脚本顶部,或用环境变量覆盖):"
  echo "  远程基站 UDP: $RAN_UDP_ENDPOINT (ack=$RAN_UDP_ACK)"
  echo "  mock-ran:     $MOCK_RAN_URL (端口 $MOCK_RAN_PORT)"
  echo "  前端上报:     $FRONTEND_URL (仅 mock-ran 模式用)"
  echo ""
  echo "已退役: mode=ran(默认目标 10.88.120.212 已下线)、mode=auto(三档回退会导致"
  echo "        远程基站与 mock-ran 两个上报源同时活着, 前端 schema 无源标识无法区分)、"
  echo "        mode=ngap/SMF、中间采集器 collector.py。"
  echo ""
  echo "日志: tail -f $LOG_FILE"
  echo "PID:  cat $PID_FILE"
}

# 无参调用直接出 usage: 否则会被后面的"已在运行"检查挡住, 模块跑着时永远看不到帮助
if [ -z "$CMD" ]; then
  usage
  exit 0
fi

case "$CMD" in
  stop)
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
      case "$(ps -o cmd= -p "$(cat "$MOCK_RAN_PID_FILE")" 2>/dev/null)" in
        *--frontend-url*)
          fe="$(ps -o cmd= -p "$(cat "$MOCK_RAN_PID_FILE")" | sed -n 's/.*--frontend-url \([^ ]*\).*/\1/p')"
          [ -n "$fe" ] && info "自报前端: $fe" || info "自报前端: 未配置(纯模拟器)"
          ;;
        *) info "自报前端: 未配置(纯模拟器)" ;;
      esac
    else
      info "mock-ran 未运行"
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

# 先校验模式再查"已在运行": 否则传错模式时只会看到"已在运行", 拿不到真正的错误原因
case "$MODE" in
  ran-udp|mock-ran) ;;
  *)
    warn "未知模式: $MODE (支持 ran-udp|mock-ran)"
    echo ""
    usage
    exit 1
    ;;
esac

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
  ran-udp)
    info "mode=ran-udp(UDP 直连远程基站): $RAN_UDP_ENDPOINT (ack=$RAN_UDP_ACK)"
    info "  上报: 由远程基站自己 POST 前端, 本机不起任何上报进程"
    # 强制互斥: 前端 schema 无数据源标识字段, mock-ran 与远程基站同时自报会画出
    # 无法区分合并的交织曲线。QoSModule 进程异常退出而 mock-ran 存活时, PID_FILE
    # 检查会放行启动, 故此处显式停掉任何遗留的 mock-ran。
    stop_mock_ran
    RUN_FLAGS="$COMMON_FLAGS -core-mode ran-udp -ran-udp-endpoint $RAN_UDP_ENDPOINT -ran-udp-ack=$RAN_UDP_ACK -ran-timeout 3s"
    ;;

  mock-ran)
    info "mode=mock-ran(HTTP 直连 mock-ran): $MOCK_RAN_URL"
    info "  上报: mock-ran 自报 -> $FRONTEND_URL"
    start_mock_ran
    # 注: mock-ran 模式在二进制层面就是 ran 模式(-core-mode ran), 只是 -ran-url 指向本地 mock。
    # Go 侧 ModeRAN 因此必须保留, 不存在 -core-mode mock-ran。
    RUN_FLAGS="$COMMON_FLAGS -core-mode ran -ran-url $MOCK_RAN_URL -ran-timeout 3s"
    ;;

  *)
    warn "未知模式: $MODE (支持 ran-udp|mock-ran)"
    echo ""
    usage
    exit 1
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
    if [ "$MODE" = "ran-udp" ]; then
      info "上报由远程基站($RAN_UDP_ENDPOINT)自己负责, 本机无上报进程"
    fi
else
    fail "启动失败,查看日志: $LOG_FILE"
    tail -5 "$LOG_FILE" 2>/dev/null
    rm -f "$PID_FILE"
    exit 1
fi
