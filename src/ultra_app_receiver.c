/* Parked, owner-authorized foreground-only test. No fb0 pixel or mode writes.
 * All mode changes have a separate parent guardian, plus a manual restore path.
 * /dev/mem is read-only. OEM disables FBIOPUT, so use the existing sysfs mode API.
 * The foreground modelist is restored to its single original 240x320 mode. */
#include <linux/fb.h>
#include "tinf/tinf.h"
typedef unsigned int u32;
typedef unsigned short u16;
typedef unsigned char u8;
typedef unsigned long usize;
_Static_assert(sizeof(struct fb_var_screeninfo)==160,"ARM fb var ABI");
_Static_assert(sizeof(struct fb_fix_screeninfo)==68,"ARM fb fix ABI");
#define ROOT "/tmp/op-ultra-app-v61/"
#define ALPHA 0x40084621U
#define KEY 0x40084622U
#define GETBLANK 0x8004462bU
static long call6(long n,long a,long b,long c,long d,long e,long f) {
 register long r0 __asm__("r0")=a,r1 __asm__("r1")=b,r2 __asm__("r2")=c;
 register long r3 __asm__("r3")=d,r4 __asm__("r4")=e,r5 __asm__("r5")=f,r7 __asm__("r7")=n;
 __asm__ volatile("svc 0" : "+r"(r0) : "r"(r1),"r"(r2),"r"(r3),"r"(r4),"r"(r5),"r"(r7) : "memory","cc");return r0;
}
static long s3(long n,long a,long b,long c){return call6(n,a,b,c,0,0,0);}
void *memcpy(void*d,const void*s,usize n){u8*a=d;const u8*b=s;while(n--)*a++=*b++;return d;}
void __aeabi_memcpy4(void*d,const void*s,usize n){memcpy(d,s,n);}
void *memset(void*d,int v,usize n){u8*a=d;while(n--)*a++=(u8)v;return d;}
static int equal(const void*a,const void*b,usize n){const u8*x=a,*y=b;while(n--)if(*x++!=*y++)return 0;return 1;}
static void text(const char*s){usize n=0;while(s[n])n++;s3(4,1,(long)s,n);}
static void num(u32 n){char b[11];int i=10;b[i]=0;do{b[--i]='0'+n%10;n/=10;}while(n);text(b+i);}
static void kv(const char*s,u32 n){text(s);num(n);text("\n");}
static int err(const char*s,long n){text("ERROR ");text(s);text(" errno=");num((u32)-n);text("\n");return 1;}
static long ioc(int fd,u32 op,void*p){return s3(54,fd,op,(long)p);}
static void nap(int sec){long t[2]={sec,0},r[2];while(s3(162,(long)t,(long)r,0)==-4){t[0]=r[0];t[1]=r[1];}}
static u32 hash(const u8*p,u32 n){u32 h=2166136261U;while(n--)h=(h^*p++)*16777619U;return h;}
static void* mapping(int fd,u32 len,int prot,u32 offset){long m=call6(192,0,len,prot,fd<0?0x22:1,fd,offset);if((u32)m>=0xfffff001U){err("MMAP",m);return 0;}return(void*)m;}
static void unmap(void*p,u32 len){if(p)s3(91,(long)p,len,0);}
static int fileio(const char*path,void*buf,u32 len,int write){
 int fd=s3(5,(long)path,write?(1|64|128):0,0600);if(fd<0)return err("BACKUP_OPEN",fd);
 u32 done=0;while(done<len){long r=s3(write?4:3,fd,(long)((u8*)buf+done),len-done);if(r==-4)continue;if(r<=0){s3(6,fd,0,0);return err("BACKUP_IO",r?r:-5);}done+=(u32)r;}
 s3(6,fd,0,0);return 0;
}
struct saved {u32 magic;struct fb_var_screeninfo v;struct fb_fix_screeninfo f;u32 blank,com,gw,pos,hash;};
static struct saved old;
static struct fb_var_screeninfo bgvar;
static volatile u32 *regs;
static u8 *backup;
static int fg=-1,bg=-1;
static volatile u32 *lease;
static int user_stop(void){int fd=s3(5,(long)ROOT "continuous.stop",0,0);if(fd<0)return 0;s3(6,fd,0,0);return 1;}
static int eqstr(const char*a,const char*b){while(*a&&*a==*b){a++;b++;}return *a==*b;}
static u32 nowms(void){long t[2];if(s3(263,1,(long)t,0)<0)return 0;return (u32)t[0]*1000+(u32)t[1]/1000000;}
struct address {u16 family,port;u32 ip;u8 zero[8];};
static int ready(int fd,int event,int ms){struct {int fd;short events,revents;} p={fd,(short)event,0};long r=s3(168,(long)&p,1,ms);return r>0&&(p.revents&event);}
static int exact(int fd,void*p,u32 n,int sending){u32 used=0;while(used<n){if(!ready(fd,sending?4:1,2000))return 1;long r=s3(sending?4:3,fd,(long)((u8*)p+used),n-used);if(r==-4)continue;if(r<=0)return 1;used+=(u32)r;}return 0;}
static int capture_channel;
static int connection(void){
 int fd=s3(281,2,1,0);if(fd<0){err("SOCKET",fd);return -1;}
 int one=1;call6(294,fd,1,2,(long)&one,4,0);
 struct address a={2,0x15c0,0x280114ac,{0}};if(capture_channel)a.port=0x16c0;
 if(s3(282,fd,(long)&a,16)<0||s3(284,fd,1,0)<0){s3(6,fd,0,0);text("LISTEN_FAILED\n");return -1;}
 text(capture_channel?"STREAM_READY_172.20.1.40_49174\n":"STREAM_READY_172.20.1.40_49173\n");
 if(!ready(fd,1,15000)){s3(6,fd,0,0);text("CONNECT_TIMEOUT\n");return -1;}
 struct address peer;u32 n=16;int c=s3(285,fd,(long)&peer,(long)&n);s3(6,fd,0,0);
 if(c<0)return -1;
 if(n!=16||peer.ip!=0x2d0114ac){s3(6,c,0,0);text("REFUSED_PEER\n");return -1;}
 static const char expected_token[]="OPULT26-UIFR-001";
 _Static_assert(sizeof(expected_token)==17,"16-byte session token");
 char token[16];if(exact(c,token,16,0)||!equal(token,expected_token,16)){s3(6,c,0,0);text("REFUSED_TOKEN\n");return -1;}return c;
}
static int capture(int kind){
 kv("capture_start_ms=",nowms());s3(27,20,0,0);
 int fd=s3(5,(long)"/dev/fb0",0,0);if(fd<0)return 1;
 struct fb_var_screeninfo v;struct fb_fix_screeninfo f;
 if(ioc(fd,FBIOGET_VSCREENINFO,&v)<0||ioc(fd,FBIOGET_FSCREENINFO,&f)<0||v.xres!=1920||v.yres!=720||v.bits_per_pixel!=32||f.line_length!=7680||f.smem_len<5529600||v.xoffset||v.yoffset||v.red.offset!=16||v.green.offset!=8||v.blue.offset){text("CAPTURE_REFUSED\n");return 2;}
 const u8*source=mapping(fd,5529600,1,0);const u16*overlay=0;
 if(kind==2){int layer=s3(5,(long)"/dev/fb1",0,0);struct fb_var_screeninfo fv;struct fb_fix_screeninfo ff;u32 blank=99;
  if(layer<0||ioc(layer,FBIOGET_VSCREENINFO,&fv)<0||ioc(layer,FBIOGET_FSCREENINFO,&ff)<0||ioc(layer,GETBLANK,&blank)<0||blank||fv.xres!=1920||fv.yres!=720||fv.bits_per_pixel!=16||fv.xoffset||fv.yoffset||ff.line_length!=3840||ff.smem_len<2764800){text("CAPTURE_OVERLAY_INACTIVE\n");return 2;}
  overlay=mapping(layer,2764800,1,0);if(!overlay)return 3;
 }
 u32 width=kind?1920:960,height=kind?720:360,step=kind?1:2,bytes=width*height*2;
 u16*out=mapping(-1,bytes,3,0);if(!source||!out)return 3;capture_channel=kind?1:0;
 int c=connection();if(c<0)return 4;
 for(u32 y=0;y<height;y++)for(u32 x=0;x<width;x++){u32 index=y*step*1920+x*step;u32 color=((const u32*)source)[index];u16 pixel=(u16)(((color>>8)&0xf800)|((color>>5)&0x07e0)|((color>>3)&31));if(overlay&&overlay[index])pixel=overlay[index];out[y*width+x]=pixel;}
 int fail=exact(c,out,bytes,1);s3(6,c,0,0);text(fail?"CAPTURE_SEND_FAILED\n":"CAPTURE_OK\n");return fail;
}
static void barrier(void){__asm__ volatile("dmb ish" ::: "memory");}
static int getmode(int fd,struct fb_var_screeninfo*v,struct fb_fix_screeninfo*f){long r;
 if((r=ioc(fd,FBIOGET_VSCREENINFO,v))<0)return err("GET_VAR",r);
 if((r=ioc(fd,FBIOGET_FSCREENINFO,f))<0)return err("GET_FIX",r);return 0;
}
static void modeprint(const struct fb_var_screeninfo*v,const struct fb_fix_screeninfo*f){
 kv("xres=",v->xres);kv("yres=",v->yres);kv("virtual_x=",v->xres_virtual);kv("virtual_y=",v->yres_virtual);
 kv("bpp=",v->bits_per_pixel);kv("stride=",f->line_length);kv("memory_bytes=",f->smem_len);kv("memory_address=",f->smem_start);
}
static int expected(const struct saved*s){return s->v.xres==240&&s->v.yres==320&&s->v.xres_virtual==240&&s->v.yres_virtual==960&&s->v.bits_per_pixel==16&&!s->v.xoffset&&!s->v.yoffset&&!s->v.nonstd&&s->v.red.offset==11&&s->v.red.length==5&&s->v.green.offset==5&&s->v.green.length==6&&s->v.blue.offset==0&&s->v.blue.length==5&&s->f.line_length==480&&s->f.smem_len>=460800&&s->f.smem_len<=8388608&&s->blank==4&&s->com==4&&s->gw==0x80000000U&&s->pos==0;}
struct video_mode {u32 name,refresh,xres,yres,pixclock,left,right,upper,lower,hsync,vsync,sync,vmode,flag;};
_Static_assert(sizeof(struct video_mode)==56,"ARM videomode ABI");
static struct video_mode video(const struct fb_var_screeninfo*v){struct video_mode m={0,60,v->xres,v->yres,v->pixclock,v->left_margin,v->right_margin,v->upper_margin,v->lower_margin,v->hsync_len,v->vsync_len,v->sync,v->vmode,0};return m;}
static int syswrite(const char*path,const void*p,u32 len,int zero_ok){int fd=s3(5,(long)path,1,0);if(fd<0)return err("SYSFS_OPEN",fd);long r=s3(4,fd,(long)p,len);s3(6,fd,0,0);if(r!=(long)len&&!(zero_ok&&r==0))return err("SYSFS_WRITE",r<0?r:-5);return 0;}
static int original_list(void){char b[80];int fd=s3(5,(long)"/sys/class/graphics/fb1/modes",0,0);if(fd<0)return 0;long n=s3(3,fd,(long)b,sizeof(b));s3(6,fd,0,0);const char*s="U:240x320p-60\n";return n==14&&equal(b,s,14);}
static int setmode(int wide){
 struct video_mode m[2];m[0]=video(&old.v);m[1]=video(&bgvar);
 if(syswrite("/sys/class/graphics/fb1/modes",m,wide?112:56,1))return 1;
 if(wide){const char*s="U:1920x720p-60\n";return syswrite("/sys/class/graphics/fb1/mode",s,15,0);}
 const char*s="U:240x320p-60\n";
 if(syswrite("/sys/class/graphics/fb1/mode",s,14,0))return 1;
 s="240,960\n";return syswrite("/sys/class/graphics/fb1/virtual_size",s,8,0);
}
static int restore(void){
 text("RESTORING_FOREGROUND\n");int ok=1;long r=ioc(fg,FBIOBLANK,(void*)4);if(r<0)r=ioc(fg,FBIOBLANK,(void*)4);if(r<0){err("RESTORE_BLANK",r);ok=0;}
 struct fb_var_screeninfo v=old.v;struct fb_fix_screeninfo f;
 if(setmode(0)){text("RESTORE_MODE_RETRY\n");if(setmode(0))ok=0;}
 if(getmode(fg,&v,&f)){ok=0;}else{
  modeprint(&v,&f);
  if(v.xres!=old.v.xres||v.yres!=old.v.yres||v.xres_virtual!=old.v.xres_virtual||v.yres_virtual!=old.v.yres_virtual||v.bits_per_pixel!=old.v.bits_per_pixel||v.xoffset!=old.v.xoffset||v.yoffset!=old.v.yoffset||f.line_length!=old.f.line_length||f.smem_len<old.f.smem_len||f.smem_len>8388608){text("RESTORE_GEOMETRY_MISMATCH_NO_PIXEL_WRITE\n");ok=0;}
  else {u8*p=mapping(fg,old.f.smem_len,3,0);if(!p)ok=0;else{memcpy(p,backup,old.f.smem_len);barrier();u32 h=hash(p,old.f.smem_len);kv("restored_fnv=",h);if(h!=old.hash)ok=0;unmap(p,old.f.smem_len);}}
  if(f.smem_len!=old.f.smem_len)text("NOTE_DMA_CAPACITY_RETAINED_UNTIL_DRIVER_REINIT\n");
 }
 /* Baseline has black key 0 and the BG global alpha enabled at 128. */
 int key[2]={0,0},alpha[2]={1,128};
 if((r=ioc(fg,KEY,key))<0){err("RESTORE_KEY",r);ok=0;}
 if((r=ioc(bg,ALPHA,alpha))<0){err("RESTORE_ALPHA",r);ok=0;}
 if((r=ioc(fg,FBIOBLANK,(void*)4))<0){err("FINAL_BLANK",r);ok=0;}
 nap(1);u32 blank=99;if(ioc(fg,GETBLANK,&blank)<0||blank!=old.blank)ok=0;
 kv("restored_blank=",blank);kv("restored_com=",regs[0]);kv("restored_gw=",regs[1]);kv("restored_pos=",regs[2]);
 if(regs[0]!=old.com||regs[1]!=old.gw||regs[2]!=old.pos)ok=0;
 if(!original_list()){text("RESTORE_MODELIST_MISMATCH\n");ok=0;}
 struct fb_var_screeninfo b;struct fb_fix_screeninfo bf;if(getmode(bg,&b,&bf)||!equal(&b,&bgvar,sizeof(b))) {text("BG_VAR_CHANGED\n");ok=0;}
 text(ok?"RESTORE_OK\n":"RESTORE_FAILED_FOREGROUND_LEFT_BLANK\n");return ok?0:9;
}
static void copy32(void*d,const void*s,u32 bytes){u32*out=d;const u32*in=s;for(u32 i=0;i<bytes/4;i++)out[i]=in[i];}
static u16 rgb565(u32 v){return (u16)(((v>>8)&0xf800)|((v>>5)&0x07e0)|((v>>3)&31));}
static u16 mix565(u16 front,u16 back,u32 alpha){
 /* Interpolate channels separately: 8-bit weights overflow the packed RB gap. */
 u32 inv=256-alpha;
 u32 red=(((front>>11)*alpha+(back>>11)*inv)>>8)&31;
 u32 green=((((front>>5)&63)*alpha+((back>>5)&63)*inv)>>8)&63;
 u32 blue=(((front&31)*alpha+(back&31)*inv)>>8)&31;
 return (u16)((red<<11)|(green<<5)|blue);
}
static u32 ease(u32 pos,u32 length){if(pos>=length)return 256;return pos*pos*(3*length-2*pos)*256/(length*length*length);}
static u16 mix_dither565(u16 front,u16 back,u32 a,u32 phase){
 static const u8 threshold[16]={0,8,2,10,12,4,14,6,3,11,1,9,15,7,13,5};u32 d=(u32)threshold[phase&15]*16+8,inv=256-a;
 u32 red=(((front>>11)*a+(back>>11)*inv+d)>>8)&31;
 u32 green=((((front>>5)&63)*a+((back>>5)&63)*inv+d)>>8)&63;
 u32 blue=(((front&31)*a+(back&31)*inv+d)>>8)&31;
 return (u16)((red<<11)|(green<<5)|blue);
}
static void blend_color(u16*p,u32 i,u16 back,u32 a,u32 phase){u16 v=mix_dither565(p[i],back,a,phase);p[i]=v?v:1;}
/* 40% gradient profile, matching the HTML comparison slider at 40.
 * OEM opacity at normalized positions 0/45/78/100%: 90/82/37.2/0%.
 * Read live OEM pixels for both strips; never use a saved icon or a color key
 * to identify warnings. Recovery and OEM foreground backup stay intact. */
static u16 band_top_weight[96],band_bottom_weight[60];static int weights_ready;
static u32 band_weight(u32 distance,u32 last){
 static const u32 positions[4]={0,4500,7800,10000},weights[4]={230,210,95,0};
 u32 pos=distance*10000/last;
 for(u32 n=1;n<4;n++)if(pos<=positions[n])return weights[n-1]-(weights[n-1]-weights[n])*(pos-positions[n-1])/(positions[n]-positions[n-1]);
 return 0;
}
static u32 oem_bands[1920*(96+60)],oem_left[564],oem_right[564];
static int read_exact_fd(int fd,void*p,u32 count){u32 n=0;while(n<count){long k=s3(3,fd,(long)((u8*)p+n),count-n);if(k==-4)continue;if(k<=0)return 1;n+=(u32)k;}return 0;}
static int snapshot_bands(const u32*source){
 if(s3(19,bg,0,0)<0||read_exact_fd(bg,oem_bands,737280)||s3(19,bg,5068800,0)<0||read_exact_fd(bg,oem_bands+184320,460800))return 1;
 for(u32 y=96;y<660;y++){oem_left[y-96]=source[y*1920];oem_right[y-96]=source[y*1920+1919];}
 return 0;
}
static void protect(u8*canvas,const u32*oem){
 u16*p=(u16*)canvas;
 if(!weights_ready){for(u32 y=0;y<96;y++)band_top_weight[y]=(u16)band_weight(y,95);for(u32 y=0;y<60;y++)band_bottom_weight[y]=(u16)band_weight(y,59);weights_ready=1;}
 for(u32 y=0;y<720;y++){
  if(y>=96&&y<660)continue;
  u32 native=y<96?band_top_weight[y]:band_bottom_weight[719-y],a=256-native,row=y*1920;
  for(u32 x=0;x<1920;x++)blend_color(p,row+x,rgb565(oem[(y<96?y:y-660+96)*1920+x]),a,((y&3)<<2)|(x&3));
 }
 for(u32 i=96*1920;i<660*1920;i++)if(!p[i])p[i]=1;
 for(u32 y=96;y<660;y++){
  u32 row=y*1920;u16 left=rgb565(oem_left[y-96]),right=rgb565(oem_right[y-96]);
  for(u32 x=0;x<32;x++){u32 f=ease(x,32);blend_color(p,row+x,left,f,((y&3)<<2)|(x&3));blend_color(p,row+1919-x,right,f,((y&3)<<2)|((1919-x)&3));}
 }
}
static int worker(void){
 int c=connection();if(c<0)return 3;
 struct fb_var_screeninfo source_v;struct fb_fix_screeninfo source_f;
 if(getmode(bg,&source_v,&source_f)||source_v.xres!=1920||source_v.yres!=720||source_v.bits_per_pixel!=32||source_v.xoffset||source_v.yoffset||source_v.red.offset!=16||source_v.green.offset!=8||source_v.blue.offset||source_f.line_length!=7680||source_f.smem_len<5529600||source_f.smem_len>33554432){text("REFUSED_OEM_READ_GEOMETRY\n");return 3;}
 const u32*oem=mapping(bg,5529600,1,0);
 u8*input=mapping(-1,1048576,3,0);u8*canvas=mapping(-1,2764800,3,0);if(!oem||!input||!canvas){s3(6,c,0,0);return 3;}
 struct fb_var_screeninfo v=old.v;struct fb_fix_screeninfo f;
 long r=0;if(setmode(1))return 4;
 if(getmode(fg,&v,&f))return 4;
 modeprint(&v,&f);
 if(v.xres!=1920||v.yres!=720||v.xres_virtual!=1920||v.yres_virtual!=720||v.bits_per_pixel!=16||v.xoffset||v.yoffset||f.line_length!=3840||f.smem_len<2764800||f.smem_len>8388608){text("REFUSED_WIDE_GEOMETRY\n");return 4;}
 u8*p=mapping(fg,f.smem_len,3,0);if(!p)return 5;
 memset(p,0,f.smem_len);memset(canvas,0,2764800);barrier();
 int alpha[2]={1,255},key[2]={1,0};
 if((r=ioc(fg,ALPHA,alpha))<0)return err("FG_ALPHA",r);
 if((r=ioc(fg,KEY,key))<0)return err("BLACK_KEY",r);
 if((regs[0]&15)!=14||regs[1]!=0xff000000U||regs[2]){text("REFUSED_BLEND_STATE\n");return 5;}
 if((r=ioc(fg,FBIOBLANK,(void*)0))<0)return err("ENABLE_WIDE",r);
 struct fb_fix_screeninfo actual;if(ioc(fg,FBIOGET_FSCREENINFO,&actual)<0||actual.smem_start!=f.smem_start||actual.smem_len!=f.smem_len){ioc(fg,FBIOBLANK,(void*)4);text("BUFFER_MOVED_AFTER_ENABLE\n");return 6;}
 if((regs[0]&15)!=15||regs[1]!=0xff000000U||regs[2]){ioc(fg,FBIOBLANK,(void*)4);text("REFUSED_ACTIVE_BLEND_STATE\n");return 7;}
 kv("visible_com=",regs[0]);kv("visible_gw=",regs[1]);
 u32 start=nowms(),first=0,last=0,frames=0,max_gap=0,gaps50=0,gaps100=0,blit_total=0,blit_max=0,inflate_total=0,blend_total=0,copy_total=0,source_last=0,last_seq=0,failed=0,messages=0,paused=0,pauses=0;
 text("APP_CONTINUOUS_UNTIL_STOP_OR_SOURCE_LOSS\n");
 while(1){
  u32 header[4];if(exact(c,header,16,0)){text("STREAM_ENDED_OR_TIMEOUT\n");break;}
  if(header[0]==0x56505032U&&header[2]==0){
   if(messages&&header[1]<=last_seq){text("REFUSED_PAUSE_SEQUENCE\n");failed=1;break;}
   if(!paused){memset(p,0,2764800);barrier();text("CANVAS_PAUSED_NATIVE_VISIBLE\n");pauses++;}
   paused=1;last_seq=header[1];messages++;if(lease)*lease=nowms();continue;
  }
  if(header[0]!=0x56504632U||(header[2]<6||header[2]>1048576)||(messages&&header[1]<=last_seq)){text("REFUSED_FRAME_HEADER\n");failed=1;break;}
  if(exact(c,input,header[2],0)){text("TRUNCATED_FRAME\n");failed=1;break;}
  u32 got=nowms();
  if(!frames){first=got;text("FIRST_VIDEO_FRAME\n");}
  else{u32 gap=got-last;if(gap>max_gap)max_gap=gap;if(gap>50)gaps50++;if(gap>100)gaps100++;}
  u32 begin=nowms();unsigned int decoded=2764800;if(tinf_zlib_uncompress(canvas,&decoded,input,header[2])!=TINF_OK||decoded!=2764800){text("INVALID_COMPRESSED_FRAME\n");failed=1;break;}u32 inflated=nowms();if(snapshot_bands(oem)){text("OEM_READ_FAILED_RESTORE\n");failed=1;break;}protect(canvas,oem_bands);u32 blended=nowms();copy32(p,canvas,2764800);barrier();u32 completed=nowms();inflate_total+=inflated-begin;blend_total+=blended-inflated;copy_total+=completed-blended;u32 duration=completed-begin;
  blit_total+=duration;if(duration>blit_max)blit_max=duration;
  if(paused){text("CANVAS_RESUMED\n");paused=0;}messages++;
  last=got;source_last=header[3];last_seq=header[1];frames++;if(lease)*lease=nowms();
  if(frames%150==0){kv("frames_progress=",frames);kv("elapsed_progress_ms=",got-first);}
 }
 ioc(fg,FBIOBLANK,(void*)4);s3(6,c,0,0);
 kv("canvas_pauses=",pauses);kv("frames=",frames);kv("frame_span_ms=",frames?last-first:0);kv("source_elapsed_ms=",source_last);kv("max_gap_ms=",max_gap);kv("gaps_over_50ms=",gaps50);kv("gaps_over_100ms=",gaps100);kv("inflate_total_ms=",inflate_total);kv("blend_total_ms=",blend_total);kv("copy_total_ms=",copy_total);kv("compose_blit_total_ms=",blit_total);kv("compose_blit_max_ms=",blit_max);kv("last_sequence=",last_seq);
 unmap(p,f.smem_len);unmap(input,1048576);unmap(canvas,2764800);text("WORKER_STOPPED\n");return failed?8:0;
}
int run(long*stack){
 if(stack[0]>1&&eqstr(((char**)stack)[2],"capture"))return capture(0);
 if(stack[0]>1&&eqstr(((char**)stack)[2],"capture_full"))return capture(1);
 if(stack[0]>1&&eqstr(((char**)stack)[2],"capture_ui"))return capture(2);
 char mode=stack[0]>1?((char**)stack)[2][0]:'p';int show=mode=='s',recover=mode=='r',change=show||recover;
 fg=s3(5,(long)"/dev/fb1",change?2:0,0);if(fg<0)return err("OPEN_FG",fg);
 bg=s3(5,(long)"/dev/fb0",change?2:0,0);if(bg<0)return err("OPEN_BG",bg);
 int mem=s3(5,(long)"/dev/mem",0,0);if(mem<0)return err("OPEN_MEM_READONLY",mem);
 regs=mapping(mem,4096,1,0x02740000U>>12);if(!regs)return 2;s3(6,mem,0,0);
 struct fb_fix_screeninfo bf;if(getmode(bg,&bgvar,&bf)||bgvar.xres!=1920||bgvar.yres!=720||bgvar.bits_per_pixel!=32||bf.line_length!=7680){text("REFUSED_BG_GEOMETRY\n");return 3;}
 if(recover){if(fileio(ROOT "original.state",&old,sizeof(old),0))return 2;}
 else {
  if(getmode(fg,&old.v,&old.f))return 2;
  if(ioc(fg,GETBLANK,&old.blank)<0)return 2;
  u32 channel=0;if(ioc(fg,0x80044625U,&channel)<0||channel!=0x0a6ff7ffU)return 2;
  old.magic=0x57494445;old.com=regs[0];old.gw=regs[1];old.pos=regs[2];
  modeprint(&old.v,&old.f);kv("blank=",old.blank);kv("com=",old.com);kv("gw=",old.gw);kv("pos=",old.pos);
 }
 if(!change){text("PROBE_ONLY\n");return 0;}
 if(show&&!original_list()){text("REFUSED_UNEXPECTED_MODELIST\n");return 3;}
 if(old.magic!=0x57494445||!expected(&old)){text("REFUSED_UNEXPECTED_BASELINE\n");return 3;}
 backup=mapping(-1,old.f.smem_len,3,0);if(!backup)return 2;
 if(recover){if(fileio(ROOT "original.pixels",backup,old.f.smem_len,0)||hash(backup,old.f.smem_len)!=old.hash){text("REFUSED_BAD_BACKUP\n");return 3;}return restore();}
 u8*p=mapping(fg,old.f.smem_len,1,0);if(!p)return 2;memcpy(backup,p,old.f.smem_len);unmap(p,old.f.smem_len);old.hash=hash(backup,old.f.smem_len);kv("backup_fnv=",old.hash);
 if(fileio(ROOT "original.pixels",backup,old.f.smem_len,1)||fileio(ROOT "original.state",&old,sizeof(old),1))return 2;
 long shared=call6(192,0,4096,3,0x21,-1,0);if((u32)shared>=0xfffff001U)return err("SHARED_LEASE",shared);lease=(volatile u32*)shared;*lease=nowms();
 long child=s3(2,0,0,0);if(child<0)return err("FORK",child);
 if(!child)return worker();
 text("RESTORE_GUARD_ACTIVE_5_SECOND_FRAME_LEASE\n");int status=0;long wait=0;
 for(;;){wait=call6(114,child,(long)&status,1,0,0,0);if(wait==child)break;if(user_stop()){text("USER_STOP_REQUESTED\n");break;}if((u32)(nowms()-*lease)>5000){text("FRAME_LEASE_EXPIRED\n");break;}nap(1);}
 if(wait!=child){text("GUARD_TIMEOUT_KILL\n");s3(37,child,9,0);do{wait=call6(114,child,(long)&status,0,0,0,0);}while(wait==-4);}
 kv("worker_status=",status);return restore();
}
__attribute__((naked,noreturn)) void _start(void){__asm__ volatile("mov r0, sp\nbl run\nmov r7, #1\nsvc 0\n");}
