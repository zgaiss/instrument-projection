/* drawtest.c —— 最小验证：往仪表 fb1 前景层投一张固定图案，定时后自动还原
 *
 * 不改模式、不改 virtual_size、不备份像素、不写 sysfs。
 * 只做：读几何 -> 填图案 -> 设 alpha/key -> 解除 blank -> 保持 -> 还原。
 *
 * 无 libc，直 syscall（与原方案 receiver 同风格），armv7 静态。
 * 用法：drawtest p           只读报告（不写任何东西）
 *       drawtest h [秒]      投放图案（默认 60 秒后自动还原）
 *       drawtest r           仅还原
 */
typedef unsigned char u8;
typedef unsigned short u16;
typedef unsigned int u32;

#define O_RDWR 2
#define PROT_READ 1
#define PROT_WRITE 2
#define MAP_SHARED 1
#define MAP_PRIVATE 2
#define MAP_ANONYMOUS 0x20

#define FBIOGET_VSCREENINFO 0x4600
#define FBIOGET_FSCREENINFO 0x4602
#define FBIOBLANK 0x4611
#define ALPHA 0x40084621U
#define KEY 0x40084622U
#define GETBLANK 0x8004462BU

#define REGBASE 0x02740000U

struct fb_bitfield {
    u32 offset, length, msb_right;
};
struct fb_var_screeninfo {
    u32 xres, yres, xres_virtual, yres_virtual;
    u32 xoffset, yoffset;
    u32 bits_per_pixel, grayscale;
    struct fb_bitfield red, green, blue, transp;
    u32 nonstd, activate;
    u32 height, width, accel_flags;
    u32 pixclock, left_margin, right_margin, upper_margin, lower_margin;
    u32 hsync_len, vsync_len, sync, vmode, rotate, colorspace;
    u32 reserved[4];
};
struct fb_fix_screeninfo {
    char id[16];
    unsigned long smem_start;
    u32 smem_len, type, type_aux, visual;
    u16 xpanstep, ypanstep, ywrapstep;
    u32 line_length;
    unsigned long mmio_start;
    u32 mmio_len, accel;
    u16 reserved[3];
};

/* ABI 固定：布局不对就整个作废，宁可编译失败也不要运行时错位 */
typedef char assert_var[(sizeof(struct fb_var_screeninfo) == 160) ? 1 : -1];
typedef char assert_fix[(sizeof(struct fb_fix_screeninfo) == 68) ? 1 : -1];

static long call6(long n, long a, long b, long c, long d, long e, long f) {
    register long r0 __asm__("r0") = a, r1 __asm__("r1") = b, r2 __asm__("r2") = c;
    register long r3 __asm__("r3") = d, r4 __asm__("r4") = e, r5 __asm__("r5") = f;
    register long r7 __asm__("r7") = n;
    __asm__ volatile("svc 0"
                     : "+r"(r0)
                     : "r"(r1), "r"(r2), "r"(r3), "r"(r4), "r"(r5), "r"(r7)
                     : "memory", "cc");
    return r0;
}
static long s3(long n, long a, long b, long c) { return call6(n, a, b, c, 0, 0, 0); }

void *memcpy(void *d, const void *s, unsigned n) {
    u8 *a = d; const u8 *b = s;
    while (n--) *a++ = *b++;
    return d;
}
void *memset(void *d, int v, unsigned n) {
    u8 *a = d;
    while (n--) *a++ = (u8)v;
    return d;
}
void __aeabi_memcpy4(void *d, const void *s, unsigned n) { memcpy(d, s, n); }

static void text(const char *s) {
    unsigned n = 0;
    while (s[n]) n++;
    s3(4, 1, (long)s, n);
}
static void num(u32 v) {
    char b[12]; int i = 11; b[i] = 0;
    do { b[--i] = '0' + v % 10; v /= 10; } while (v);
    text(b + i);
}
static void kv(const char *s, u32 v) { text(s); num(v); text("\n"); }
static void err(const char *s, long r) {
    text("ERROR "); text(s); text(" errno="); num((u32)-r); text("\n");
}
static void nap(u32 sec) {
    long t[2] = {sec, 0}, r[2];
    while (s3(162, (long)t, (long)r, 0) == -4) { t[0] = r[0]; t[1] = r[1]; }
}
static long iooc(int fd, u32 op, void *p) { return s3(54, fd, op, (long)p); }
static void *mapping(int fd, u32 len, int prot, u32 page) {
    long m = call6(192, 0, len, prot, fd < 0 ? (MAP_PRIVATE | MAP_ANONYMOUS) : MAP_SHARED, fd, page);
    if ((u32)m >= 0xfffff001U) { err("MMAP", m); return 0; }
    return (void *)m;
}

static int fg = -1, mem = -1;
static volatile u32 *regs;
static struct fb_var_screeninfo ov;
static struct fb_fix_screeninfo of;
static u32 oblnak = 4;

static u16 rgb(u32 r, u32 g, u32 b) {
    return (u16)(((r & 0x1f) << 11) | ((g & 0x3f) << 5) | (b & 0x1f));
}

/* 固定图案：四色竖条 + 白色边框 + 顶部黑条，任何缩放下都能一眼认出 */
static void pattern(u8 *p, u32 w, u32 h, u32 stride) {
    u32 x, y;
    for (y = 0; y < h; y++) {
        u16 *row = (u16 *)(p + y * stride);
        for (x = 0; x < w; x++) {
            u16 c;
            if (y < 6 || y >= h - 6 || x < 6 || x >= w - 6) {
                c = rgb(31, 63, 31);                 /* 白边框 */
            } else if (y < 6 + 28) {
                c = rgb(31, 0, 0);                   /* 顶部红条 */
            } else {
                u32 band = (x * 4) / w;
                c = band == 0 ? rgb(31, 63, 31)      /* 白 */
                  : band == 1 ? rgb(31, 0, 0)        /* 红 */
                  : band == 2 ? rgb(0, 63, 0)        /* 绿 */
                              : rgb(0, 0, 31);       /* 蓝 */
            }
            row[x] = c;
        }
    }
}

static void restore(void) {
    int key[2] = {0, 0}, alpha[2] = {1, 128};
    long r;
    text("RESTORING\n");
    if ((r = iooc(fg, FBIOBLANK, (void *)4)) < 0) err("BLANK_ON", r);
    if ((r = iooc(fg, KEY, key)) < 0) err("KEY_BACK", r);
    if ((r = iooc(fg, ALPHA, alpha)) < 0) err("ALPHA_BACK", r);
    if ((r = iooc(fg, FBIOBLANK, (void *)4)) < 0) err("FINAL_BLANK", r);
    text("RESTORE_OK\n");
}

static int run(long *stack) {
    int argc = (int)stack[0];
    char **argv = (char **)stack;
    char mode = argc > 1 ? argv[1][0] : 'p';
    u32 hold = 60;

    if (argc > 2 && mode == 'h') {
        hold = 0;
        for (char *s = argv[2]; *s >= '0' && *s <= '9'; s++) hold = hold * 10 + (u32)(*s - '0');
        if (hold == 0 || hold > 3600) hold = 60;
    }

    fg = (int)s3(5, (long)"/dev/fb1", O_RDWR, 0);
    if (fg < 0) { err("OPEN_FB1", fg); return 2; }

    mem = (int)s3(5, (long)"/dev/mem", 0, 0);
    if (mem < 0) { err("OPEN_MEM", mem); return 2; }
    regs = (volatile u32 *)mapping(mem, 4096, PROT_READ, REGBASE >> 12);
    if (!regs) return 2;
    s3(6, mem, 0, 0);

    if (iooc(fg, FBIOGET_VSCREENINFO, &ov) < 0 || iooc(fg, FBIOGET_FSCREENINFO, &of) < 0) {
        text("ERROR GETMODE\n");
        return 2;
    }
    u32 blank = 99;
    iooc(fg, GETBLANK, &blank);
    oblnak = blank;

    text("GEOM ");
    kv("xres=", ov.xres); kv("yres=", ov.yres);
    kv("vx=", ov.xres_virtual); kv("vy=", ov.yres_virtual);
    kv("bpp=", ov.bits_per_pixel);
    kv("stride=", of.line_length); kv("smem=", of.smem_len);
    kv("red_off=", ov.red.offset); kv("green_off=", ov.green.offset); kv("blue_off=", ov.blue.offset);
    kv("blank=", blank);
    kv("com=", regs[0]); kv("gw=", regs[1]); kv("pos=", regs[2]);

    if (mode == 'p') { text("PROBE_ONLY\n"); return 0; }
    if (mode == 'r') { restore(); return 0; }

    if (ov.xres != 240 || ov.yres != 320 || ov.bits_per_pixel != 16 ||
        of.line_length < 480 || of.smem_len < 153600) {
        text("REFUSED_UNEXPECTED_GEOMETRY\n");
        return 3;
    }

    /* 先填图案，再解除 blank，避免闪一屏垃圾 */
    u8 *p = mapping(fg, of.smem_len, PROT_READ | PROT_WRITE, 0);
    if (!p) return 4;
    memset(p, 0, of.smem_len);
    pattern(p, ov.xres, ov.yres, of.line_length);

    int alpha[2] = {1, 255}, key[2] = {1, 0};
    long r;
    if ((r = iooc(fg, ALPHA, alpha)) < 0) { err("FG_ALPHA", r); return 5; }
    if ((r = iooc(fg, KEY, key)) < 0) { err("BLACK_KEY", r); return 5; }
    kv("blend_com=", regs[0] & 15);
    kv("blend_gw=", regs[1]);
    if ((r = iooc(fg, FBIOBLANK, (void *)0)) < 0) { err("ENABLE", r); restore(); return 5; }
    kv("after_enable_com=", regs[0] & 15);
    kv("after_enable_gw=", regs[1]);
    text("VISIBLE\n");

    nap(hold);
    restore();
    return 0;
}

__attribute__((naked, noreturn)) void _start(void) {
    __asm__ volatile("mov r0, sp\n bl run\n mov r7, #1\n svc 0\n");
}
