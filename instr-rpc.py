#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
instr-rpc.py —— 通过 30510 管理 RPC 在仪表上执行只读命令（零写入）

协议（从 UltraProjectionRunner.java 的 rpc() 还原）：
  request  = u16BE service(0x0101) | u16BE method(0x0211) | u32BE size
             | u16BE client(0x7e71) | u16BE session | 4B {1,0,0,0} | payload
             size = 8 + len(payload)
  payload  = field1 = headerBytes
             field2 = "shell"
             field2 = command
             headerBytes = 0x08 + varint(0x7e710000 + session)
  field(n) = varint(n*8+2) | varint(len) | bytes
  response = 同结构头 16B（protocol=1, version=0, type=0x80, rc=0）+ body

关键坑：仪表侧 nc 在 stdin EOF 后立刻退出，不会等回包。
        必须发送后保持连接（cat frame; sleep N）| nc 才能读到响应。

用法：
  python3 instr-rpc.py 'uname -a'
  python3 instr-rpc.py --batch cmds.txt
  环境变量 ADB / SERIAL / IC_IP / IC_PORT 可覆盖
"""
import os
import re
import subprocess
import sys
import time

ADB = os.environ.get("ADB", "adb")  # 也可用 ADB=/path/to/adb 覆盖
SERIAL = os.environ.get("SERIAL", "172.20.10.14:5050")
IC_IP = os.environ.get("IC_IP", "172.20.1.40")
IC_PORT = os.environ.get("IC_PORT", "30510")
TMP = "/data/local/tmp/.carprobe"

CLIENT = 0x7E71
BASE = 0x7E710000
SESSION = 0x4101


def varint(value: int) -> bytes:
    out = bytearray()
    while value > 0x7F:
        out.append((value & 0x7F) | 0x80)
        value >>= 7
    out.append(value & 0x7F)
    return bytes(out)


def pbfield(number: int, data: bytes) -> bytes:
    return varint(number * 8 + 2) + varint(len(data)) + data


def build_frame(cmd: str) -> bytes:
    header = b"\x08" + varint(BASE + SESSION)
    payload = pbfield(1, header) + pbfield(2, b"shell") + pbfield(2, cmd.encode("utf-8"))
    return (
        b"\x01\x01"                      # service
        + b"\x02\x11"                    # method
        + (8 + len(payload)).to_bytes(4, "big")  # size
        + b"\x7e\x71"                    # client
        + SESSION.to_bytes(2, "big")     # session
        + b"\x01\x00\x00\x00"            # protocol=1 version=0 type=0
        + payload
    )


def adb(*args, capture=True):
    return subprocess.run(
        [ADB, "-s", SERIAL, *args],
        capture_output=capture,
        timeout=60,
    )


def run_remote(cmd: str, wait: float = 2.5) -> bytes:
    """把帧推到车机，保持连接等待回包，返回原始响应字节。"""
    frame = build_frame(cmd)
    local = "/tmp/.instr-rpc.frame"
    with open(local, "wb") as f:
        f.write(frame)
    adb("push", "-q", local, f"{TMP}/req.bin")
    remote = (
        f"( cat {TMP}/req.bin; sleep {wait} ) | nc -w 6 {IC_IP} {IC_PORT} "
        f"> {TMP}/resp.bin 2>{TMP}/err.txt; wc -c < {TMP}/resp.bin"
    )
    adb("shell", remote)
    got = adb("exec-out", f"cat {TMP}/resp.bin")
    return got.stdout or b""


def parse_response(raw: bytes):
    """返回 (header_ok, rc, type, [strings])"""
    if len(raw) < 16:
        return (False, None, None, [])
    service = int.from_bytes(raw[0:2], "big")
    method = int.from_bytes(raw[2:4], "big")
    size = int.from_bytes(raw[4:8], "big")
    client = int.from_bytes(raw[8:10], "big")
    sess = int.from_bytes(raw[10:12], "big")
    protocol, version, rtype, rc = raw[12], raw[13], raw[14], raw[15]
    ok = (
        service == 0x101
        and method == 0x211
        and client == CLIENT
        and sess == SESSION
        and protocol == 1
        and version == 0
        and rtype == 0x80
        and rc == 0
    )
    body = raw[16:]
    return (ok, rc, rtype, extract_strings(body))


def _vi(buf, i):
    value = 0
    shift = 0
    while i < len(buf):
        b = buf[i]
        i += 1
        value |= (b & 0x7F) << shift
        if not (b & 0x80):
            return value, i
        shift += 7
        if shift > 63:
            break
    raise ValueError("varint")


def extract_strings(buf: bytes):
    """递归抓所有 length-delimited 字段，保留可打印的。"""
    out = []
    i = 0
    while i < len(buf):
        try:
            tag, i = _vi(buf, i)
        except ValueError:
            break
        wire = tag & 7
        if wire == 0:
            _, i = _vi(buf, i)
        elif wire == 2:
            try:
                ln, i = _vi(buf, i)
            except ValueError:
                break
            chunk = buf[i:i + ln]
            i += ln
            printable = sum(1 for b in chunk if 9 <= b <= 13 or 32 <= b < 127)
            if chunk and printable / len(chunk) > 0.85:
                out.append(chunk.decode("utf-8", "replace"))
            else:
                out.extend(extract_strings(chunk))
        elif wire == 5:
            i += 4
        elif wire == 1:
            i += 8
        else:
            break
    return out


def exec_readonly(cmd: str, wait: float = 2.5):
    raw = run_remote(cmd, wait=wait)
    ok, rc, rtype, strings = parse_response(raw)
    return ok, rc, raw, "\n".join(strings)


def main():
    if len(sys.argv) < 2:
        print(__doc__)
        return 1
    adb("shell", f"mkdir -p {TMP}")
    if sys.argv[1] == "--batch":
        with open(sys.argv[2], "r", encoding="utf-8") as f:
            cmds = [ln.strip() for ln in f if ln.strip() and not ln.startswith("#")]
    else:
        cmds = [" ".join(sys.argv[1:])]

    for cmd in cmds:
        t0 = time.time()
        ok, rc, raw, text = exec_readonly(cmd, wait=2.5)
        dt = int((time.time() - t0) * 1000)
        print(f"\n$ {cmd}")
        if not raw:
            print("  [无响应] 0 字节")
        elif not ok:
            print(f"  [头校验失败] rc={rc} type={rtype} bytes={len(raw)}")
            print("  " + raw[:64].hex(" "))
        else:
            print(f"  [rc=0, {len(raw)}B, {dt}ms]")
            for line in text.splitlines():
                print("  " + line)
    return 0


if __name__ == "__main__":
    sys.exit(main())
