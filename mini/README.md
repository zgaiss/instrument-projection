# fbctl —— 仪表前景层投放验证工具（最小 demo 固化版）

本目录是从「能不能把内容投到仪表」这个问题的**实测产物**，不是原厂方案的复刻。
所有结论都来自这台车（E28U / 仪表 Linux 3.0.35 armv7l / 飞思卡尔 i.MX6）上的实际运行。

## 结论先说

| 能力 | 状态 | 证据 |
|---|---|---|
| 往 fb1 前景层画固定图案并显示 | ✅ 已验证 | 240×320 图案可见，像素回读 `ff ff…` |
| 切到 1920×720 全屏并显示 | ✅ 已验证 | `WIDE 1920x720 stride=3840 smem=2764800`，中心像素 `0xf81f` 命中测试卡第 5 段彩条 |
| 到点自动还原（含像素/几何/寄存器） | ✅ 已验证 | `RESTORED_FNV` 与备份 hash 一致、`com` 回 4、`blank` 回 4 |
| 把**大屏地图**投上去 | ❌ 未实现 | 缺帧来源，见文末 |

## 用法

```bash
fbctl p             # 只读报告，不写任何东西
fbctl h [秒]        # 240×320 前景层测试卡（默认 60s）
fbctl fs [秒]       # 1920×720 全屏测试卡（默认 60s，到点自动还原）
fbctl r             # 从 /tmp/dt/original.* 手工恢复（进程被杀时用）
```

## 部署（从 Mac 到仪表）

```bash
./deploy.sh          # 编译 + adb push + nc 直传 + chmod
```

`deploy.sh` 做的事，以及每一步的坑：

1. `GOOS=linux GOARCH=arm GOARM=7 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w"`
   —— 本机 clang 无法链接 ARM ELF（Apple ld64 不支持，且没有 lld），Go 自带链接器是唯一可行路径。
2. `adb push` 到中控 `/data/local/tmp/dt/`。
3. **中控监听、仪表主动拉取**：`adb shell "nc -l -p 9996 < <file>"`。
4. 仪表侧：`( sleep 8 ) | nc 172.20.1.45 9996 > /tmp/dt/fbctl`。

### 四个必须记住的坑

- **仪表侧 stdin 在 RPC 里已经是 EOF**，`nc` 会边接边退，收到 0 字节。必须用 `( sleep N )` 顶住。
- **方向要选对**：反过来（仪表监听、中控推）在 busybox `nc -l` 上会把 1.7 MB 截断成 450 KB。
- **30510 单次命令不要超过 ~2.5 s**：超时只是客户端看不到回包，命令本身会继续跑；长任务要 `( ... ) &` 放后台，再另发短命令查日志。
- **不要并发调用 `instr-rpc.py`**：它复用中控上的 `req.bin/resp.bin`，并发会读到上一次的响应（表现为"返回了别的内容"）。

## 这台车实测到的基线参数

```
FB1  xres=240 yres=320 vx=240 vy=960 bpp=16 stride=480
     modes 只有 U:240x320p-60，virtual_size=240,960，blank=4
     D:240x320 / 5-6-5（red.offset=11 green=5 blue=0）
FB0  xres=1920 yres=720 bpp=32 stride=7680（ECluster 背景层）
REG  com=4  gw=0x80000000  pos=0     @ 0x02740000
CH   channel ioctl 0x80044625 = 0x0a6ff7ff
```

`/dev/mem` **只能 mmap 读，不能用 read/dd**：`dd` 走 read 路径会返回 `Bad address`，
但 `busybox devmem` 和 mmap 都正常。寄存器必须用 mmap 方式读。

## 全屏是怎么切的（照抄 ultra_app_receiver.c 的 setmode）

1. 把 2 个 `struct video_mode`（56 B/个，共 112 B）写入 `/sys/class/graphics/fb1/modes`：
   m[0] 由 fb1 当前 var 生成，m[1] 由 **fb0 的 var** 生成（所以全屏分辨率等于背景层）。
2. 向 `/sys/class/graphics/fb1/mode` 写 `"U:1920x720p-60\n"`。
3. 校验：`1920x720 / vx=1920 / vy=720 / bpp=16 / stride=3840 / smem>=2764800`。
4. 还原：写 1 个 video_mode（56 B）→ `"U:240x320p-60\n"` → `virtual_size="240,960\n"`。

## 已知的副作用与风险

- **DMA 容量不会降回去**：还原后 `smem` 从 460800 变成 2764800，原厂代码里也注明
  `NOTE_DMA_CAPACITY_RETAINED_UNTIL_DRIVER_REINIT`。厂商 `expected()` 允许 460800–8388608，所以不影响判断。
- `com` 残留问题：只做 blank/key/alpha 而**不切模式**时，`com` 会从 4 变成 6（alpha-enable 位残留），
  且不自愈；走一次全屏往返后自动回到 4。原厂 `restore()` 也是靠 `setmode(0)` 复位这一位。
- 还原时 `ALPHA` 写的是 **bg（fb0）**，不是 fg —— 这是原厂基线（BG 全局 alpha=128）。
- **兜底**：进程若被杀，`/tmp/dt/original.state` + `original.pixels` 还在，跑 `fbctl r` 即可；
  再不行仪表断电重启（`/tmp` 清空、fb1 回 baseline）。

## 还缺什么才能投「大屏地图」

全屏图层这一关已经通了，缺的是**画面来源**：

- 原方案的帧来自中控 CarPlay ALT 解码器 → `UltraProjectionRunner` 取帧 → 压缩 → 49173。
  这三份源码里**没有取帧那一段**，解码器在车厂侧。
- 车机自带地图是独立 App，要拿它的画面得走 `SurfaceControl.captureLayers` / `MediaProjection`
  这类通道，Android 12 上各有权限与包含范围的限制。

也就是说：**"能不能显示"已经不是问题，"画面从哪来"才是。**
