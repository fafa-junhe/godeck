//go:build cgo && linux

package media

/*
#cgo pkg-config: libva libva-drm
#include <va/va.h>
#include <va/va_drm.h>
#include <fcntl.h>
#include <unistd.h>
#include <errno.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
static int godeck_va_probe(const char *selected,int want,int *enc,int *dec,char *vendor,int len,char *node,int node_len) {
 int permission=0, found=0, initialized=0;*enc=*dec=0;
 for(int n=128;n<144;n++){
  char path[256];if(selected&&selected[0]){if(n>128)break;snprintf(path,sizeof(path),"%s",selected);}else snprintf(path,sizeof(path),"/dev/dri/renderD%d",n);
  int fd=open(path,O_RDWR|O_CLOEXEC);if(fd<0){if(errno==EACCES||errno==EPERM)permission=1;continue;}found=1;
  VADisplay d=vaGetDisplayDRM(fd);int major=0,minor=0;if(!d||vaInitialize(d,&major,&minor)!=VA_STATUS_SUCCESS){close(fd);continue;}initialized=1;
  int device_enc=0,device_dec=0;const char *v=vaQueryVendorString(d);if(v&&vendor&&len>0)snprintf(vendor,len,"%s",v);
  int capacity=vaMaxNumProfiles(d),count=0;VAProfile *profiles=calloc((size_t)capacity,sizeof(*profiles));
  if(profiles&&vaQueryConfigProfiles(d,profiles,&count)==VA_STATUS_SUCCESS){
   for(int i=0;i<count;i++){
    if(profiles[i]!=VAProfileH264ConstrainedBaseline&&profiles[i]!=VAProfileH264Main&&profiles[i]!=VAProfileH264High)continue;
    int ec=vaMaxNumEntrypoints(d),nentries=0;VAEntrypoint *entries=calloc((size_t)ec,sizeof(*entries));if(!entries)continue;
    if(vaQueryConfigEntrypoints(d,profiles[i],entries,&nentries)==VA_STATUS_SUCCESS){
     for(int j=0;j<nentries;j++){
      if(entries[j]!=VAEntrypointVLD&&entries[j]!=VAEntrypointEncSlice)continue;
      VAConfigAttrib a={.type=VAConfigAttribRTFormat,.value=0};
      if(vaGetConfigAttributes(d,profiles[i],entries[j],&a,1)==VA_STATUS_SUCCESS&&a.value!=VA_ATTRIB_NOT_SUPPORTED&&(a.value&VA_RT_FORMAT_YUV420)){
       if(entries[j]==VAEntrypointEncSlice)device_enc=1;if(entries[j]==VAEntrypointVLD)device_dec=1;
      }
     }
    }free(entries);
   }
  }free(profiles);vaTerminate(d);close(fd);*enc=device_enc;*dec=device_dec;if(node&&node_len>0)snprintf(node,node_len,"%s",path);if((want==1&&*enc)||(want==2&&*dec)||(want==0&&*enc&&*dec))return 0;
 }
 if(initialized)return 1;if(permission)return 2;if(found)return 3;return 4;
}
*/
import "C"
import "unsafe"

type VACapability struct { Encode,Decode bool; Vendor,Device string; Reason string }
// ProbeVA queries actual libva H.264 profile entrypoints. Plugin availability
// and successful pipeline initialization must be checked separately.
func ProbeVA()VACapability{return probeVA("",0)}
func ProbeVAPath(path string)VACapability{return probeVA(path,0)}
func ProbeVAEncode()VACapability{return probeVA("",1)}
func ProbeVADecode()VACapability{return probeVA("",2)}
func probeVA(path string,want int)VACapability {var enc,dec C.int;var vendor [256]C.char;var node [256]C.char;selected:=C.CString(path);defer C.free(unsafe.Pointer(selected));status:=C.godeck_va_probe(selected,C.int(want),&enc,&dec,(*C.char)(unsafe.Pointer(&vendor[0])),C.int(len(vendor)),(*C.char)(unsafe.Pointer(&node[0])),C.int(len(node)));result:=VACapability{Encode:enc!=0,Decode:dec!=0,Vendor:C.GoString(&vendor[0]),Device:C.GoString(&node[0])};switch status{case 1:result.Reason="VA 驱动未报告完整 H.264 8 位编码与解码能力";case 2:result.Reason="无法访问图形 render 节点；请检查当前用户的设备权限";case 3:result.Reason="VA 驱动初始化失败；请检查发行版的 VA-API 用户态驱动";case 4:result.Reason="未找到图形 render 节点"};return result}
