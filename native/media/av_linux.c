//go:build linux && cgo

#include "av_linux.h"
#include <libavcodec/avcodec.h>
#include <libavutil/hwcontext.h>
#include <libavutil/imgutils.h>
#include <libavutil/opt.h>
#include <libavutil/error.h>
#include <libswscale/swscale.h>
#include <errno.h>
#include <stdlib.h>
#include <string.h>
#include <stdio.h>

#define MAX_OUTPUT (64*1024*1024)
#define QUEUE_MAX 8
struct godeck_av { AVCodecContext *codec; AVBufferRef *device; struct SwsContext *sws; int width,height,encode; godeck_av_output queue[QUEUE_MAX]; int head,count; };
static int failure(char **out,const char *what,int code){char detail[AV_ERROR_MAX_STRING_SIZE];av_strerror(code,detail,sizeof(detail));char buf[512];snprintf(buf,sizeof(buf),"%s: %s",what,detail);if(out)*out=strdup(buf);return -1;}
static int msg(char **out,const char *what){if(out)*out=strdup(what);return -1;}
void godeck_av_error_free(char *p){free(p);}
void godeck_av_output_free(godeck_av_output *o){free(o->data);memset(o,0,sizeof(*o));}
void godeck_av_close(godeck_av *p){if(!p)return;for(int i=0;i<QUEUE_MAX;i++)godeck_av_output_free(&p->queue[i]);sws_freeContext(p->sws);avcodec_free_context(&p->codec);av_buffer_unref(&p->device);free(p);}
static enum AVPixelFormat hw_format(AVCodecContext *ctx,const enum AVPixelFormat *formats){(void)ctx;for(;*formats!=AV_PIX_FMT_NONE;formats++)if(*formats==AV_PIX_FMT_VAAPI)return *formats;return AV_PIX_FMT_NONE;}
static godeck_av *open_device(const char *path,char **error){godeck_av *p=calloc(1,sizeof(*p));if(!p){msg(error,"allocation failed");return NULL;}int r=av_hwdevice_ctx_create(&p->device,AV_HWDEVICE_TYPE_VAAPI,path,NULL,0);if(r<0){failure(error,"VA-API device initialization failed",r);godeck_av_close(p);return NULL;}return p;}
godeck_av *godeck_av_encoder(int w,int h,int fps,int level,const char *device,char **error){
 godeck_av *p=open_device(device,error);if(!p)return NULL;const AVCodec *codec=avcodec_find_encoder_by_name("h264_vaapi");if(!codec){msg(error,"FFmpeg h264_vaapi encoder unavailable");goto fail;}
 p->codec=avcodec_alloc_context3(codec);if(!p->codec){msg(error,"encoder allocation failed");goto fail;}p->encode=1;p->width=w;p->height=h;
 AVCodecContext *c=p->codec;c->width=w;c->height=h;c->time_base=(AVRational){1,1000000};c->framerate=(AVRational){fps,1};c->pix_fmt=AV_PIX_FMT_VAAPI;c->bit_rate=4000000;c->gop_size=60;c->max_b_frames=0;c->refs=1;
 AVBufferRef *frames=av_hwframe_ctx_alloc(p->device);if(!frames){msg(error,"VA frame pool allocation failed");goto fail;}AVHWFramesContext *fc=(AVHWFramesContext*)frames->data;fc->format=AV_PIX_FMT_VAAPI;fc->sw_format=AV_PIX_FMT_NV12;fc->width=w;fc->height=h;fc->initial_pool_size=12;int r=av_hwframe_ctx_init(frames);if(r<0){av_buffer_unref(&frames);failure(error,"VA frame pool initialization failed",r);goto fail;}c->hw_frames_ctx=frames;
 char level_str[8];snprintf(level_str,sizeof(level_str),"%d",level);if((r=av_opt_set(c->priv_data,"profile","constrained_baseline",0))<0||(r=av_opt_set(c->priv_data,"level",level_str,0))<0||(r=av_opt_set(c->priv_data,"coder","cavlc",0))<0){failure(error,"cannot set WebRTC H.264 profile",r);goto fail;}
 if((r=avcodec_open2(c,codec,NULL))<0){failure(error,"hardware H.264 encoder initialization failed",r);goto fail;}return p;
 fail:godeck_av_close(p);return NULL;
}
godeck_av *godeck_av_decoder(const char *device,char **error){
 godeck_av *p=open_device(device,error);if(!p)return NULL;const AVCodec *codec=avcodec_find_decoder(AV_CODEC_ID_H264);if(!codec){msg(error,"FFmpeg H.264 decoder unavailable");goto fail;}p->codec=avcodec_alloc_context3(codec);if(!p->codec){msg(error,"decoder allocation failed");goto fail;}
 p->codec->get_format=hw_format;p->codec->hw_device_ctx=av_buffer_ref(p->device);p->codec->pkt_timebase=(AVRational){1,1000000};if(!p->codec->hw_device_ctx){msg(error,"device reference allocation failed");goto fail;}int r=avcodec_open2(p->codec,codec,NULL);if(r<0){failure(error,"hardware H.264 decoder initialization failed",r);goto fail;}return p;
 fail:godeck_av_close(p);return NULL;
}
static int append(godeck_av *p,godeck_av_output *o,char **error){if(p->count==QUEUE_MAX){godeck_av_output_free(o);return msg(error,"codec output queue overflow");}p->queue[(p->head+p->count)%QUEUE_MAX]=*o;p->count++;memset(o,0,sizeof(*o));return 0;}
static int take(godeck_av *p,godeck_av_output *out){if(!p->count)return 0;*out=p->queue[p->head];memset(&p->queue[p->head],0,sizeof(*out));p->head=(p->head+1)%QUEUE_MAX;p->count--;return 1;}
static int drain_encoder(godeck_av *p,char **error){AVPacket *pkt=av_packet_alloc();if(!pkt)return msg(error,"packet allocation failed");int r;while((r=avcodec_receive_packet(p->codec,pkt))>=0){if(pkt->size<=0||pkt->size>MAX_OUTPUT){av_packet_free(&pkt);return msg(error,"invalid encoded packet size");}godeck_av_output o={0};o.data=malloc(pkt->size);if(!o.data){av_packet_free(&pkt);return msg(error,"packet allocation failed");}memcpy(o.data,pkt->data,pkt->size);o.size=pkt->size;o.pts=pkt->pts;o.key=(pkt->flags&AV_PKT_FLAG_KEY)!=0;if(append(p,&o,error)<0){av_packet_free(&pkt);return -1;}av_packet_unref(pkt);}av_packet_free(&pkt);if(r!=AVERROR(EAGAIN)&&r!=AVERROR_EOF)return failure(error,"hardware encoding failed",r);return 0;}
int godeck_av_encode(godeck_av *p,const uint8_t *data,size_t len,int64_t pts,int force,godeck_av_output *out,char **error){
 if(!p||!p->encode||len!=(size_t)p->width*p->height*3/2)return msg(error,"invalid NV12 frame");AVFrame *sw=av_frame_alloc(),*hw=av_frame_alloc();if(!sw||!hw){av_frame_free(&sw);av_frame_free(&hw);return msg(error,"frame allocation failed");}int r;sw->format=AV_PIX_FMT_NV12;sw->width=p->width;sw->height=p->height;if((r=av_frame_get_buffer(sw,32))<0){failure(error,"NV12 allocation failed",r);goto fail;}for(int y=0;y<p->height;y++)memcpy(sw->data[0]+(size_t)y*sw->linesize[0],data+(size_t)y*p->width,p->width);for(int y=0;y<p->height/2;y++)memcpy(sw->data[1]+(size_t)y*sw->linesize[1],data+(size_t)p->width*p->height+(size_t)y*p->width,p->width);
 if((r=av_hwframe_get_buffer(p->codec->hw_frames_ctx,hw,0))<0){failure(error,"VA frame allocation failed",r);goto fail;}if((r=av_hwframe_transfer_data(hw,sw,0))<0){failure(error,"NV12 upload failed",r);goto fail;}hw->pts=pts;if(force)hw->pict_type=AV_PICTURE_TYPE_I;
 r=avcodec_send_frame(p->codec,hw);if(r==AVERROR(EAGAIN)){if(drain_encoder(p,error)<0)goto fail;r=avcodec_send_frame(p->codec,hw);}if(r<0){failure(error,"sending hardware frame failed",r);goto fail;}if(drain_encoder(p,error)<0)goto fail;av_frame_free(&sw);av_frame_free(&hw);return take(p,out);
 fail:av_frame_free(&sw);av_frame_free(&hw);return -1;
}
static int drain_decoder(godeck_av *p,int *corrupt,char **error){AVFrame *hw=av_frame_alloc(),*sw=av_frame_alloc();if(!hw||!sw){av_frame_free(&hw);av_frame_free(&sw);return msg(error,"frame allocation failed");}int r;while((r=avcodec_receive_frame(p->codec,hw))>=0){if(hw->format!=AV_PIX_FMT_VAAPI){msg(error,"decoder did not produce a VA-API hardware frame");goto fail;}if((hw->flags&AV_FRAME_FLAG_CORRUPT)||hw->decode_error_flags!=0){if(corrupt)*corrupt=1;av_frame_unref(hw);av_frame_unref(sw);continue;}if(hw->width<=0||hw->height<=0||hw->width>4096||hw->height>4096||(int64_t)hw->width*hw->height*4>MAX_OUTPUT){msg(error,"decoded frame dimensions invalid");goto fail;}if((r=av_hwframe_transfer_data(sw,hw,0))<0){failure(error,"VA frame readback failed",r);goto fail;}p->sws=sws_getCachedContext(p->sws,sw->width,sw->height,sw->format,hw->width,hw->height,AV_PIX_FMT_RGBA,SWS_BILINEAR,NULL,NULL,NULL);if(!p->sws){msg(error,"RGBA converter initialization failed");goto fail;}godeck_av_output o={0};o.width=hw->width;o.height=hw->height;o.stride=o.width*4;o.size=o.stride*o.height;o.pts=hw->pts;o.data=malloc(o.size);if(!o.data){msg(error,"RGBA allocation failed");goto fail;}uint8_t *dst[4]={o.data,NULL,NULL,NULL};int strides[4]={o.stride,0,0,0};if(sws_scale(p->sws,(const uint8_t *const*)sw->data,sw->linesize,0,o.height,dst,strides)!=o.height){godeck_av_output_free(&o);msg(error,"RGBA conversion failed");goto fail;}if(append(p,&o,error)<0)goto fail;av_frame_unref(hw);av_frame_unref(sw);}av_frame_free(&hw);av_frame_free(&sw);if(r!=AVERROR(EAGAIN)&&r!=AVERROR_EOF)return failure(error,"hardware decoding failed",r);return 0;
 fail:av_frame_free(&hw);av_frame_free(&sw);return -1;}
int godeck_av_decode(godeck_av *p,const uint8_t *data,size_t len,int64_t pts,godeck_av_output *out,char **error){if(!p||p->encode||!data||!len||len>MAX_OUTPUT)return msg(error,"invalid H.264 access unit");AVPacket *pkt=av_packet_alloc();if(!pkt)return msg(error,"packet allocation failed");int r=av_new_packet(pkt,(int)len);if(r<0){av_packet_free(&pkt);return failure(error,"packet allocation failed",r);}memcpy(pkt->data,data,len);pkt->pts=pts;pkt->dts=pts;int corrupt=0;r=avcodec_send_packet(p->codec,pkt);if(r==AVERROR(EAGAIN)){if(drain_decoder(p,&corrupt,error)<0){av_packet_free(&pkt);return -1;}r=avcodec_send_packet(p->codec,pkt);}av_packet_free(&pkt);if(r==AVERROR_INVALIDDATA){avcodec_flush_buffers(p->codec);return -2;}if(r<0)return failure(error,"sending H.264 access unit failed",r);if(drain_decoder(p,&corrupt,error)<0)return -1;if(take(p,out))return 1;if(corrupt)return -2;return 0;}

