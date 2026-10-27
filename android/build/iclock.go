// iclock.go —— 仪表全屏色块 + 倒计时（armv7 静态二进制）
//
// 用法：
//   iclock <秒数>   全屏投放，显示色块与 MM:SS 倒计时，到点自动还原（默认 120）
//   iclock r        从 /tmp/iclock/original.* 手工恢复
//   iclock p        只读探测
//
// 与 fbctl v2 的关系：显示路径、备份/恢复、混合态校验全部沿用已验证实现，
// 新增每秒重绘的数字区域（七段数码管）与 SIGTERM 捕获。
//
// 写入范围：/dev/fb1、/sys/class/graphics/fb1/{modes,mode,virtual_size}、显示寄存器（只读）、
//          /tmp/iclock/{original.pixels,original.state,pid}
package main

import (
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
	"unsafe"
)

const (
	oRDWR = 2

	fbioGetVScreen = 0x4600
	fbioGetFScreen = 0x4602
	fbioBlank      = 0x4611
	ioAlpha        = 0x40084621
	ioKey          = 0x40084622
	ioGetBlank     = 0x8004462B
	ioChannel      = 0x80044625

	regBase = 0x02740000
	root    = "/tmp/iclock/"
	magicID = 0x49434C4B

	screenW = 1920
	screenH = 720
)

type bitfield struct{ Offset, Length, MsbRight uint32 }

type varScreenInfo struct {
	Xres, Yres, XresVirtual, YresVirtual uint32
	Xoffset, Yoffset                     uint32
	BitsPerPixel, Grayscale              uint32
	Red, Green, Blue, Transp             bitfield
	Nonstd, Activate                     uint32
	Height, Width, AccelFlags            uint32
	Pixclock, Left, Right, Upper, Lower  uint32
	HsyncLen, VsyncLen, Sync, Vmode      uint32
	Rotate, Colorspace                   uint32
	Reserved                             [4]uint32
}

type fixScreenInfo struct {
	ID         [16]byte
	SmemStart  uint32
	SmemLen    uint32
	Type       uint32
	TypeAux    uint32
	Visual     uint32
	Xpanstep   uint16
	Ypanstep   uint16
	Ywrapstep  uint16
	LineLength uint32
	MmioStart  uint32
	MmioLen    uint32
	Accel      uint32
	Reserved   [3]uint16
}

type videoMode struct {
	Name, Refresh, Xres, Yres, Pixclock uint32
	Left, Right, Upper, Lower           uint32
	Hsync, Vsync, Sync, Vmode, Flag     uint32
}

type saved struct {
	Magic uint32
	V     varScreenInfo
	F     fixScreenInfo
	Blank uint32
	Com   uint32
	Gw    uint32
	Pos   uint32
	Hash  uint32
}

var (
	fgFd, bgFd int
	regs       []byte

	fb     []byte
	stride int
	wide   bool
	state  saved
	backup []byte
)

// ── 基础 ──────────────────────────────────────────────────────
func ioctl(fd int, op uintptr, arg unsafe.Pointer) error {
	for {
		_, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), op, uintptr(arg))
		if e == syscall.EINTR {
			continue
		}
		if e != 0 {
			return e
		}
		return nil
	}
}

func getMode(fd int, v *varScreenInfo, f *fixScreenInfo) error {
	if err := ioctl(fd, fbioGetVScreen, unsafe.Pointer(v)); err != nil {
		return err
	}
	return ioctl(fd, fbioGetFScreen, unsafe.Pointer(f))
}

func reg(i int) uint32 { return *(*uint32)(unsafe.Pointer(&regs[i*4])) }

func rgb(r, g, b uint32) uint16 { return uint16((r&0x1f)<<11 | (g&0x3f)<<5 | (b & 0x1f)) }

func hash(p []byte) uint32 {
	h := uint32(2166136261)
	for _, b := range p {
		h = (h ^ uint32(b)) * 16777619
	}
	return h
}

func bytesOf(p unsafe.Pointer, n int) []byte { return unsafe.Slice((*byte)(p), n) }

func sysWrite(path string, b []byte, zeroOK bool) error {
	fd, err := syscall.Open(path, syscall.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("open %s: %v", path, err)
	}
	defer syscall.Close(fd)
	n, err := syscall.Write(fd, b)
	if err != nil {
		return fmt.Errorf("write %s: %v", path, err)
	}
	if n != len(b) && !(zeroOK && n == 0) {
		return fmt.Errorf("short write %s: %d/%d", path, n, len(b))
	}
	return nil
}

func videoOf(v *varScreenInfo) videoMode {
	return videoMode{
		Name: 0, Refresh: 60,
		Xres: v.Xres, Yres: v.Yres, Pixclock: v.Pixclock,
		Left: v.Left, Right: v.Right, Upper: v.Upper, Lower: v.Lower,
		Hsync: v.HsyncLen, Vsync: v.VsyncLen, Sync: v.Sync, Vmode: v.Vmode, Flag: 0,
	}
}

func setMode(toWide bool, oldV, bgV *varScreenInfo) error {
	if toWide {
		m := [2]videoMode{videoOf(oldV), videoOf(bgV)}
		if err := sysWrite("/sys/class/graphics/fb1/modes",
			bytesOf(unsafe.Pointer(&m[0]), 112), true); err != nil {
			return err
		}
		return sysWrite("/sys/class/graphics/fb1/mode", []byte("U:1920x720p-60\n"), false)
	}
	m := [1]videoMode{videoOf(oldV)}
	if err := sysWrite("/sys/class/graphics/fb1/modes",
		bytesOf(unsafe.Pointer(&m[0]), 56), true); err != nil {
		return err
	}
	if err := sysWrite("/sys/class/graphics/fb1/mode", []byte("U:240x320p-60\n"), false); err != nil {
		return err
	}
	return sysWrite("/sys/class/graphics/fb1/virtual_size", []byte("240,960\n"), false)
}

// ── 像素写入 ───────────────────────────────────────────────────
// FG 的 key_value = 0，任何 0 值像素都会被当成透明挖掉，所以统一抬到 1。
func put(x, y int, c uint16) {
	if x < 0 || y < 0 || x >= screenW || y >= screenH {
		return
	}
	if c == 0 {
		c = 1
	}
	o := y*stride + x*2
	fb[o] = byte(c)
	fb[o+1] = byte(c >> 8)
}

func rect(x0, y0, x1, y1 int, c uint16) {
	for y := y0; y <= y1; y++ {
		for x := x0; x <= x1; x++ {
			put(x, y, c)
		}
	}
}

// 背景：深蓝竖向渐变，越往下越亮，方便肉眼判断有没有被拉伸
func bgAt(x, y int) uint16 {
	if x < 0 || y < 0 || x >= screenW || y >= screenH {
		return 1
	}
	t := uint32(y)
	return rgb(2+(t*6)/screenH, 8+(t*14)/screenH, 22+(t*30)/screenH)
}

func fillBg(x0, y0, x1, y1 int) {
	for y := y0; y <= y1; y++ {
		for x := x0; x <= x1; x++ {
			put(x, y, bgAt(x, y))
		}
	}
}

// ── 七段数码管 ─────────────────────────────────────────────────
//      a
//     f b
//      g
//     e c
//      d
var segMask = [10]uint8{
	0b0111111, // 0  a b c d e f
	0b0000110, // 1  b c
	0b1011011, // 2  a b d e g
	0b1001111, // 3  a b c d g
	0b1100110, // 4  b c f g
	0b1101101, // 5  a c d f g
	0b1111101, // 6  a c d e f g
	0b0000111, // 7  a b c
	0b1111111, // 8
	0b1101111, // 9  a b c d f g
}

const (
	segA uint8 = 1 << 0
	segB uint8 = 1 << 1
	segC uint8 = 1 << 2
	segD uint8 = 1 << 3
	segE uint8 = 1 << 4
	segF uint8 = 1 << 5
	segG uint8 = 1 << 6
)

// drawDigit 在 (x,y) 画一个宽 w、高 h、笔画厚 t 的数字
func drawDigit(d int, x, y, w, h, t int, c uint16) {
	if d < 0 || d > 9 {
		return
	}
	m := segMask[d]
	h2 := h / 2
	// 横向段：左右各缩进 t/2，避免与竖段重叠出毛刺
	if m&segA != 0 {
		rect(x+t/2, y, x+w-t/2, y+t-1, c)
	}
	if m&segG != 0 {
		rect(x+t/2, y+h2-t/2, x+w-t/2, y+h2+t/2-1, c)
	}
	if m&segD != 0 {
		rect(x+t/2, y+h-t, x+w-t/2, y+h-1, c)
	}
	if m&segF != 0 {
		rect(x, y+t/2, x+t-1, y+h2-t/2, c)
	}
	if m&segB != 0 {
		rect(x+w-t, y+t/2, x+w-1, y+h2-t/2, c)
	}
	if m&segE != 0 {
		rect(x, y+h2+t/2, x+t-1, y+h-t/2, c)
	}
	if m&segC != 0 {
		rect(x+w-t, y+h2+t/2, x+w-1, y+h-t/2, c)
	}
}

// ── 画面布局 ───────────────────────────────────────────────────
const (
	numH    = 230
	numW    = 138
	numT    = 28
	numY    = 96
	numGap  = 34
	colonW  = 62
	dirtyY0 = 88
	dirtyY1 = 348
)

func numAreaWidth() int {
	// 两位 : 两位
	return numW*4 + colonW + numGap*4
}

func drawColon(x, y int, c uint16) {
	rect(x+18, y+numH*3/10-14, x+18+26, y+numH*3/10+12, c)
	rect(x+18, y+numH*7/10-14, x+18+26, y+numH*7/10+12, c)
}

// drawCountdown 只重绘数字区域，sec 为剩余秒数
func drawCountdown(sec int) {
	x0 := (screenW - numAreaWidth()) / 2
	x1 := x0 + numAreaWidth()
	fillBg(x0-8, dirtyY0, x1+8, dirtyY1)

	c := rgb(31, 63, 12) // 亮黄绿
	if sec <= 10 {
		c = rgb(31, 10, 8) // 最后十秒转红
	}
	if sec < 0 {
		sec = 0
	}
	m, s := sec/60, sec%60
	digits := [4]int{m / 10, m % 10, s / 10, s % 10}

	x := x0
	for i, d := range digits {
		drawDigit(d, x, numY, numW, numH, numT, c)
		x += numW + numGap
		if i == 1 {
			drawColon(x-numGap+8, numY, c)
			x += colonW
		}
	}
}

// progress bar：剩余比例
func drawProgress(sec, total int) {
	y0, y1 := 360, 372
	x0, x1 := 200, screenW-200
	rect(x0, y0, x1, y1, rgb(4, 10, 24))
	w := 0
	if total > 0 {
		w = (x1 - x0) * sec / total
	}
	if w > 0 {
		rect(x0, y0, x0+w, y1, rgb(6, 40, 18))
	}
}

var barColors = [8]uint16{
	rgb(31, 63, 31), // 白
	rgb(31, 63, 0),  // 黄
	rgb(0, 63, 31),  // 青
	rgb(0, 63, 0),   // 绿
	rgb(31, 0, 31),  // 品红
	rgb(31, 0, 0),   // 红
	rgb(0, 0, 31),   // 蓝
	rgb(8, 16, 8),   // 灰
}

func drawStatic() {
	// 边框：3px 白框，一眼看出是否铺满
	white := rgb(31, 63, 31)
	rect(0, 0, screenW-1, 2, white)
	rect(0, screenH-3, screenW-1, screenH-1, white)
	rect(0, 0, 2, screenH-1, white)
	rect(screenW-3, 0, screenW-1, screenH-1, white)

	// 8 段彩条
	by0, by1 := 420, 660
	bw := screenW / len(barColors)
	for i, c := range barColors {
		rect(i*bw, by0, (i+1)*bw-1, by1, c)
		// 段间留一条暗线，方便数段
		rect((i+1)*bw-2, by0, (i+1)*bw-1, by1, rgb(3, 6, 14))
	}
	// 彩条上下 2px 白线
	rect(0, by0-3, screenW-1, by0-2, white)
	rect(0, by1+2, screenW-1, by1+3, white)

	// 底部刻度尺，每 60px 一格，每 480px 长刻度
	for x := 0; x < screenW; x += 60 {
		l := 10
		if x%480 == 0 {
			l = 28
		}
		for y := screenH - 4; y > screenH-4-l; y-- {
			put(x, y, white)
		}
	}

	// 左右两个定位块，用于确认没有横向裁切
	rect(20, 400, 60, 416, rgb(31, 0, 31))
	rect(screenW-61, 400, screenW-21, 416, rgb(31, 0, 31))
}

// ── 备份 / 恢复 ────────────────────────────────────────────────
func saveState(s *saved, pixels []byte) error {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(root+"original.pixels", pixels, 0o600); err != nil {
		return err
	}
	return os.WriteFile(root+"original.state",
		bytesOf(unsafe.Pointer(s), int(unsafe.Sizeof(*s))), 0o600)
}

func loadState(s *saved) ([]byte, error) {
	b, err := os.ReadFile(root + "original.state")
	if err != nil {
		return nil, err
	}
	if len(b) != int(unsafe.Sizeof(*s)) {
		return nil, fmt.Errorf("state size %d != %d", len(b), unsafe.Sizeof(*s))
	}
	copy(bytesOf(unsafe.Pointer(s), len(b)), b)
	return os.ReadFile(root + "original.pixels")
}

func restore(s *saved, pixels []byte, toWide bool) int {
	fmt.Println("RESTORING")
	ok := true

	ioctl(fgFd, fbioBlank, unsafe.Pointer(uintptr(4)))
	if toWide {
		if err := setMode(false, &s.V, &s.V); err != nil {
			fmt.Println("ERROR RESTORE_MODE", err)
			ok = false
		}
	}

	var v varScreenInfo
	var f fixScreenInfo
	if err := getMode(fgFd, &v, &f); err != nil {
		fmt.Println("ERROR RESTORE_GETMODE", err)
		ok = false
	} else {
		fmt.Printf("AFTER_RESTORE_GEOM xres=%d yres=%d vx=%d vy=%d bpp=%d stride=%d smem=%d\n",
			v.Xres, v.Yres, v.XresVirtual, v.YresVirtual, v.BitsPerPixel, f.LineLength, f.SmemLen)
		if v.Xres != s.V.Xres || v.Yres != s.V.Yres ||
			v.XresVirtual != s.V.XresVirtual || v.YresVirtual != s.V.YresVirtual ||
			v.BitsPerPixel != s.V.BitsPerPixel ||
			f.LineLength != s.F.LineLength || f.SmemLen < s.F.SmemLen || f.SmemLen > 8388608 {
			fmt.Println("RESTORE_GEOMETRY_MISMATCH_NO_PIXEL_WRITE")
			ok = false
		} else if p, err := syscall.Mmap(fgFd, 0, int(f.SmemLen),
			syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED); err == nil {
			n := int(s.F.SmemLen)
			copy(p[:n], pixels[:n])
			got := hash(p[:n])
			fmt.Printf("RESTORED_FNV=%d expected=%d\n", got, s.Hash)
			if got != s.Hash {
				ok = false
			}
			syscall.Munmap(p)
		} else {
			fmt.Println("ERROR RESTORE_MMAP", err)
			ok = false
		}
	}

	key := [2]int32{0, 0}
	alpha := [2]int32{1, 128}
	if err := ioctl(fgFd, ioKey, unsafe.Pointer(&key)); err != nil {
		fmt.Println("ERROR RESTORE_KEY", err)
		ok = false
	}
	if err := ioctl(bgFd, ioAlpha, unsafe.Pointer(&alpha)); err != nil {
		fmt.Println("ERROR RESTORE_ALPHA_BG", err)
		ok = false
	}
	if err := ioctl(fgFd, fbioBlank, unsafe.Pointer(uintptr(4))); err != nil {
		fmt.Println("ERROR FINAL_BLANK", err)
		ok = false
	}
	time.Sleep(time.Second)

	var blank uint32 = 99
	if err := ioctl(fgFd, ioGetBlank, unsafe.Pointer(&blank)); err != nil || blank != s.Blank {
		ok = false
	}
	fmt.Printf("RESTORED blank=%d com=%d gw=0x%08x pos=%d\n", blank, reg(0), reg(1), reg(2))
	if reg(0) != s.Com || reg(1) != s.Gw || reg(2) != s.Pos {
		fmt.Println("RESTORE_REGISTER_MISMATCH")
	}
	if ok {
		fmt.Println("RESTORE_OK")
		return 0
	}
	fmt.Println("RESTORE_FAILED")
	return 9
}

// ── main ──────────────────────────────────────────────────────
func main() {
	if unsafe.Sizeof(varScreenInfo{}) != 160 || unsafe.Sizeof(fixScreenInfo{}) != 68 {
		fmt.Println("ABI_MISMATCH")
		os.Exit(9)
	}
	os.MkdirAll(root, 0o755)

	arg := "p"
	hold := 120
	if len(os.Args) > 1 {
		arg = os.Args[1]
	}
	if n, err := strconv.Atoi(arg); err == nil {
		if n > 0 && n <= 7200 {
			hold = n
		}
		arg = "run"
	}

	// dump：本机渲染一帧到 PPM，用于在不动硬件的前提下校验版式
	if arg == "dump" {
		stride = screenW * 2
		fb = make([]byte, stride*screenH)
		fillBg(0, 0, screenW-1, screenH-1)
		drawStatic()
		drawCountdown(hold)
		drawProgress(hold, hold)
		out, _ := os.Create("iclock.ppm")
		fmt.Fprintf(out, "P6\n%d %d\n255\n", screenW, screenH)
		buf := make([]byte, screenW*3)
		for y := 0; y < screenH; y++ {
			for x := 0; x < screenW; x++ {
				o := y*stride + x*2
				v := uint16(fb[o]) | uint16(fb[o+1])<<8
				r, g, b := (v>>11)&0x1f, (v>>5)&0x3f, v&0x1f
				buf[x*3] = byte(r * 255 / 31)
				buf[x*3+1] = byte(g * 255 / 63)
				buf[x*3+2] = byte(b * 255 / 31)
			}
			out.Write(buf)
		}
		out.Close()
		fmt.Println("DUMPED iclock.ppm")
		return
	}
	change := arg == "run" || arg == "r"
	wide = arg == "run"

	flags := syscall.O_RDONLY
	if change {
		flags = oRDWR
	}
	var err error
	if fgFd, err = syscall.Open("/dev/fb1", flags, 0); err != nil {
		fmt.Println("ERROR OPEN_FG", err)
		os.Exit(2)
	}
	defer syscall.Close(fgFd)
	if bgFd, err = syscall.Open("/dev/fb0", flags, 0); err != nil {
		fmt.Println("ERROR OPEN_BG", err)
		os.Exit(2)
	}
	defer syscall.Close(bgFd)

	mem, err := syscall.Open("/dev/mem", syscall.O_RDONLY, 0)
	if err != nil {
		fmt.Println("ERROR OPEN_MEM", err)
		os.Exit(2)
	}
	m, err := syscall.Mmap(mem, regBase, 4096, syscall.PROT_READ, syscall.MAP_SHARED)
	syscall.Close(mem)
	if err != nil {
		fmt.Println("ERROR MMAP_REG", err)
		os.Exit(2)
	}
	regs = m

	var fgV varScreenInfo
	var fgF fixScreenInfo
	if err := getMode(fgFd, &fgV, &fgF); err != nil {
		fmt.Println("ERROR GET_FG", err)
		os.Exit(2)
	}
	var bgV varScreenInfo
	var bgF fixScreenInfo
	if err := getMode(bgFd, &bgV, &bgF); err != nil {
		fmt.Println("ERROR GET_BG", err)
		os.Exit(2)
	}
	var blank uint32 = 99
	ioctl(fgFd, ioGetBlank, unsafe.Pointer(&blank))
	var channel uint32
	ioctl(fgFd, ioChannel, unsafe.Pointer(&channel))

	fmt.Printf("FG xres=%d yres=%d vx=%d vy=%d bpp=%d stride=%d smem=%d\n",
		fgV.Xres, fgV.Yres, fgV.XresVirtual, fgV.YresVirtual, fgV.BitsPerPixel, fgF.LineLength, fgF.SmemLen)
	fmt.Printf("STATE blank=%d com=%d gw=0x%08x pos=%d channel=0x%08x\n",
		blank, reg(0), reg(1), reg(2), channel)

	if arg == "p" {
		fmt.Println("PROBE_ONLY")
		return
	}

	if arg == "r" {
		var s saved
		px, err := loadState(&s)
		if err != nil || s.Magic != magicID || hash(px) != s.Hash {
			fmt.Println("ERROR NO_VALID_BACKUP")
			os.Exit(2)
		}
		fmt.Printf("RECOVER_FROM_BACKUP smem=%d hash=%d\n", s.F.SmemLen, s.Hash)
		os.Exit(restore(&s, px, fgV.Xres != 240))
	}

	// 基线校验
	bad := ""
	if fgV.Xres != 240 || fgV.Yres != 320 || fgV.BitsPerPixel != 16 ||
		fgV.XresVirtual != 240 || fgV.YresVirtual != 960 ||
		fgF.LineLength != 480 || fgF.SmemLen < 460800 || fgF.SmemLen > 8388608 {
		bad = "GEOMETRY"
	}
	if blank != 4 {
		bad = "BLANK"
	}
	if bad != "" {
		fmt.Println("REFUSED_BASELINE_" + bad)
		os.Exit(3)
	}

	state = saved{Magic: magicID, V: fgV, F: fgF, Blank: blank, Com: reg(0), Gw: reg(1), Pos: reg(2)}

	orig, err := syscall.Mmap(fgFd, 0, int(fgF.SmemLen), syscall.PROT_READ, syscall.MAP_SHARED)
	if err != nil {
		fmt.Println("ERROR MMAP_BACKUP", err)
		os.Exit(4)
	}
	backup = make([]byte, fgF.SmemLen)
	copy(backup, orig)
	syscall.Munmap(orig)
	state.Hash = hash(backup)
	if err := saveState(&state, backup); err != nil {
		fmt.Println("ERROR SAVE_BACKUP", err)
		os.Exit(4)
	}
	fmt.Printf("BACKUP smem=%d hash=%d\n", fgF.SmemLen, state.Hash)

	if err := setMode(true, &fgV, &bgV); err != nil {
		fmt.Println("ERROR SETMODE_WIDE", err)
		restore(&state, backup, true)
		os.Exit(5)
	}
	var v varScreenInfo
	var f fixScreenInfo
	if err := getMode(fgFd, &v, &f); err != nil {
		fmt.Println("ERROR GET_WIDE", err)
		restore(&state, backup, true)
		os.Exit(5)
	}
	fmt.Printf("WIDE xres=%d yres=%d stride=%d smem=%d\n", v.Xres, v.Yres, f.LineLength, f.SmemLen)
	if v.Xres != screenW || v.Yres != screenH || v.BitsPerPixel != 16 ||
		f.LineLength != screenW*2 || f.SmemLen < screenW*screenH*2 {
		fmt.Println("REFUSED_WIDE_GEOMETRY")
		restore(&state, backup, true)
		os.Exit(6)
	}
	stride = int(f.LineLength)

	fb, err = syscall.Mmap(fgFd, 0, int(f.SmemLen), syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
	if err != nil {
		fmt.Println("ERROR MMAP_FB", err)
		restore(&state, backup, true)
		os.Exit(7)
	}

	fillBg(0, 0, screenW-1, screenH-1)
	drawStatic()
	drawCountdown(hold)
	drawProgress(hold, hold)

	alpha := [2]int32{1, 255}
	key := [2]int32{1, 0}
	if err := ioctl(fgFd, ioAlpha, unsafe.Pointer(&alpha)); err != nil {
		fmt.Println("ERROR FG_ALPHA", err)
		restore(&state, backup, true)
		os.Exit(8)
	}
	if err := ioctl(fgFd, ioKey, unsafe.Pointer(&key)); err != nil {
		fmt.Println("ERROR BLACK_KEY", err)
		restore(&state, backup, true)
		os.Exit(8)
	}
	fmt.Printf("BLEND com=%d gw=0x%08x\n", reg(0)&15, reg(1))
	if reg(0)&15 != 14 || reg(1) != 0xff000000 || reg(2) != 0 {
		fmt.Println("REFUSED_BLEND_STATE")
		restore(&state, backup, true)
		os.Exit(8)
	}
	if err := ioctl(fgFd, fbioBlank, unsafe.Pointer(uintptr(0))); err != nil {
		fmt.Println("ERROR ENABLE", err)
		restore(&state, backup, true)
		os.Exit(8)
	}
	fmt.Printf("AFTER_ENABLE com=%d gw=0x%08x\n", reg(0)&15, reg(1))
	if reg(0)&15 != 15 || reg(1) != 0xff000000 || reg(2) != 0 {
		fmt.Println("REFUSED_ACTIVE_BLEND_STATE")
		restore(&state, backup, true)
		os.Exit(8)
	}

	os.WriteFile(root+"pid", []byte(strconv.Itoa(os.Getpid())), 0o644)
	fmt.Printf("VISIBLE %dx%d hold=%ds\n", screenW, screenH, hold)
	os.Stdout.Sync()

	// SIGTERM/SIGINT: 提前结束也要走完整恢复
	stop := make(chan int, 1)
	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	go func() {
		<-sigc
		stop <- 1
	}()

	start := time.Now()
	deadline := start.Add(time.Duration(hold) * time.Second)
	elapsed := 0
	for {
		select {
		case <-stop:
			fmt.Println("SIGNAL_STOP")
			syscall.Munmap(fb)
			os.Exit(restore(&state, backup, true))
		default:
		}
		now := time.Now()
		left := int(deadline.Sub(now).Seconds() + 0.5)
		if left <= 0 {
			break
		}
		if left != elapsed {
			elapsed = left
			drawCountdown(left)
			drawProgress(left, hold)
			fmt.Printf("TICK %02d:%02d\n", left/60, left%60)
			os.Stdout.Sync()
		}
		time.Sleep(100 * time.Millisecond)
	}

	syscall.Munmap(fb)
	fmt.Println("COUNTDOWN_DONE")
	os.Exit(restore(&state, backup, true))
}
