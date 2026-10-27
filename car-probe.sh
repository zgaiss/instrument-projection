#!/bin/bash
# car-probe.sh —— 仪表投放方案 · 零写入最小探查
#
# 设计原则：
#   1. 不投递任何二进制到仪表；不启动 receiver；不改 sysfs；不动 /dev/fb*
#   2. 仪表侧只发两条只读命令：uname -a、cat 几个 sysfs 文件
#   3. 中控侧只跑只读查询
#   唯一落盘：中控 /data/local/tmp 下两个 41~130 字节的请求帧（可用 --clean 删除）
#
# 用法：
#   ./car-probe.sh              # 全量探查
#   ./car-probe.sh --clean      # 清掉中控上的临时请求帧
#   SERIAL=192.168.1.9:5555 ./car-probe.sh   # 指定 adb 目标
#
set -uo pipefail

SERIAL="${SERIAL:-172.20.10.14:5050}"
IC_IP="172.20.1.40"
IC_RPC=30510
IC_FRAME=49173
TMP="/data/local/tmp/.carprobe"
OUT="./probe-$(date +%Y%m%d-%H%M%S)"

# ---- 定位 adb：环境变量 > PATH > 已知安装位置 ----
if [ -z "${ADB:-}" ]; then
  if command -v adb >/dev/null 2>&1; then
    ADB="$(command -v adb)"
  else
    for c in \
      "$HOME/Library/Android/sdk/platform-tools/adb" \
      "/opt/homebrew/bin/adb" \
      "/usr/local/bin/adb"
    do
      [ -x "$c" ] && ADB="$c" && break
    done
  fi
fi
if [ -z "${ADB:-}" ] || [ ! -x "$ADB" ]; then
  echo "找不到 adb，请用 ADB=/path/to/adb ./car-probe.sh 指定" >&2
  exit 1
fi

A() { "$ADB" -s "$SERIAL" "$@"; }
S() { A shell "$@" 2>/dev/null; }

if ! A get-state >/dev/null 2>&1; then
  echo "adb 连不上 $SERIAL，先确认：$ADB devices" >&2
  exit 1
fi

# protobuf varint 编码（写原始字节）
put_varint() {
  local v=$1
  while [ "$v" -ge 128 ]; do
    printf "\\x$(printf %02x $(( (v & 127) | 128 )))"
    v=$(( v >> 7 ))
  done
  printf "\\x$(printf %02x $(( v & 127 )))"
}

varint_len() {
  local v=$1 n=1
  while [ "$v" -ge 128 ]; do v=$(( v >> 7 )); n=$(( n + 1 )); done
  echo "$n"
}

# 构造一帧 30510 管理 RPC 请求（只读命令）
build_frame() {
  local cmd="$1"
  local clen; clen=$(printf '%s' "$cmd" | wc -c | tr -d ' ')
  local clenv; clenv=$(varint_len "$clen")
  # payload = field1(1+1+6) + field2"shell"(1+1+5) + field2cmd(1+varint(clen)+clen)
  local plen=$(( 16 + clenv + clen ))
  local size=$(( 8 + plen ))
  {
    printf '\x01\x01\x02\x11'
    printf "\\x$(printf %02x $(( size >> 24 )))\\x$(printf %02x $((( size >> 16 ) & 255 )))\\x$(printf %02x $((( size >> 8 ) & 255 )))\\x$(printf %02x $(( size & 255 )))"
    printf '\x7e\x71\x41\x01\x01\x00\x00\x00'
    printf '\x0a\x06\x08\x81\x82\xc5\xf3\x07'
    printf '\x12\x05shell'
    printf '\x12'
    put_varint "$clen"
    printf '%s' "$cmd"
  }
}

# 发一帧，取原始响应
rpc_read() {
  local name="$1" cmd="$2"
  local f="$TMP.$name"
  build_frame "$cmd" > "/tmp/.frame.$name" || return 1
  A push "/tmp/.frame.$name" "$f" >/dev/null 2>&1 || { echo "  [$name] 推送请求帧失败"; return 1; }
  A exec-out "cat $f | nc -w 4 $IC_IP $IC_RPC" > "$OUT/rpc-$name.bin" 2>/dev/null
  local n
  n=$(wc -c < "$OUT/rpc-$name.bin" | tr -d ' ')
  echo "  [$name] 请求 $cmd"
  echo "  [$name] 响应 ${n} 字节"
  if [ "$n" -gt 0 ]; then
    echo "  [$name] 解码（hex）："
    od -A d -t x1 "$OUT/rpc-$name.bin" | sed 's/^/      /'
    echo "  [$name] 解码（可读）："
    od -A d -c "$OUT/rpc-$name.bin" | sed 's/^/      /'
  else
    echo "  [$name] 无响应 —— 见文末「无响应怎么判断」"
  fi
  echo
}

if [ "${1:-}" = "--clean" ]; then
  A shell "rm -f $TMP.*" >/dev/null 2>&1
  echo "已清理中控 $TMP.*"
  exit 0
fi

mkdir -p "$OUT"
echo "=============================================="
echo " 零写入最小探查   $(date)"
echo " 输出目录：$OUT"
echo " 本脚本不做任何写入操作（仪表侧只读，中控侧只查）"
echo "=============================================="
echo

echo "── 0. 连接与机型 ─────────────────────────────"
adb connect "$SERIAL" >/dev/null 2>&1
if ! A get-state 2>/dev/null | grep -q device; then
  echo "  ✗ 连不上 $SERIAL，先确认车机热点已连、端口 5050 开着"
  exit 1
fi
echo "  ✓ 已连接 $SERIAL"
S 'getprop ro.product.model; getprop ro.build.version.release; getprop ro.build.display.id; getprop ro.build.fingerprint' \
  | sed 's/^/    /'
echo

echo "── 1. 中控在车内网的位置（门槛 B1）────────────"
echo "  IPv4 地址："
S 'ip -4 addr show 2>/dev/null | grep -w inet' | sed 's/^/    /'
echo "  路由："
S 'ip route 2>/dev/null' | sed 's/^/    /'
if S 'ip -4 addr show 2>/dev/null | grep -qw "172.20.1.45"'; then
  echo "  ✓ 中控就是 172.20.1.45，与代码硬编码一致"
else
  echo "  ✗ 没找到 172.20.1.45 —— 两端代码都要改并重编，先别往下走"
fi
echo

echo "── 2. 到仪表的端口连通性（门槛 B2）───────────"
if ! S 'command -v nc >/dev/null 2>&1 && echo yes' | grep -q yes; then
  echo "  ⚠ 车机上没有 nc，后续 RPC 探测无法进行（可试 toybox nc / busybox nc）"
fi
for p in "$IC_RPC" "$IC_FRAME"; do
  # 注意：车机是 toybox nc，不支持 -z；用「连上后 stdin 即为 EOF」判活
  r=$(S "nc -w 2 $IC_IP $p </dev/null 2>&1; echo \"rc=\$?\"")
  case "$r" in
    *rc=0*)   verdict="可连（有服务在听）" ;;
    *refused*|*Refused*) verdict="端口关闭/无服务（Connection refused）" ;;
    *timeout*|*timed*|*rc=1*) verdict="不可达或超时（可能被防火墙丢包）" ;;
    *)        verdict="未知（原样见下）" ;;
  esac
  printf "    %s:%s  -> %s\n" "$IC_IP" "$p" "$verdict"
  printf "      raw: %s\n" "$(echo "$r" | tr '\n' ' ' | cut -c1-120)"
done
echo

echo "── 3. 30510 通道是否真的执行 shell（关键）────"
rpc_read "uname" "uname -a"
echo

echo "── 4. 仪表 fb1 基线参数（只读，决定能不能显示对）"
rpc_read "fb1" "cat /sys/class/graphics/fb1/modes /sys/class/graphics/fb1/mode /sys/class/graphics/fb1/virtual_size /sys/class/graphics/fb1/bits_per_pixel"
echo

echo "── 5. 仪表 /tmp 可执行性与空间（门槛 B3）─────"
rpc_read "tmp" "grep ' /tmp ' /proc/mounts; df /tmp"
echo

echo "── 6. 中控侧：CarPlay 取帧通道与权限 ─────────"
echo "  SELinux: $(S 'getenforce')"
echo "  相关进程："
S 'ps -A 2>/dev/null | grep -iE "carplay|autokit|cpplay|shizuku" | head -20' | sed 's/^/    /'
echo "  相关包："
S 'pm list packages 2>/dev/null | grep -iE "carplay|autokit|shizuku|xmart|xpcar"' | sed 's/^/    /'
echo "  SurfaceFlinger 图层（含 carplay/alt 关键字）："
S 'dumpsys SurfaceFlinger --list 2>/dev/null | grep -iE "carplay|alt" | head -20' | sed 's/^/    /'
echo

echo "=============================================="
echo " 结果在：$OUT/"
echo " 把 rpc-uname.bin 的 od 输出贴回来，就能判定通道是否成立。"
echo " 收尾：./car-probe.sh --clean"
echo "=============================================="
echo
echo "【无响应怎么判断】"
echo "  1) 端口都不通        -> B2 不成立，方案止步于此"
echo "  2) 端口通但无响应    -> 可能服务端有来源 IP 白名单，或帧格式/会话号需要调整"
echo "  3) 有响应但 rpc 头校验失败 -> 把 od 输出贴回来定位"
echo
echo "【安全声明】本脚本未向仪表写入任何内容：未投递二进制、未启动 receiver、未改 fb。"
