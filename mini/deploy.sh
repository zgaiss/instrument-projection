#!/bin/bash
# deploy.sh —— 编译 fbctl 并传到仪表 /tmp/dt/
#
# 用法：./deploy.sh [端口]
# 前提：adb 已连上车机（默认 172.20.10.14:5050），车机到仪表 172.20.1.40 通。
#
# 传输方向：中控监听 → 仪表主动拉取。
# 反过来（仪表监听、中控推）会被 busybox nc -l 截断，实测 1.7MB 只到 450KB。
set -u

PORT="${1:-9996}"
HERE="$(cd "$(dirname "$0")" && pwd)"
ADB="${ADB:-adb}"
PY="${PY:-python3}"
REMOTE=/data/local/tmp/dt

echo "── 1. 编译 (linux/arm, 静态) ────────────────────"
export PATH="/opt/homebrew/bin:$PATH"
cd "$HERE" || exit 1
GOOS=linux GOARCH=arm GOARM=7 CGO_ENABLED=0 \
  go build -trimpath -ldflags="-s -w" -o fbctl fbctl.go || exit 1
LSIZE=$(wc -c < fbctl | tr -d ' ')
echo "   本地 fbctl = ${LSIZE} B"

echo "── 2. 推到中控 ─────────────────────────────────"
"$ADB" shell "mkdir -p $REMOTE" >/dev/null 2>&1
"$ADB" push -q "$HERE/fbctl" "$REMOTE/fbctl" || exit 1

echo "── 3. 中控起监听 :$PORT ────────────────────────"
"$ADB" shell "nc -l -p $PORT < $REMOTE/fbctl" &
LISTEN_PID=$!
sleep 1

echo "── 4. 仪表拉取 ─────────────────────────────────"
# sleep 必须 > 传输耗时，否则仪表侧 nc 在 stdin EOF 后立刻退出，文件被截断
"$PY" "$HERE/../instr-rpc.py" \
  "( sleep 8 ) | nc 172.20.1.45 $PORT > /tmp/dt/fbctl; chmod 755 /tmp/dt/fbctl" >/dev/null 2>&1

wait $LISTEN_PID 2>/dev/null

echo "── 5. 校验大小 ─────────────────────────────────"
RSIZE=$("$PY" "$HERE/../instr-rpc.py" 'wc -c < /tmp/dt/fbctl' 2>/dev/null | tr -dc '0-9')
echo "   仪表 /tmp/dt/fbctl = ${RSIZE:-?} B"
if [ "${RSIZE:-0}" = "$LSIZE" ]; then
  echo "   ✅ 大小一致，可执行：/tmp/dt/fbctl p"
else
  echo "   ❌ 大小不符，重跑一次；或检查中控 :$PORT 是否还被占用"
  exit 1
fi
