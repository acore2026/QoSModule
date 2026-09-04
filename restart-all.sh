#!/bin/bash
#
# 核心网 + 基站指标上报 一键重启脚本
#
# 环境变量(可选,都有默认值):
#   QOS_MODE=ran|ran-udp|mock-ran|auto   QoSModule 启动模式(默认 auto)
#   COLLECTOR_URL=http://...:28448/api/v1/qos   采集上报目标(默认 192.168.1.10:28448)
#   GNB_HOST=10.88.120.212           基站 SSH 地址
#   RAN_UDP_ENDPOINT=10.88.0.3:9999  gNB UDP 地址(auto 判定 udp 是否生效用)
#
# 采集启动与 QoS 模式关联(step 10, collector --auto-ran 自动跟随实际生效档):
#   ran / mock-ran / auto(UDP 不通) → 启动采集(auto-ran 跟随 mock/real)
#   ran-udp                           → 不启动采集(udp 模式针对模拟 gNB, 采集真实基站无意义)
# 注: SMF/ngap 方案已废弃, start-qos.sh 不再有 mode=ngap, auto 也不带 SMF 第3档。
#

set -e

cd /home/core/free5gc-compose-new

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

TOTAL=10

step() { echo -e "${BLUE}[$1/$TOTAL] $2${NC}"; }
ok() { echo -e "${GREEN}  ✓ $1${NC}"; }
fail() { echo -e "${RED}  ✗ $1${NC}"; exit 1; }

echo "=========================================="
echo "     核心网重启脚本"
echo "=========================================="

step 1 "停止 IMS 服务..."
systemctl stop free5gc-ue-routes.service 2>/dev/null || true
systemctl stop free5gc-disable-offload.service 2>/dev/null || true
systemctl stop kamailio.service 2>/dev/null || true
ok "IMS 服务已停止"

step 2 "停止 Docker 容器..."
docker-compose down || fail "Docker 容器停止失败"
# docker-proxy 释放端口有延迟，等待确保端口已释放，避免 up 时 "address already in use"
echo -e "${YELLOW}  等待端口释放...${NC}"
sleep 3
ok "Docker 容器已停止"

step 3 "加载 gtp5g 内核模块..."
if lsmod | grep -q gtp5g; then
    rmmod gtp5g 2>/dev/null || true
fi
if [ -f /home/core/gtp5g/gtp5g.ko ]; then
    insmod /home/core/gtp5g/gtp5g.ko && ok "gtp5g 模块加载成功" || fail "gtp5g 模块加载失败"
else
    fail "未找到 gtp5g.ko"
fi

step 4 "启动 Docker 容器..."
docker-compose up -d || fail "Docker 容器启动失败"
# 端口冲突可能导致部分容器停留在 created 状态，检测并重试启动
sleep 2
CREATED=$(docker ps -a --filter "status=created" --format "{{.Names}}")
if [ -n "$CREATED" ]; then
    echo -e "${YELLOW}  以下容器未启动，正在重试: $CREATED${NC}"
    for c in $CREATED; do
        docker start "$c" >/dev/null 2>&1 || true
    done
    sleep 2
fi
ok "Docker 容器已启动"

echo -e "${YELLOW}  等待 MongoDB 就绪...${NC}"
for i in $(seq 1 30); do
    docker exec mongodb mongo --quiet --eval "db.adminCommand('ping')" >/dev/null 2>&1 && break
    sleep 2
done
ok "MongoDB 已就绪"

step 5 "清理 UE 上下文..."
docker exec mongodb mongo --quiet --eval "
db = db.getSiblingDB('free5gc');
var r1 = db.subscriptionData.contextData.amf3gppAccess.deleteMany({});
var r2 = db.subscriptionData.authenticationData.authenticationStatus.deleteMany({});
print('AMF上下文: 删除 ' + r1.deletedCount + ' 条');
print('认证状态: 删除 ' + r2.deletedCount + ' 条');
" || true
ok "UE 上下文已清理"

step 6 "配置网络环境..."
ip addr add 10.88.120.100/24 dev eth1 2>/dev/null || true
ip addr add 10.88.120.99/24 dev eth1 2>/dev/null || true
ip link set eth1 up
ip addr add 10.100.200.99/24 dev br-free5gc 2>/dev/null || true
ok "网络配置完成"

step 7 "启动 IMS 服务..."
# 单元文件可能在磁盘上被修改过，先 reload 让 systemd 重新加载，否则 restart 会告警
systemctl daemon-reload || fail "systemctl daemon-reload 失败"
systemctl restart free5gc-ue-routes.service || fail "free5gc-ue-routes 启动失败"
ok "free5gc-ue-routes"
systemctl restart kamailio.service || fail "kamailio 启动失败"
ok "kamailio"
systemctl restart free5gc-disable-offload.service || fail "free5gc-disable-offload 启动失败"
ok "free5gc-disable-offload"

QOS_SCRIPT="/home/core/QoSModule/scripts/start-qos.sh"
QOS_MODE="${QOS_MODE:-auto}"
step 8 "启动 QoSModule ($QOS_MODE 模式)..."
# 先幂等停止可能残留的旧进程（首次运行无 PID 文件也安全）
"$QOS_SCRIPT" stop >/dev/null 2>&1 || true
# 地址可用环境变量覆盖，例如:
#   RAN_URL=http://10.88.120.212:80/api/v1/qos/update \
#   RAN_UDP_ENDPOINT=10.88.0.3:9999 RAN_UDP_ACK=1 \
#   ./restart-all.sh
# QOS_MODE=ran|ran-udp|mock-ran|auto (默认 auto)。采集启动与模式关联:
#   ran/mock-ran → 启动采集; ran-udp → 不启动; auto → 按实际回退判定(step 10)
# QOS_BIND 默认 0.0.0.0:7400，须与 MASQUE Proxy 配置的目标 UDP 端口一致
if "$QOS_SCRIPT" "$QOS_MODE"; then
    ok "QoSModule ($QOS_MODE)"
else
    fail "QoSModule 启动失败，查看日志: /home/core/QoSModule/logs/qos-module.log"
fi

MASQUE_DIR="/home/core/masque/masque/proxy"
MASQUE_LOG="/home/core/masque/masque/proxy.log"
MASQUE_PROXY_URL="${MASQUE_PROXY_URL:-https://10.88.120.100:443}"
step 9 "启动 MASQUE Proxy..."
# 幂等停止旧实例（go run 主进程及其编译产物子进程都带 -proxy 参数）
pkill -f "go run ./cmd/proxy -proxy" 2>/dev/null || true
pkill -f "/proxy -proxy https://" 2>/dev/null || true
sleep 1
# -proxy 地址须与 step 6 在 eth1 上配置的 IP 一致（默认 10.88.120.100）
# 子 shell + nohup 后台运行，避免改变本脚本 cwd
( cd "$MASQUE_DIR" && nohup go run ./cmd/proxy -proxy "$MASQUE_PROXY_URL" > "$MASQUE_LOG" 2>&1 & )
sleep 2
if pgrep -f "proxy -proxy $MASQUE_PROXY_URL" >/dev/null; then
    ok "MASQUE Proxy ($MASQUE_PROXY_URL)"
else
    fail "MASQUE Proxy 启动失败，查看日志: $MASQUE_LOG"
fi

COLLECTOR="/home/core/QoSModule/ranreporter/collector.py"
COLLECTOR_LOG="/home/core/QoSModule/logs/collector.log"
COLLECTOR_PID="/tmp/ranreporter-collector.pid"
COLLECTOR_URL="${COLLECTOR_URL:-http://192.168.1.10:28448/api/v1/qos}"
GNB_HOST="${GNB_HOST:-10.88.120.212}"
# 采集启动与 QoS 模式关联(collector --auto-ran 跟随实际生效档)。
# ran/mock-ran → 启动采集; ran-udp → 不启动; auto → 探实际回退(udp 端点有回包=ran-udp, 否则启动)
effective_qos_mode() {
    case "$1" in
        ran|ran-udp|mock-ran) echo "$1"; return ;;
    esac
    local udp_ep="${RAN_UDP_ENDPOINT:-10.88.0.3:9999}"
    if python3 /home/core/QoSModule/ranreporter/udp_probe.py "$udp_ep" >/dev/null 2>&1; then
        echo "ran-udp"
    else
        echo "collect"
    fi
}
EFF_MODE=$(effective_qos_mode "$QOS_MODE")
step 10 "启动 RANReporter 指标上报 (collector.py --auto-ran 跟随实际生效档: mock/real/smf_bridge)..."
if [ "$EFF_MODE" = "ran-udp" ]; then
    warn "QoS 生效模式=ran-udp, 跳过采集上报 (udp 模式不采集,不影响核心网)"
else
    # 幂等停止旧实例(首次运行无 PID 也安全)
    if [ -f "$COLLECTOR_PID" ]; then
        kill "$(cat "$COLLECTOR_PID")" 2>/dev/null || true
    fi
    pkill -f "QoSModule/ranreporter/collector.py" 2>/dev/null || true
    sleep 1
    # --auto-ran: collector 每轮读 qos-module.log 判定 QoSModule 实际走哪档(-> done):
    #   mock-ran 档 -> 读 mock /metrics; udp-ran/ran-udp 档 -> SSH 真 gNB。
    # (SMF/ngap 已废弃不再产生该档; collector 仍保留 smf_bridge 兼容旧二进制)
    # mock 路径不依赖 SSH, 故 SSH 不通也启动(real 档采空, mock 档仍正常上报)。
    MOCK_RAN_URL="${MOCK_RAN_URL:-http://127.0.0.1:18081}"
    if ssh -o BatchMode=yes -o ConnectTimeout=3 "$GNB_HOST" true 2>/dev/null; then
        ok "基站 $GNB_HOST SSH 可达 (real 档可采真 gNB trace)"
    else
        warn "基站 $GNB_HOST SSH 不可达 (real 档将采空; mock 档仍正常上报)"
    fi
    nohup python3 "$COLLECTOR" --auto-ran "$MOCK_RAN_URL" --host "$GNB_HOST" --url "$COLLECTOR_URL" > "$COLLECTOR_LOG" 2>&1 &
    echo $! > "$COLLECTOR_PID"
    sleep 1
    if kill -0 "$(cat "$COLLECTOR_PID")" 2>/dev/null; then
        ok "RANReporter 已启动 (pid=$(cat "$COLLECTOR_PID"), --auto-ran $MOCK_RAN_URL)"
        info "日志: tail -f $COLLECTOR_LOG"
    else
        warn "RANReporter 启动后即退出,查看日志: $COLLECTOR_LOG (不影响核心网)"
    fi
fi

echo ""
echo "=========================================="
echo -e "${GREEN}     核心网重启完成!${NC}"
echo "=========================================="
systemctl is-active kamailio.service free5gc-disable-offload.service free5gc-ue-routes.service
"$QOS_SCRIPT" status 2>/dev/null || true
pgrep -af "proxy -proxy" 2>/dev/null || true
pgrep -af "QoSModule/ranreporter/collector.py" 2>/dev/null || true