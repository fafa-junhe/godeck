//go:build linux && cgo

package media

/*
#cgo pkg-config: libavcodec libavutil libswscale
#include <stdlib.h>
#include "av_linux.h"
*/
import "C"
import (
 "errors"
 "fmt"
 "strings"
 "sync"
 "time"
 "unsafe"
)

// ErrAVAgain means input was accepted but the codec has no output yet.
var ErrAVAgain = errors.New("hardware codec needs more input")
// ErrAVCorrupt means a corrupted bitstream/frame was discarded cleanly; wait for next IDR.
var ErrAVCorrupt = errors.New("corrupted video frame discarded")
type AVSample struct {Data []byte; PTS time.Duration; Key bool}
type AVFrame struct {Data []byte; Width,Height,Stride int; PTS time.Duration}
type AVEncoder struct {mu sync.Mutex; ptr *C.godeck_av; width,height,level int; device string; force,verified bool}
type AVDecoder struct {mu sync.Mutex; ptr *C.godeck_av; device string; verified bool}
func avDevice(device string)(string,error){if device==""{device="/dev/dri/renderD128"};if !strings.HasPrefix(device,"/dev/dri/renderD")||strings.ContainsAny(device,"\x00\r\n"){return "",errors.New("VA-API requires an explicit DRM render node")};return device,nil}
func avError(p *C.char)error{if p==nil{return errors.New("FFmpeg VA-API failed")};defer C.godeck_av_error_free(p);return errors.New(C.GoString(p))}
func NewAVEncoder(width,height int,device string)(*AVEncoder,error){return NewAVEncoderWithProfile(width,height,30,31,device)}
// NewAVEncoderWithProfile requires the selected WebRTC constrained-baseline level_idc.
func NewAVEncoderWithProfile(width,height,fps,level int,device string)(*AVEncoder,error){maxFS,maxMBPS:=0,0;switch level{case 31:maxFS,maxMBPS=3600,108000;case 40:maxFS,maxMBPS=8192,245760;case 42:maxFS,maxMBPS=8704,522240;default:return nil,errors.New("unsupported negotiated H.264 level")};if width<=0||height<=0||width%2!=0||height%2!=0||width>4096||height>4096||fps<=0||fps>60{return nil,errors.New("invalid NV12 dimensions or frame rate")};mb:=((width+15)/16)*((height+15)/16);if mb>maxFS||mb*fps>maxMBPS{return nil,errors.New("frame dimensions or rate exceed negotiated H.264 level")};device,e:=avDevice(device);if e!=nil{return nil,e};d:=C.CString(device);defer C.free(unsafe.Pointer(d));var ce *C.char;p:=C.godeck_av_encoder(C.int(width),C.int(height),C.int(fps),C.int(level),d,&ce);if p==nil{return nil,avError(ce)};return &AVEncoder{ptr:p,width:width,height:height,level:level,device:device},nil}
func NewAVDecoder(device string)(*AVDecoder,error){device,e:=avDevice(device);if e!=nil{return nil,e};d:=C.CString(device);defer C.free(unsafe.Pointer(d));var ce *C.char;p:=C.godeck_av_decoder(d,&ce);if p==nil{return nil,avError(ce)};return &AVDecoder{ptr:p,device:device},nil}
func(e *AVEncoder) DevicePath()string{return e.device}
func(d *AVDecoder) DevicePath()string{return d.device}
func(e *AVEncoder) Backend()string{return "FFmpeg h264_vaapi"}
func(d *AVDecoder) Backend()string{return "FFmpeg H.264 VA-API"}
func(e *AVEncoder) Verified()bool{e.mu.Lock();defer e.mu.Unlock();return e.verified}
func(d *AVDecoder) Verified()bool{d.mu.Lock();defer d.mu.Unlock();return d.verified}
func(e *AVEncoder) ForceKey(){e.mu.Lock();defer e.mu.Unlock();e.force=true}
// EncodeNV12 accepts tightly packed Y followed by interleaved UV, at microsecond PTS precision.
func(e *AVEncoder) EncodeNV12(data []byte,pts time.Duration)(AVSample,error){e.mu.Lock();defer e.mu.Unlock();if e.ptr==nil{return AVSample{},errors.New("encoder closed")};if len(data)!=e.width*e.height*3/2||pts<0{return AVSample{},errors.New("invalid NV12 frame or PTS")};var out C.godeck_av_output;var ce *C.char;force:=C.int(0);if e.force{force=1};r:=C.godeck_av_encode(e.ptr,(*C.uint8_t)(unsafe.Pointer(&data[0])),C.size_t(len(data)),C.int64_t(pts/time.Microsecond),force,&out,&ce);if r<0{return AVSample{},avError(ce)};e.force=false;if r==0{return AVSample{},ErrAVAgain};defer C.godeck_av_output_free(&out);b:=C.GoBytes(unsafe.Pointer(out.data),out.size);if err:=validateH264(b,out.key!=0,e.level);err!=nil{return AVSample{},err};e.verified=true;return AVSample{Data:b,PTS:time.Duration(out.pts)*time.Microsecond,Key:out.key!=0},nil}
// Decode requires a complete Annex B H.264 access unit and returns a CPU-readback RGBA frame.
func(d *AVDecoder) Decode(data []byte,pts time.Duration)(AVFrame,error){d.mu.Lock();defer d.mu.Unlock();if d.ptr==nil{return AVFrame{},errors.New("decoder closed")};if len(data)==0||len(data)>32<<20||pts<0{return AVFrame{},errors.New("invalid H.264 access unit or PTS")};var out C.godeck_av_output;var ce *C.char;r:=C.godeck_av_decode(d.ptr,(*C.uint8_t)(unsafe.Pointer(&data[0])),C.size_t(len(data)),C.int64_t(pts/time.Microsecond),&out,&ce);if r==-2{if ce!=nil{C.godeck_av_error_free(ce)};return AVFrame{},ErrAVCorrupt};if r<0{return AVFrame{},avError(ce)};if r==0{return AVFrame{},ErrAVAgain};defer C.godeck_av_output_free(&out);d.verified=true;return AVFrame{Data:C.GoBytes(unsafe.Pointer(out.data),out.size),Width:int(out.width),Height:int(out.height),Stride:int(out.stride),PTS:time.Duration(out.pts)*time.Microsecond},nil}
func(e *AVEncoder) Close(){e.mu.Lock();defer e.mu.Unlock();if e.ptr!=nil{C.godeck_av_close(e.ptr);e.ptr=nil}}
func(d *AVDecoder) Close(){d.mu.Lock();defer d.mu.Unlock();if d.ptr!=nil{C.godeck_av_close(d.ptr);d.ptr=nil}}
// Reject packetization/profile mismatches instead of claiming WebRTC compatibility.
func validateH264(b []byte,key bool,level int)error{var starts []int;for i:=0;i+3<len(b);{n:=0;if b[i]==0&&b[i+1]==0&&b[i+2]==1{n=3}else if i+4<len(b)&&b[i]==0&&b[i+1]==0&&b[i+2]==0&&b[i+3]==1{n=4};if n>0{starts=append(starts,i+n);i+=n}else{i++}};if len(starts)==0||starts[0]>4{return errors.New("hardware encoder did not produce Annex B")};sps,pps,idr:=false,false,false;for _,i:=range starts{if i>=len(b){return errors.New("invalid H.264 NAL")};switch b[i]&31{case 7:if i+3>=len(b)||b[i+1]!=0x42||(b[i+2]&0x4f)!=0x40||int(b[i+3])!=level{return fmt.Errorf("hardware encoder H.264 profile/level incompatible with negotiation")};sps=true;case 8:pps=true;case 5:idr=true}};if key&&(!sps||!pps||!idr){return errors.New("hardware keyframe lacks SPS/PPS or IDR")};return nil}

