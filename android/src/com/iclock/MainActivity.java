package com.iclock;

import android.app.Activity;
import android.app.AlertDialog;
import android.content.DialogInterface;
import android.graphics.Color;
import android.graphics.Typeface;
import android.os.Bundle;
import android.os.Handler;
import android.os.Looper;
import android.text.method.ScrollingMovementMethod;
import android.view.View;
import android.view.ViewGroup;
import android.view.WindowManager;
import android.widget.Button;
import android.widget.LinearLayout;
import android.widget.ScrollView;
import android.widget.TextView;

import java.io.ByteArrayOutputStream;
import java.io.IOException;
import java.io.InputStream;
import java.io.OutputStream;
import java.net.InetAddress;
import java.net.InetSocketAddress;
import java.net.NetworkInterface;
import java.net.ServerSocket;
import java.net.Socket;
import java.net.SocketTimeoutException;
import java.text.SimpleDateFormat;
import java.util.Date;
import java.util.Enumeration;
import java.util.Locale;

/**
 * 仪表投放 —— 把一张色块测试图 + 倒计时推到仪表全屏显示。
 *
 * 全流程在车机本地完成，不依赖电脑：
 *   1. 用 30510 管理 RPC（仪表上的 root shell）准备目录；
 *   2. 本机开一个临时 TCP 端口，让仪表回连把 armv7 程序拉过去；
 *   3. 通过 RPC 启动它；程序自己每秒重绘倒计时，到点自动还原仪表。
 */
public class MainActivity extends Activity {

    // ── 实测得到的固定参数 ────────────────────────────────
    static final String IC_IP = "172.20.1.40";    // 仪表
    static final int IC_RPC = 30510;              // 管理 RPC（root shell）
    static final String LOCAL_IP = "172.20.1.45"; // 本机车内网地址，仪表按这个回连
    static final int PULL_PORT = 9971;            // 二进制投递端口
    static final String DIR = "/tmp/iclock";
    static final String BIN = DIR + "/bin";
    static final String LOG = DIR + "/run.log";

    private TextView logView, hintView;
    private Button btnStart, btnStop, btnProbe;
    private final Handler ui = new Handler(Looper.getMainLooper());
    private volatile boolean busy = false;
    private int holdSec = 300;

    @Override
    protected void onCreate(Bundle b) {
        super.onCreate(b);
        getWindow().addFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON);

        LinearLayout root = new LinearLayout(this);
        root.setOrientation(LinearLayout.VERTICAL);
        root.setBackgroundColor(Color.rgb(14, 16, 22));
        int pad = dp(12);
        root.setPadding(pad, pad, pad, pad);

        TextView title = new TextView(this);
        title.setText("仪表投放 · 色块 + 倒计时");
        title.setTextColor(Color.rgb(120, 220, 160));
        title.setTextSize(18);
        root.addView(title);

        hintView = new TextView(this);
        hintView.setTextSize(12);
        hintView.setTextColor(Color.rgb(150, 200, 170));
        hintView.setPadding(0, dp(4), 0, dp(6));
        root.addView(hintView);

        TextView sub = new TextView(this);
        sub.setText("仪表 " + IC_IP + ":" + IC_RPC + "　→　本机 " + LOCAL_IP);
        sub.setTextColor(Color.rgb(110, 116, 130));
        sub.setTextSize(11);
        sub.setPadding(0, 0, 0, dp(10));
        root.addView(sub);

        LinearLayout bar = new LinearLayout(this);
        bar.setOrientation(LinearLayout.HORIZONTAL);
        btnStart = mkBtn("投放");
        btnStop = mkBtn("还原");
        btnProbe = mkBtn("探测");
        bar.addView(btnStart);
        bar.addView(btnStop);
        bar.addView(btnProbe);
        root.addView(bar);

        ScrollView sv = new ScrollView(this);
        LinearLayout.LayoutParams svp = new LinearLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT, 0, 1f);
        svp.topMargin = dp(10);
        sv.setLayoutParams(svp);

        logView = new TextView(this);
        logView.setTextColor(Color.rgb(200, 206, 216));
        logView.setTextSize(11);
        logView.setTypeface(Typeface.MONOSPACE);
        logView.setMovementMethod(new ScrollingMovementMethod());
        logView.setTextIsSelectable(true);
        sv.addView(logView);
        root.addView(sv);

        setContentView(root);

        btnStart.setOnClickListener(new View.OnClickListener() {
            public void onClick(View v) { pickDuration(); }
        });
        btnStop.setOnClickListener(new View.OnClickListener() {
            public void onClick(View v) { run(2); }
        });
        btnProbe.setOnClickListener(new View.OnClickListener() {
            public void onClick(View v) { run(0); }
        });

        refreshHint();
        log("本机网卡: " + localIps());
        log("就绪。点「投放」选时长后推到仪表，到点自动还原。");
        log("全程不需要电脑。首次使用建议先点「探测」确认通道。");
    }

    private void refreshHint() {
        hintView.setText("时长 " + holdSec + " 秒　·　先选时长再投放");
    }

    private Button mkBtn(String t) {
        Button b = new Button(this);
        b.setText(t);
        b.setTextSize(14);
        LinearLayout.LayoutParams p = new LinearLayout.LayoutParams(0,
                ViewGroup.LayoutParams.WRAP_CONTENT, 1f);
        p.rightMargin = dp(6);
        b.setLayoutParams(p);
        return b;
    }

    private int dp(int v) {
        return (int) (v * getResources().getDisplayMetrics().density + 0.5f);
    }

    // ── 时长选择 ──────────────────────────────────────────
    private void pickDuration() {
        final int[] opts = {60, 120, 300, 600, 1800};
        final String[] labels = {"1 分钟", "2 分钟", "5 分钟", "10 分钟", "30 分钟"};
        new AlertDialog.Builder(this)
                .setTitle("倒计时时长")
                .setItems(labels, new DialogInterface.OnClickListener() {
                    public void onClick(DialogInterface d, int which) {
                        holdSec = opts[which];
                        refreshHint();
                        run(1);
                    }
                })
                .show();
    }

    // ── 日志 ──────────────────────────────────────────────
    private void log(final String s) {
        final String line = new SimpleDateFormat("HH:mm:ss", Locale.US).format(new Date())
                + "  " + s + "\n";
        ui.post(new Runnable() {
            public void run() {
                logView.append(line);
                if (logView.getLayout() != null) {
                    int sc = logView.getLayout().getLineTop(logView.getLineCount())
                            - logView.getHeight();
                    if (sc > 0) logView.scrollTo(0, sc);
                }
            }
        });
    }

    private static String localIps() {
        StringBuilder sb = new StringBuilder();
        try {
            Enumeration<NetworkInterface> en = NetworkInterface.getNetworkInterfaces();
            while (en != null && en.hasMoreElements()) {
                NetworkInterface ni = en.nextElement();
                if (!ni.isUp() || ni.isLoopback()) continue;
                Enumeration<InetAddress> as = ni.getInetAddresses();
                while (as.hasMoreElements()) {
                    InetAddress a = as.nextElement();
                    if (a.getAddress().length == 4 && !a.isLoopbackAddress()) {
                        sb.append(ni.getName()).append("=").append(a.getHostAddress()).append("  ");
                    }
                }
            }
        } catch (Exception e) {
            return "读取失败: " + e;
        }
        String s = sb.toString().trim();
        return s.length() == 0 ? "(无 IPv4)" : s;
    }

    // ── 入口：0 探测 / 1 投放 / 2 还原 ─────────────────────
    private void run(final int what) {
        if (busy) { log("上一步还在进行中…"); return; }
        busy = true;
        setEnabled(false);
        new Thread(new Runnable() {
            public void run() {
                try {
                    if (what == 0) doProbe();
                    else if (what == 1) doDeploy();
                    else doRestore();
                } catch (Throwable t) {
                    log("!! " + t.getClass().getSimpleName() + ": " + t.getMessage());
                } finally {
                    busy = false;
                    ui.post(new Runnable() {
                        public void run() { setEnabled(true); }
                    });
                }
            }
        }, "ic-worker").start();
    }

    private void setEnabled(final boolean on) {
        btnStart.setEnabled(on);
        btnStop.setEnabled(on);
        btnProbe.setEnabled(on);
    }

    // ── 探测 ──────────────────────────────────────────────
    private void doProbe() {
        log("── 只读探测（不写仪表）──");
        String ips = localIps();
        log("本机网卡: " + ips);
        if (!ips.contains(LOCAL_IP)) {
            log("⚠ 没找到 " + LOCAL_IP + "。车载网口可能没起来，正常情况走不通。");
        }

        log("1) 通道连通性 + 身份");
        String u = rpc("echo IC_OK; id 2>/dev/null | head -1; uname -r", 6000);
        if (u == null) {
            log("✗ 连不上 " + IC_IP + ":" + IC_RPC);
            log("  排查：车是否通电、eth0 是否 " + LOCAL_IP + "、是否被挡。");
            return;
        }
        log("✓ " + oneLine(u));

        log("2) 仪表侧现状");
        log("  " + oneLine(rpc("ls -l " + BIN + " 2>&1; cat " + DIR + "/pid 2>/dev/null", 4000)));

        log("3) 显示状态（只读）");
        log("  " + oneLine(rpc(BIN + " p 2>&1", 8000)));
        log("  若报 No such file，说明还没投过程序，属正常。");
    }

    // ── 投放 ──────────────────────────────────────────────
    private void doDeploy() {
        log("════ 开始投放（" + holdSec + " 秒）════");

        log("── 1/8 通道检查 ──");
        String u = rpc("echo IC_OK; id 2>/dev/null | head -1", 5000);
        if (u == null || !u.contains("IC_OK")) {
            log("✗ 连不上仪表 " + IC_IP + ":" + IC_RPC);
            log("  排查：车是否通电、本机 eth0 是否 " + LOCAL_IP + "、是否被挡。");
            return;
        }
        log("✓ " + oneLine(u));

        log("── 2/8 清掉上次残留 ──");
        rpc("p=$(cat " + DIR + "/pid 2>/dev/null); [ -n \"$p\" ] && kill $p 2>/dev/null; echo CLEAN", 4000);
        sleep(4500);
        log("  " + oneLine(rpc("mkdir -p " + DIR + "; rm -f " + BIN + " " + LOG
                + "; echo PREP_OK", 4000)));

        log("── 3/8 本机监听 " + PULL_PORT + " ──");
        ServerSocket ss;
        try {
            ss = new ServerSocket();
            ss.setReuseAddress(true);
            ss.bind(new InetSocketAddress(InetAddress.getByName("0.0.0.0"), PULL_PORT), 1);
            ss.setSoTimeout(45000);
            log("✓ 已监听");
        } catch (IOException e) {
            log("✗ 监听失败: " + e + "（端口被占用？）");
            return;
        }

        log("── 4/8 让仪表回连拉取 ──");
        // 仪表侧 nc 在 stdin EOF 后立刻退出，要用 sleep 顶住；
        // 传输完成后由同一条后台命令做 chmod。
        String pullCmd = "( ( sleep 6 ) | nc " + LOCAL_IP + " " + PULL_PORT + " > " + BIN
                + "; chmod 755 " + BIN + " ) > /dev/null 2>&1 &";
        log("  " + oneLine(rpc(pullCmd, 4000)) + " (已下发)");

        long sent = -1;
        log("── 5/8 传输二进制 ──");
        Socket c = null;
        try {
            c = ss.accept();
            log("  ✓ 仪表已连接: " + c.getInetAddress().getHostAddress());
            InputStream is = getAssets().open("iclock");
            OutputStream os = c.getOutputStream();
            byte[] buf = new byte[65536];
            long n = 0;
            int r;
            while ((r = is.read(buf)) > 0) {
                os.write(buf, 0, r);
                n += r;
            }
            os.flush();
            is.close();
            sleep(400);
            c.close();
            c = null;
            sent = n;
            log("  ✓ 已发送 " + n + " 字节");
        } catch (SocketTimeoutException e) {
            log("✗ 仪表没有连过来（45s 超时）");
            log("  多半是仪表上的 nc 不可用，或该端口被策略拦住。");
        } catch (IOException e) {
            log("✗ 传输中断: " + e);
        } finally {
            if (c != null) { try { c.close(); } catch (IOException ignored) {} }
            try { ss.close(); } catch (IOException ignored) {}
        }
        if (sent < 0) return;

        log("  等待仪表落盘…");
        sleep(8000);

        log("── 6/8 核对文件 ──");
        String chk = rpc("ls -l " + BIN + " 2>&1", 4000);
        log("  " + oneLine(chk));
        if (chk == null || !chk.contains("rwx")) {
            log("✗ 文件没就位或缺可执行位，终止。");
            return;
        }

        log("── 7/8 启动 ──");
        String st = rpc("( " + BIN + " " + holdSec + " > " + LOG + " 2>&1 & ) ; sleep 2; echo STARTED", 9000);
        log("  " + oneLine(st));

        log("── 8/8 运行日志 ──");
        String lg = rpc("head -22 " + LOG + " 2>&1", 5000);
        log("  " + oneLine(lg));
        if (lg == null) return;
        if (lg.contains("REFUSED_BASELINE_GEOMETRY")) {
            log("✗ 仪表显示层不在基线（尺寸不是 240x320）。");
            log("  多半是上次没还原干净。点「还原」；无效就断电重启仪表。");
        } else if (lg.contains("REFUSED_BASELINE_BLANK")) {
            log("✗ 前景层当前不是 blank=4，先点「还原」。");
        } else if (lg.contains("REFUSED_BLEND_STATE") || lg.contains("REFUSED_ACTIVE_BLEND_STATE")) {
            log("✗ 混合态校验没过，已自动回滚。把日志发我。");
        } else if (lg.contains("VISIBLE")) {
            log("✅ 已投放。仪表应显示：深蓝底 + 大号倒计时 + 八段彩条 + 白边框。");
            if (lg.contains("TICK")) {
                log("   倒计时已在跳（日志里能看到 TICK）。");
            }
            log("   剩余 " + holdSec + " 秒后自动还原，也可随时点「还原」。");
        } else {
            log("⚠ 没看到预期标记，把上面日志发我。");
        }
    }

    // ── 还原 ──────────────────────────────────────────────
    private void doRestore() {
        log("── 立即还原 ──");
        String pid = rpc("cat " + DIR + "/pid 2>/dev/null", 3000);
        String p = pid == null ? "" : pid.replaceAll("[^0-9]", "");
        if (p.length() > 0) {
            log("  通知进程 " + p + " 退出（它会走完整恢复）");
            log("  " + oneLine(rpc("kill " + p + " 2>/dev/null; echo KILLED", 3000)));
            sleep(4500);
        } else {
            log("  没有记录到运行中的进程");
        }

        String r = rpc(BIN + " r 2>&1", 14000);
        log("  " + oneLine(r));
        if (r != null && r.contains("RESTORE_OK")) {
            log("✅ 已完全还原（像素 hash 与备份一致）。");
        } else if (r != null && r.contains("NO_VALID_BACKUP")) {
            log("  没有可用备份，说明本来就没投放过。");
        } else {
            log("⚠ 恢复未确认。兜底：仪表断电重启，/tmp 清空、显示层回出厂状态。");
        }
    }

    // ── 30510 RPC ─────────────────────────────────────────
    /**
     * 请求 = u16BE 0x0101 | u16BE 0x0211 | u32BE size
     *      | u16BE 0x7e71 | u16BE session | 4B {1,0,0,0} | payload
     *      size = 8 + len(payload)
     * payload = field1(header=0x08+varint(0x7e710000+session))
     *         + field2("shell") + field2(cmd)
     * 响应 = 同结构 8B 头 + size 字节，其中前 8 字节为
     *        client/session/protocol/version/type(0x80)/rc(0)
     */
    private String rpc(String cmd, int timeoutMs) {
        Socket s = new Socket();
        try {
            s.connect(new InetSocketAddress(IC_IP, IC_RPC), 4000);
            s.setSoTimeout(timeoutMs);
            OutputStream os = s.getOutputStream();
            os.write(buildFrame(cmd));
            os.flush();

            InputStream in = s.getInputStream();
            byte[] hdr = readN(in, 8);
            if (hdr == null) return null;
            int size = ((hdr[4] & 0xff) << 24) | ((hdr[5] & 0xff) << 16)
                    | ((hdr[6] & 0xff) << 8) | (hdr[7] & 0xff);
            if (size < 8 || size > 8 * 1024 * 1024) return null;
            byte[] body = readN(in, size);
            if (body == null) return null;
            if (body.length >= 8) {
                int rtype = body[6] & 0xff;
                int rc = body[7] & 0xff;
                if (rtype != 0x80 || rc != 0) {
                    log("  [rpc rc=" + rc + " type=0x" + Integer.toHexString(rtype) + "]");
                }
            }
            return extractStrings(body, 8);
        } catch (SocketTimeoutException e) {
            return null;
        } catch (IOException e) {
            log("  [rpc] " + e.getMessage());
            return null;
        } finally {
            try { s.close(); } catch (IOException ignored) {}
        }
    }

    private static byte[] readN(InputStream in, int n) throws IOException {
        byte[] b = new byte[n];
        int off = 0;
        while (off < n) {
            int r = in.read(b, off, n - off);
            if (r < 0) return null;
            off += r;
        }
        return b;
    }

    private int session = 0x4100;
    private static final int CLIENT = 0x7E71;
    private static final int BASE = 0x7E710000;

    private byte[] buildFrame(String cmd) {
        session = (session + 1) & 0xFFFF;
        byte[] header = concat(new byte[]{0x08}, varint(BASE + session));
        byte[] payload = concat(
                pbField(1, header),
                pbField(2, "shell".getBytes()),
                pbField(2, utf8(cmd)));
        ByteArrayOutputStream o = new ByteArrayOutputStream();
        o.write(0x01); o.write(0x01);
        o.write(0x02); o.write(0x11);
        int size = 8 + payload.length;
        o.write((size >> 24) & 0xff);
        o.write((size >> 16) & 0xff);
        o.write((size >> 8) & 0xff);
        o.write(size & 0xff);
        o.write(0x7e); o.write(0x71);
        o.write((session >> 8) & 0xff);
        o.write(session & 0xff);
        o.write(0x01); o.write(0x00); o.write(0x00); o.write(0x00);
        o.write(payload, 0, payload.length);
        return o.toByteArray();
    }

    private static byte[] utf8(String s) {
        try { return s.getBytes("UTF-8"); } catch (Exception e) { return s.getBytes(); }
    }

    private static byte[] varint(long v) {
        ByteArrayOutputStream o = new ByteArrayOutputStream();
        while (v > 0x7F) {
            o.write((int) ((v & 0x7F) | 0x80));
            v >>= 7;
        }
        o.write((int) (v & 0x7F));
        return o.toByteArray();
    }

    private static byte[] pbField(int num, byte[] data) {
        return concat(varint(num * 8 + 2), varint(data.length), data);
    }

    private static byte[] concat(byte[]... parts) {
        int n = 0;
        for (byte[] p : parts) n += p.length;
        byte[] out = new byte[n];
        int o = 0;
        for (byte[] p : parts) {
            System.arraycopy(p, 0, out, o, p.length);
            o += p.length;
        }
        return out;
    }

    /** 递归抓 protobuf 里可打印的 length-delimited 字段 */
    private static String extractStrings(byte[] buf, int start) {
        StringBuilder sb = new StringBuilder();
        walk(buf, start, sb, 0);
        return sb.toString();
    }

    private static void walk(byte[] b, int i, StringBuilder sb, int depth) {
        if (depth > 6) return;
        while (i < b.length) {
            long[] tagRes = new long[1];
            int ni = readVarint(b, i, tagRes);
            if (ni < 0) return;
            long tag = tagRes[0];
            i = ni;
            int wire = (int) (tag & 7);
            if (wire == 0) {
                int k = readVarint(b, i, tagRes);
                if (k < 0) return;
                i = k;
            } else if (wire == 2) {
                int k = readVarint(b, i, tagRes);
                if (k < 0) return;
                int len = (int) tagRes[0];
                i = k;
                if (len < 0 || i + len > b.length) return;
                int printable = 0;
                for (int j = i; j < i + len; j++) {
                    int ch = b[j] & 0xff;
                    if ((ch >= 9 && ch <= 13) || (ch >= 32 && ch < 127) || ch >= 0xC0) printable++;
                }
                if (len > 0 && printable * 100 / len > 85) {
                    sb.append(new String(b, i, len)).append('\n');
                } else {
                    walk(b, i, sb, depth + 1);
                }
                i += len;
            } else if (wire == 5) {
                i += 4;
            } else if (wire == 1) {
                i += 8;
            } else {
                return;
            }
        }
    }

    private static int readVarint(byte[] b, int i, long[] out) {
        long v = 0;
        int shift = 0;
        while (i < b.length) {
            int x = b[i] & 0xff;
            i++;
            v |= ((long) (x & 0x7F)) << shift;
            if ((x & 0x80) == 0) {
                out[0] = v;
                return i;
            }
            shift += 7;
            if (shift > 63) return -1;
        }
        return -1;
    }

    private static String oneLine(String s) {
        if (s == null) return "(无响应)";
        s = s.trim().replace("\r", "");
        if (s.length() == 0) return "(空)";
        return s.replace("\n", " ⏎ ");
    }

    private static void sleep(long ms) {
        try { Thread.sleep(ms); } catch (InterruptedException ignored) {}
    }
}
