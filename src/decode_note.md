# src 目录说明

`ultra_app_receiver.c` / `UltraProjectionRunner.java` / `UltraFrameServer.java` 来自 QQ 邮箱
「Fwd: 仪表」（h***@gmail.com，2026-10-27 11:23，message_id 已隐去）。

附件通过 `mcp__qq-mail__DownloadAttachment` 取回时只返回 base64 文本，不落盘，所以本目录存放的是
从源码中提取的关键实现摘录（不是完整原文件）。完整原件需要时按上面的 message_id + attachment_id 重新下载：

| 文件 | attachment_id |
|---|---|
| ultra_app_receiver.c | `att_bl91bHRyYV9hcHBfcmVjZWl2ZXIuYw` |
| UltraProjectionRunner.java | `att_bl9VbHRyYVByb2plY3Rpb25SdW5uZXIuamF2YQ` |
| UltraFrameServer.java | `att_bl9VbHRyYUZyYW1lU2VydmVyLmphdmE` |

## 从 ultra_app_receiver.c 提取的关键事实（已核实）

- `setmode(wide)`：
  - 先把 2 个 `struct video_mode`（56 B/个，共 112 B）写进 `/sys/class/graphics/fb1/modes`
    （m[0] 由 fb1 当前 var 生成，m[1] 由 **fb0 的 var** 生成）；
  - 再向 `/sys/class/graphics/fb1/mode` 写 `"U:1920x720p-60\n"`（15 B）。
  - 切回窄模式：写 1 个 video_mode（56 B）→ 写 `"U:240x320p-60\n"`（14 B）→ 写
    `/sys/class/graphics/fb1/virtual_size` = `"240,960\n"`（8 B）。
- 全屏后校验：`xres=1920 yres=720 vx=1920 vy=720 bpp=16 stride=3840 smem>=2764800`。
- 打开全屏的前置校验 `expected()`：fb1 必须仍是
  `240x320 / virtual 240x960 / 16bpp / stride 480 / 460800<=smem<=8388608 / blank=4 / com=4 / gw=0x80000000 / pos=0`，
  且 channel ioctl `0x80044625` 返回 `0x0a6ff7fe`。
- `restore()` 顺序：`FBIOBLANK(4)`×2 → `setmode(0)` → 校验几何 → 回写备份像素并比对 hash →
  `KEY={0,0}`（fg）→ `ALPHA={1,128}`（**bg**，不是 fg）→ `FBIOBLANK(4)` → 校验 blank/com/gw/pos。
- 备份：`/tmp/<root>/original.pixels`（fb1 smem_len 字节）+ `original.state`（saved 结构体，含 var/fix/blank/com/gw/pos/hash）。
- `show` 里 `expected()` 不通过直接返回 `instrument_busy`，**一个字节都不写**。
