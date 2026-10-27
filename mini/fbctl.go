// fbctl.go —— 仪表前景层（fb1）投放验证工具 v2
//
// 与 v1（drawtest.go）的差别：
//   1. 投放前把 fb1 的 var/fix/blank/com/gw/pos 与整块像素备份到 /tmp/dt/，
//      退出时按 ultra_app_receiver.c 的 restore() 顺序完整还原（含几何校验 + hash 比对）；
//   2. ALPHA 按源码作用在 bg（fb0）上，而不是 fg —— v1 弄错 fd，会残留 com 的 alpha-enable 位；
//   3. 新增 fs 模式：按 setmode(1) 把 fb1 切到 1920x720 全屏，投一张测试卡。
//
// 用法：
//   fbctl p            只读报告（不写任何东西）
//   fbctl h [秒]       240x320 前景层测试（默认 60 秒）
//   fbctl fs [秒]      1920x720 全屏测试（默认 60 秒，超时自动还原）
//   fbctl r            从 /tmp/dt/original.* 手工恢复
package main

import (
	"fmt"
	"os"
	"strconv"
	"syscall"
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
	root    = "/tmp/dt/"
	magicID = 0x57494445
)

type bitfield struct {
	Offset, Length, MsbRight uint32
}

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
)

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

// ── sysfs 写入 ────────────────────────────────────────────────
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

// setMode 完全照抄 ultra_app_receiver.c 的 setmode()
func setMode(wide bool, oldV, bgV *varScreenInfo) error {
	if wide {
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

// ── 测试卡 ────────────────────────────────────────────────────
func testCard(p []byte, w, h, stride int) {
	for i := 0; i < stride*h && i < len(p); i++ {
		p[i] = 0
	}
	set := func(x, y int, c uint16) {
		if x < 0 || y < 0 || x >= w || y >= h {
			return
		}
		row := p[y*stride:]
		row[x*2] = byte(c)
		row[x*2+1] = byte(c >> 8)
	}
	rect := func(x0, y0, x1, y1 int, c uint16) {
		for y := y0; y <= y1; y++ {
			for x := x0; x <= x1; x++ {
				set(x, y, c)
			}
		}
	}

	// 深蓝渐变底
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			set(x, y, rgb(uint32(x*31/w), uint32(y*12/h), uint32(8+y*23/h)))
		}
	}
	white := rgb(31, 63, 31)
	// 3px 白色边框：一眼判断是否铺满整屏
	rect(0, 0, w-1, 2, white)
	rect(0, h-3, w-1, h-1, white)
	rect(0, 0, 2, h-1, white)
	rect(w-3, 0, w-1, h-1, white)

	// 网格：每 120x90 一格；每 480px 一条亮线，用来判断拉伸是否等比
	dim := rgb(3, 8, 4)
	bright := rgb(9, 20, 10)
	for x := 0; x < w; x += 120 {
		c := dim
		if x%480 == 0 {
			c = bright
		}
		for y := 0; y < h; y++ {
			set(x, y, c)
		}
	}
	for y := 0; y < h; y += 90 {
		for x := 0; x < w; x++ {
			set(x, y, dim)
		}
	}

	// 中心十字 + 圆环
	cx, cy := w/2, h/2
	for x := cx - 80; x <= cx+80; x++ {
		set(x, cy, white)
		set(x, cy+1, white)
	}
	for y := cy - 80; y <= cy+80; y++ {
		set(cx, y, white)
		set(cx+1, y, white)
	}
	for a := 0; a < 720; a++ {
		r := float64(a) * 3.141592653589793 / 360.0
		set(cx+int(90*cosT(r)), cy+int(90*sinT(r)), rgb(31, 63, 0))
	}

	// 四角色块：TL 红 / TR 绿 / BL 蓝 / BR 黄
	rect(16, 16, 175, 115, rgb(31, 0, 0))
	rect(w-176, 16, w-17, 115, rgb(0, 63, 0))
	rect(16, h-116, 175, h-17, rgb(0, 0, 31))
	rect(w-176, h-116, w-17, h-17, rgb(31, 63, 0))

	// 中部 8 段彩条
	bars := []uint16{
		rgb(31, 63, 31), rgb(31, 63, 0), rgb(0, 63, 31), rgb(0, 63, 0),
		rgb(31, 0, 31), rgb(31, 0, 0), rgb(0, 0, 31), rgb(0, 0, 0),
	}
	bw := w / len(bars)
	for i, c := range bars {
		rect(i*bw, cy-60, (i+1)*bw-1, cy+59, c)
	}

	// 底部刻度尺
	for x := 0; x < w; x += 60 {
		l := 10
		if x%480 == 0 {
			l = 30
		}
		for y := h - 4; y > h-4-l; y-- {
			set(x, y, white)
		}
	}
}

func cosT(r float64) float64 { return cos(r) }
func sinT(r float64) float64 { return cos(r - 1.5707963267948966) }

// 泰勒展开，精度足够画圆，且不引入 math 包的额外体积
func cos(r float64) float64 {
	const twoPi, pi = 6.283185307179586, 3.141592653589793
	for r > pi {
		r -= twoPi
	}
	for r < -pi {
		r += twoPi
	}
	s, t := 1.0, 1.0
	for n := 1.0; n <= 13; n += 2 {
		t *= -r * r / ((n + 1) * (n + 2))
		s += t
	}
	return s
}

// ── 备份 / 恢复 ────────────────────────────────────────────────
func saveState(s *saved, pixels []byte) error {
	if err := os.WriteFile(root+"original.pixels", pixels, 0o600); err != nil {
		return err
	}
	return os.WriteFile(root+"original.state", bytesOf(unsafe.Pointer(s), int(unsafe.Sizeof(*s))), 0o600)
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

func restore(s *saved, pixels []byte, wide bool) int {
	fmt.Println("RESTORING")
	ok := true

	if err := ioctl(fgFd, fbioBlank, unsafe.Pointer(uintptr(4))); err != nil {
		ioctl(fgFd, fbioBlank, unsafe.Pointer(uintptr(4)))
	}
	if wide {
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
			v.BitsPerPixel != s.V.BitsPerPixel || v.Xoffset != s.V.Xoffset || v.Yoffset != s.V.Yoffset ||
			f.LineLength != s.F.LineLength || f.SmemLen < s.F.SmemLen || f.SmemLen > 8388608 {
			fmt.Println("RESTORE_GEOMETRY_MISMATCH_NO_PIXEL_WRITE")
			ok = false
		} else {
			if p, err := syscall.Mmap(fgFd, 0, int(f.SmemLen), syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED); err == nil {
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
		if f.SmemLen != s.F.SmemLen {
			fmt.Printf("NOTE_DMA_CAPACITY_CHANGED old=%d now=%d\n", s.F.SmemLen, f.SmemLen)
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
	syscall.Nanosleep(&syscall.Timespec{Sec: 1}, nil)

	var blank uint32 = 99
	if err := ioctl(fgFd, ioGetBlank, unsafe.Pointer(&blank)); err != nil || blank != s.Blank {
		ok = false
	}
	fmt.Printf("RESTORED blank=%d com=%d gw=0x%08x pos=%d\n", blank, reg(0), reg(1), reg(2))
	fmt.Printf("BASELINE blank=%d com=%d gw=0x%08x pos=%d\n", s.Blank, s.Com, s.Gw, s.Pos)
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
	if unsafe.Sizeof(varScreenInfo{}) != 160 || unsafe.Sizeof(fixScreenInfo{}) != 68 ||
		unsafe.Sizeof(videoMode{}) != 56 {
		fmt.Printf("ABI mismatch v=%d f=%d m=%d\n",
			unsafe.Sizeof(varScreenInfo{}), unsafe.Sizeof(fixScreenInfo{}), unsafe.Sizeof(videoMode{}))
		os.Exit(9)
	}

	mode := "p"
	hold := 60
	if len(os.Args) > 1 && len(os.Args[1]) > 0 {
		mode = os.Args[1][:1]
	}
	if len(os.Args) > 2 && (mode == "h" || mode == "f") {
		if n, err := strconv.Atoi(os.Args[2]); err == nil && n > 0 && n <= 3600 {
			hold = n
		}
	}
	wide := mode == "f"
	change := wide || mode == "h" || mode == "r"

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
	fmt.Printf("BG xres=%d yres=%d vx=%d vy=%d bpp=%d stride=%d\n",
		bgV.Xres, bgV.Yres, bgV.XresVirtual, bgV.YresVirtual, bgV.BitsPerPixel, bgF.LineLength)
	fmt.Printf("STATE blank=%d com=%d gw=0x%08x pos=%d channel=0x%08x\n",
		blank, reg(0), reg(1), reg(2), channel)

	if mode == "p" {
		fmt.Println("PROBE_ONLY")
		return
	}

	if mode == "r" {
		var s saved
		px, err := loadState(&s)
		if err != nil {
			fmt.Println("ERROR NO_BACKUP", err)
			os.Exit(2)
		}
		if s.Magic != magicID {
			fmt.Println("ERROR BAD_BACKUP")
			os.Exit(2)
		}
		if h := hash(px); h != s.Hash {
			fmt.Println("ERROR BAD_BACKUP_PIXELS")
			os.Exit(2)
		}
		fmt.Printf("RECOVER_FROM_BACKUP smem=%d hash=%d\n", s.F.SmemLen, s.Hash)
		os.Exit(restore(&s, px, fgV.Xres != 240))
	}

	// 变更前的基线校验：几何/blank/channel 严格，寄存器只告警
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
	// com / gw / pos / channel 只告警：v1 可能残留 alpha-enable 位，且本项目 firmware 的
	// channel 值与源码期望差 1 位（实测 0x0a6ff7ff vs 期望 0x0a6ff7fe），不足以拒绝。
	if reg(0) != 4 || reg(1) != 0x80000000 || reg(2) != 0 {
		fmt.Printf("WARN_REGISTER_RESIDUAL com=%d gw=0x%08x pos=0x%08x\n", reg(0), reg(1), reg(2))
	}
	if channel != 0x0a6ff7fe {
		fmt.Printf("WARN_CHANNEL value=0x%08x expected=0x0a6ff7fe\n", channel)
	}

	s := saved{Magic: magicID, V: fgV, F: fgF, Blank: blank, Com: reg(0), Gw: reg(1), Pos: reg(2)}

	// 备份 fb1 当前像素
	orig, err := syscall.Mmap(fgFd, 0, int(fgF.SmemLen), syscall.PROT_READ, syscall.MAP_SHARED)
	if err != nil {
		fmt.Println("ERROR MMAP_BACKUP", err)
		os.Exit(4)
	}
	pixels := make([]byte, fgF.SmemLen)
	copy(pixels, orig)
	syscall.Munmap(orig)
	s.Hash = hash(pixels)
	fmt.Printf("BACKUP smem=%d hash=%d\n", fgF.SmemLen, s.Hash)
	if err := saveState(&s, pixels); err != nil {
		fmt.Println("ERROR SAVE_BACKUP", err)
		os.Exit(4)
	}

	w, h := int(fgV.Xres), int(fgV.Yres)
	smem, stride := int(fgF.SmemLen), int(fgF.LineLength)

	if wide {
		if err := setMode(true, &fgV, &bgV); err != nil {
			fmt.Println("ERROR SETMODE_WIDE", err)
			os.Exit(5)
		}
		var v varScreenInfo
		var f fixScreenInfo
		if err := getMode(fgFd, &v, &f); err != nil {
			fmt.Println("ERROR GET_WIDE", err)
			restore(&s, pixels, true)
			os.Exit(5)
		}
		fmt.Printf("WIDE xres=%d yres=%d vx=%d vy=%d bpp=%d stride=%d smem=%d\n",
			v.Xres, v.Yres, v.XresVirtual, v.YresVirtual, v.BitsPerPixel, f.LineLength, f.SmemLen)
		if v.Xres != 1920 || v.Yres != 720 || v.XresVirtual != 1920 || v.YresVirtual != 720 ||
			v.BitsPerPixel != 16 || v.Xoffset != 0 || v.Yoffset != 0 ||
			f.LineLength != 3840 || f.SmemLen < 2764800 {
			fmt.Println("REFUSED_WIDE_GEOMETRY")
			restore(&s, pixels, true)
			os.Exit(6)
		}
		w, h, smem, stride = 1920, 720, int(f.SmemLen), int(f.LineLength)
	}

	fb, err := syscall.Mmap(fgFd, 0, smem, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
	if err != nil {
		fmt.Println("ERROR MMAP_FB", err)
		restore(&s, pixels, wide)
		os.Exit(7)
	}
	for i := range fb {
		fb[i] = 0
	}
	testCard(fb, w, h, stride)

	alpha := [2]int32{1, 255}
	key := [2]int32{1, 0}
	if err := ioctl(fgFd, ioAlpha, unsafe.Pointer(&alpha)); err != nil {
		fmt.Println("ERROR FG_ALPHA", err)
		restore(&s, pixels, wide)
		os.Exit(8)
	}
	if err := ioctl(fgFd, ioKey, unsafe.Pointer(&key)); err != nil {
		fmt.Println("ERROR BLACK_KEY", err)
		restore(&s, pixels, wide)
		os.Exit(8)
	}
	fmt.Printf("BLEND com=%d gw=0x%08x\n", reg(0)&15, reg(1))
	if reg(0)&15 != 14 || reg(1) != 0xff000000 || reg(2) != 0 {
		fmt.Println("REFUSED_BLEND_STATE")
		restore(&s, pixels, wide)
		os.Exit(8)
	}

	if err := ioctl(fgFd, fbioBlank, unsafe.Pointer(uintptr(0))); err != nil {
		fmt.Println("ERROR ENABLE", err)
		restore(&s, pixels, wide)
		os.Exit(8)
	}
	fmt.Printf("AFTER_ENABLE com=%d gw=0x%08x\n", reg(0)&15, reg(1))
	if reg(0)&15 != 15 || reg(1) != 0xff000000 || reg(2) != 0 {
		fmt.Println("REFUSED_ACTIVE_BLEND_STATE")
		restore(&s, pixels, wide)
		os.Exit(8)
	}
	fmt.Printf("VISIBLE %dx%d hold=%ds\n", w, h, hold)
	os.Stdout.Sync()

	for i := 0; i < hold; i++ {
		syscall.Nanosleep(&syscall.Timespec{Sec: 1}, nil)
	}
	syscall.Munmap(fb)
	os.Exit(restore(&s, pixels, wide))
}
