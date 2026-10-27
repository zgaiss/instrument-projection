// drawtest.go —— 最小验证：往仪表 fb1 前景层投一张固定图案，定时后自动还原
//
// 不改模式、不改 virtual_size、不备份像素、不写 sysfs。
// 流程：读几何 -> 填图案 -> 设 alpha/key -> 解除 blank -> 保持 -> 还原。
//
// 用法：drawtest p           只读报告（不写任何东西）
//
//	drawtest h [秒]      投放图案（默认 60 秒后自动还原）
//	drawtest r           仅还原
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

	regBase = 0x02740000
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

func ioctl(fd int, op uintptr, arg unsafe.Pointer) error {
	_, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), op, uintptr(arg))
	if e != 0 {
		return e
	}
	return nil
}

func rgb(r, g, b uint32) uint16 {
	return uint16((r&0x1f)<<11 | (g&0x3f)<<5 | (b & 0x1f))
}

// 固定图案：白边框 + 顶部红条 + 四色竖条，缩放到任何尺寸都能一眼认出
func pattern(p []byte, w, h, stride int) {
	for y := 0; y < h; y++ {
		row := p[y*stride:]
		for x := 0; x < w; x++ {
			var c uint16
			switch {
			case y < 6 || y >= h-6 || x < 6 || x >= w-6:
				c = rgb(31, 63, 31)
			case y < 34:
				c = rgb(31, 0, 0)
			default:
				switch (x * 4) / w {
				case 0:
					c = rgb(31, 63, 31)
				case 1:
					c = rgb(31, 0, 0)
				case 2:
					c = rgb(0, 63, 0)
				default:
					c = rgb(0, 0, 31)
				}
			}
			row[x*2] = byte(c)
			row[x*2+1] = byte(c >> 8)
		}
	}
}

func main() {
	if unsafe.Sizeof(varScreenInfo{}) != 160 || unsafe.Sizeof(fixScreenInfo{}) != 68 {
		fmt.Printf("ABI mismatch var=%d fix=%d\n",
			unsafe.Sizeof(varScreenInfo{}), unsafe.Sizeof(fixScreenInfo{}))
		os.Exit(9)
	}

	mode := "p"
	hold := 60
	if len(os.Args) > 1 && len(os.Args[1]) > 0 {
		mode = os.Args[1][:1]
	}
	if len(os.Args) > 2 && mode == "h" {
		if n, err := strconv.Atoi(os.Args[2]); err == nil && n > 0 && n <= 3600 {
			hold = n
		}
	}

	fg, err := syscall.Open("/dev/fb1", oRDWR, 0)
	if err != nil {
		fmt.Println("ERROR OPEN_FB1", err)
		os.Exit(2)
	}
	defer syscall.Close(fg)

	mem, err := syscall.Open("/dev/mem", syscall.O_RDONLY, 0)
	if err != nil {
		fmt.Println("ERROR OPEN_MEM", err)
		os.Exit(2)
	}
	regm, err := syscall.Mmap(mem, regBase, 4096, syscall.PROT_READ, syscall.MAP_SHARED)
	syscall.Close(mem)
	if err != nil {
		fmt.Println("ERROR MMAP_REG", err)
		os.Exit(2)
	}
	defer syscall.Munmap(regm)
	regs := func(i int) uint32 {
		return *(*uint32)(unsafe.Pointer(&regm[i*4]))
	}

	var ov varScreenInfo
	var of fixScreenInfo
	if err := ioctl(fg, fbioGetVScreen, unsafe.Pointer(&ov)); err != nil {
		fmt.Println("ERROR GET_VAR", err)
		os.Exit(2)
	}
	if err := ioctl(fg, fbioGetFScreen, unsafe.Pointer(&of)); err != nil {
		fmt.Println("ERROR GET_FIX", err)
		os.Exit(2)
	}
	var blank uint32 = 99
	ioctl(fg, ioGetBlank, unsafe.Pointer(&blank))

	fmt.Printf("GEOM xres=%d yres=%d vx=%d vy=%d bpp=%d stride=%d smem=%d\n",
		ov.Xres, ov.Yres, ov.XresVirtual, ov.YresVirtual, ov.BitsPerPixel, of.LineLength, of.SmemLen)
	fmt.Printf("COLOR red=%d green=%d blue=%d\n", ov.Red.Offset, ov.Green.Offset, ov.Blue.Offset)
	fmt.Printf("STATE blank=%d com=%d gw=0x%08x pos=%d\n", blank, regs(0), regs(1), regs(2))

	if mode == "p" {
		fmt.Println("PROBE_ONLY")
		return
	}
	if mode == "r" {
		restore(fg)
		return
	}

	if ov.Xres != 240 || ov.Yres != 320 || ov.BitsPerPixel != 16 ||
		of.LineLength < 480 || of.SmemLen < 153600 {
		fmt.Println("REFUSED_UNEXPECTED_GEOMETRY")
		os.Exit(3)
	}

	/* 先填图案再解 blank，避免闪一屏垃圾 */
	fb, err := syscall.Mmap(fg, 0, int(of.SmemLen), syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
	if err != nil {
		fmt.Println("ERROR MMAP_FB1", err)
		os.Exit(4)
	}
	for i := range fb {
		fb[i] = 0
	}
	pattern(fb, int(ov.Xres), int(ov.Yres), int(of.LineLength))

	alpha := [2]int32{1, 255}
	key := [2]int32{1, 0}
	if err := ioctl(fg, ioAlpha, unsafe.Pointer(&alpha)); err != nil {
		fmt.Println("ERROR FG_ALPHA", err)
		os.Exit(5)
	}
	if err := ioctl(fg, ioKey, unsafe.Pointer(&key)); err != nil {
		fmt.Println("ERROR BLACK_KEY", err)
		os.Exit(5)
	}
	fmt.Printf("BLEND com=%d gw=0x%08x\n", regs(0)&15, regs(1))

	if err := ioctl(fg, fbioBlank, unsafe.Pointer(uintptr(0))); err != nil {
		fmt.Println("ERROR ENABLE", err)
		restore(fg)
		os.Exit(5)
	}
	fmt.Printf("AFTER_ENABLE com=%d gw=0x%08x\n", regs(0)&15, regs(1))
	fmt.Println("VISIBLE")
	os.Stdout.Sync()

	for i := 0; i < hold; i++ {
		syscall.Nanosleep(&syscall.Timespec{Sec: 1}, nil)
	}
	restore(fg)
}

func restore(fg int) {
	fmt.Println("RESTORING")
	ioctl(fg, fbioBlank, unsafe.Pointer(uintptr(4)))
	key := [2]int32{0, 0}
	alpha := [2]int32{1, 128}
	if err := ioctl(fg, ioKey, unsafe.Pointer(&key)); err != nil {
		fmt.Println("ERROR KEY_BACK", err)
	}
	if err := ioctl(fg, ioAlpha, unsafe.Pointer(&alpha)); err != nil {
		fmt.Println("ERROR ALPHA_BACK", err)
	}
	if err := ioctl(fg, fbioBlank, unsafe.Pointer(uintptr(4))); err != nil {
		fmt.Println("ERROR FINAL_BLANK", err)
	}
	fmt.Println("RESTORE_OK")
}
