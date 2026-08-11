#!/usr/bin/env bash
# Linux equivalent of start-windows-mock-test.ps1.
# Starts Mock RAN and the QoS target as background processes, writes logs
# under <repo>/logs, and stores PIDs so `--stop` can kill the process groups.
set -euo pipefail

MOCK_BIND="127.0.0.1:18081"
TARGET_BIND="0.0.0.0:7400"
RAN_PATH="/api/v1/qos/update"
MOCK_STATUS="ACCEPTED"
MOCK_MESSAGE="mock ran accepted"
SKIP_PORT_CHECK=0
ACTION="start"
READY_TIMEOUT=15

usage() {
  cat <<'EOF'
Usage: start-linux-mock-test.sh [options] [--stop|--status]
  --mock-bind HOST:PORT     Mock RAN HTTP bind (default 127.0.0.1:18081)
  --target-bind HOST:PORT   Target UDP bind (default 0.0.0.0:7400)
  --ran-path PATH           RAN QoS update path (default /api/v1/qos/update)
  --mock-status STATUS      Mock RAN response status (default ACCEPTED)
  --mock-message MSG        Mock RAN response message (default "mock ran accepted")
  --skip-port-check         Do not fail if ports are already in use
  --ready-timeout SEC       Seconds to wait for ports to listen (default 15)
  --stop                    Stop running services and exit
  --status                  Show service status and exit
  -h, --help                Show this help
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --mock-bind) MOCK_BIND="${2:?--mock-bind requires a value}"; shift 2 ;;
    --target-bind) TARGET_BIND="${2:?--target-bind requires a value}"; shift 2 ;;
    --ran-path) RAN_PATH="${2:?--ran-path requires a value}"; shift 2 ;;
    --mock-status) MOCK_STATUS="${2:?--mock-status requires a value}"; shift 2 ;;
    --mock-message) MOCK_MESSAGE="${2:?--mock-message requires a value}"; shift 2 ;;
    --skip-port-check) SKIP_PORT_CHECK=1; shift ;;
    --ready-timeout) READY_TIMEOUT="${2:?--ready-timeout requires a value}"; shift 2 ;;
    --stop) ACTION="stop"; shift ;;
    --status) ACTION="status"; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "Unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
done

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
TARGET_DIR="$REPO_ROOT/target/target"

if [[ ! -f "$TARGET_DIR/go.mod" ]]; then
  echo "Cannot find target module at '$TARGET_DIR'. Run this script from the QoSModule checkout." >&2
  exit 1
fi

if ! command -v go >/dev/null 2>&1; then
  echo "Go is not installed or not available in PATH." >&2
  exit 1
fi

command -v ss >/dev/null 2>&1 || { echo "'ss' is required for port checks." >&2; exit 1; }

LOG_DIR="$REPO_ROOT/logs"
mkdir -p "$LOG_DIR"
MOCK_PID_FILE="$LOG_DIR/mockran.pid"
TARGET_PID_FILE="$LOG_DIR/target.pid"
MOCK_LOG="$LOG_DIR/mockran.log"
TARGET_LOG="$LOG_DIR/target.log"

port_of() { local ep="$1"; echo "${ep##*:}"; }
MOCK_PORT="$(port_of "$MOCK_BIND")"
TARGET_PORT="$(port_of "$TARGET_BIND")"

is_tcp_listen() { ss -ltn "sport = :$1" 2>/dev/null | grep -q ":$1[[:space:]]"; }
is_udp_listen() { ss -lun "sport = :$1" 2>/dev/null | grep -q ":$1[[:space:]]"; }

pid_alive() { [[ -f "$1" ]] && kill -0 "$(cat "$1" 2>/dev/null || true)" 2>/dev/null; }

stop_pidfile() {
  local pidfile="$1" name="$2"
  if [[ ! -f "$pidfile" ]]; then
    echo "$name: not running (no pid file)."
    return 0
  fi
  local pid
  pid="$(cat "$pidfile" 2>/dev/null || true)"
  if [[ -z "$pid" ]]; then
    rm -f "$pidfile"
    echo "$name: not running (empty pid file)."
    return 0
  fi
  if ! kill -0 "$pid" 2>/dev/null; then
    rm -f "$pidfile"
    echo "$name: not running (stale pid file)."
    return 0
  fi
  # pid was started with setsid, so it is its own process-group leader.
  # Kill the whole group so `go run` and the compiled binary both die.
  kill -TERM -- -"$pid" 2>/dev/null || kill -TERM "$pid" 2>/dev/null || true
  for _ in $(seq 1 30); do
    kill -0 "$pid" 2>/dev/null || break
    sleep 0.1
  done
  if kill -0 "$pid" 2>/dev/null; then
    kill -KILL -- -"$pid" 2>/dev/null || kill -KILL "$pid" 2>/dev/null || true
  fi
  rm -f "$pidfile"
  echo "$name: stopped (pid $pid)."
}

show_status() {
  local pidfile="$1" name="$2" log="$3"
  if pid_alive "$pidfile"; then
    local pid
    pid="$(cat "$pidfile")"
    echo "$name: RUNNING (pid $pid); log: $log"
  else
    echo "$name: STOPPED; log: $log"
  fi
}

case "$ACTION" in
  stop)
    stop_pidfile "$MOCK_PID_FILE" "Mock RAN"
    stop_pidfile "$TARGET_PID_FILE" "QoS Target"
    exit 0
    ;;
  status)
    show_status "$MOCK_PID_FILE" "Mock RAN" "$MOCK_LOG"
    show_status "$TARGET_PID_FILE" "QoS Target" "$TARGET_LOG"
    exit 0
    ;;
  start)
    if pid_alive "$MOCK_PID_FILE" || pid_alive "$TARGET_PID_FILE"; then
      echo "Some services are already running. Run '$0 --stop' first or '--status' to inspect." >&2
      exit 1
    fi
    rm -f "$MOCK_PID_FILE" "$TARGET_PID_FILE"
    : > "$MOCK_LOG"
    : > "$TARGET_LOG"
    if [[ "$SKIP_PORT_CHECK" -eq 0 ]]; then
      if is_tcp_listen "$MOCK_PORT"; then
        echo "TCP port $MOCK_PORT is already in use. Choose another --mock-bind, e.g. 127.0.0.1:18082, or pass --skip-port-check." >&2
        exit 1
      fi
      if is_udp_listen "$TARGET_PORT"; then
        echo "UDP port $TARGET_PORT is already in use. Choose another --target-bind, e.g. 0.0.0.0:7401, or pass --skip-port-check." >&2
        exit 1
      fi
    fi
    ;;
esac

RAN_URL="http://$MOCK_BIND$RAN_PATH"

wait_listen() {
  local kind="$1" port="$2" pidfile="$3"
  local i=0
  while (( i < READY_TIMEOUT * 10 )); do
    if ! kill -0 "$(cat "$pidfile" 2>/dev/null || echo 0)" 2>/dev/null; then
      return 1
    fi
    if [[ "$kind" == "tcp" ]] && is_tcp_listen "$port"; then return 0; fi
    if [[ "$kind" == "udp" ]] && is_udp_listen "$port"; then return 0; fi
    sleep 0.1
    i=$((i+1))
  done
  return 1
}

echo "Starting Mock RAN on $MOCK_BIND$RAN_PATH"
setsid bash -c "cd '$TARGET_DIR' && exec go run ./cmd/mockran -b '$MOCK_BIND' -path '$RAN_PATH' -status '$MOCK_STATUS' -message '$MOCK_MESSAGE'" >"$MOCK_LOG" 2>&1 &
echo $! > "$MOCK_PID_FILE"

if ! wait_listen tcp "$MOCK_PORT" "$MOCK_PID_FILE"; then
  echo "Mock RAN did not become ready on $MOCK_BIND within ${READY_TIMEOUT}s. See log:" >&2
  echo "  tail -f $MOCK_LOG" >&2
  exit 1
fi

echo "Starting QoS target on UDP $TARGET_BIND"
echo "RAN endpoint: $RAN_URL"
setsid bash -c "cd '$TARGET_DIR' && exec go run ./cmd/target -mode qos -b '$TARGET_BIND' -ran-url '$RAN_URL'" >"$TARGET_LOG" 2>&1 &
echo $! > "$TARGET_PID_FILE"

if ! wait_listen udp "$TARGET_PORT" "$TARGET_PID_FILE"; then
  echo "QoS target did not become ready on UDP $TARGET_BIND within ${READY_TIMEOUT}s. See log:" >&2
  echo "  tail -f $TARGET_LOG" >&2
  exit 1
fi

cat <<EOF

Started local QoS mock test services (background).
Mock RAN:   $RAN_URL
Target UDP: $TARGET_BIND
Mock RAN log:  $MOCK_LOG
Target log:    $TARGET_LOG

Configure MASQUE Proxy target to one of:
  Same host:        127.0.0.1:$TARGET_PORT
  Other machine:    <this-host-LAN-IP>:$TARGET_PORT

If MASQUE Proxy runs on another machine, allow inbound UDP $TARGET_PORT.

Stop services:
  $0 --stop
Show status:
  $0 --status
Tail logs:
  tail -f "$MOCK_LOG"
  tail -f "$TARGET_LOG"
EOF
