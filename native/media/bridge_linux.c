//go:build linux && cgo

#include "bridge.h"
#include <gst/gst.h>
#include <gst/app/gstappsink.h>
#include <gst/app/gstappsrc.h>
#include <gst/video/video.h>
#include <gst/video/video-event.h>
#include <stdlib.h>
#include <string.h>

struct godeck_media { GstElement *pipeline; GstElement *sink; GstElement *source; int raw; };
static godeck_media *start(const char *desc, char **error) {
 gst_init(NULL,NULL);GError *err=NULL;GstElement *pipe=gst_parse_launch(desc,&err);
 if (!pipe || err) {if(error)*error=strdup(err?err->message:"无法创建 GStreamer 管线");if(err)g_error_free(err);if(pipe)gst_object_unref(pipe);return NULL;}
 godeck_media *m=calloc(1,sizeof(*m));if(!m){gst_object_unref(pipe);return NULL;};m->pipeline=pipe;m->sink=gst_bin_get_by_name(GST_BIN(pipe),"output");m->source=gst_bin_get_by_name(GST_BIN(pipe),"input");
 if(!m->sink||gst_element_set_state(pipe,GST_STATE_PLAYING)==GST_STATE_CHANGE_FAILURE){if(error)*error=strdup("无法启动媒体管线，请检查 GStreamer 插件和设备权限");godeck_stop(m);return NULL;}return m;
}
godeck_media *godeck_raw_capture(int fd,unsigned int node,int width,int height,char **error){
 char *desc=g_strdup_printf("pipewiresrc fd=%d path=%u ! queue max-size-buffers=2 leaky=downstream ! videoconvert ! videoscale add-borders=false ! videorate drop-only=true ! video/x-raw,format=NV12,width=%d,height=%d,framerate=30/1 ! appsink name=output sync=false max-buffers=1 drop=true",fd,node,width,height);godeck_media *m=start(desc,error);g_free(desc);if(m)m->raw=1;return m;
}
godeck_media *godeck_encoder(int fd,unsigned int node,int mode,char **error){
 const char *encoder=mode==1?"vah264enc b-frames=0 ref-frames=1 key-int-max=30 dct8x8=false cabac=false":mode==2?"vaapih264enc":"x264enc tune=zerolatency speed-preset=ultrafast key-int-max=30 bframes=0 sliced-threads=false";
 char *desc=g_strdup_printf("pipewiresrc fd=%d path=%u ! queue max-size-buffers=2 leaky=downstream ! videoconvert ! video/x-raw,format=NV12 ! %s name=encoder ! h264parse config-interval=-1 ! video/x-h264,profile=constrained-baseline,stream-format=byte-stream,alignment=au ! appsink name=output sync=false max-buffers=2 drop=false",fd,node,encoder);godeck_media *m=start(desc,error);g_free(desc);return m;
}
godeck_media *godeck_encoder_with_profile(int width, int height, int fps, int level, int mode, char **error){
 (void)width; (void)height; (void)fps; (void)level;
 return godeck_encoder(0, 0, mode, error);
}
godeck_media *godeck_decoder(int mode,char **error){
 const char *decoder=mode==1?"vah264dec":mode==2?"vaapih264dec":"avdec_h264";
 char *desc=g_strdup_printf("appsrc name=input is-live=true block=false max-bytes=8388608 format=time do-timestamp=false caps=video/x-h264,stream-format=byte-stream,alignment=au ! h264parse ! %s name=decoder ! videoconvert ! video/x-raw,format=RGBA ! appsink name=output sync=false max-buffers=1 drop=true",decoder);godeck_media *m=start(desc,error);g_free(desc);return m;
}
int godeck_pull(godeck_media *m,unsigned char **data,size_t *length,uint64_t *pts,int *w,int *h){if(!m||!m->sink)return -1;GstSample *sample=gst_app_sink_try_pull_sample(GST_APP_SINK(m->sink),100*GST_MSECOND);if(!sample)return gst_app_sink_is_eos(GST_APP_SINK(m->sink))?-1:0;GstBuffer *buffer=gst_sample_get_buffer(sample);if(m->raw){GstCaps *caps=gst_sample_get_caps(sample);GstVideoInfo info;GstVideoFrame frame;if(!buffer||!caps||!gst_video_info_from_caps(&info,caps)||GST_VIDEO_INFO_FORMAT(&info)!=GST_VIDEO_FORMAT_NV12){gst_sample_unref(sample);return -1;}int width=GST_VIDEO_INFO_WIDTH(&info),height=GST_VIDEO_INFO_HEIGHT(&info);if(width<=0||height<=0||(width&1)||(height&1)||width>16384||height>16384||!gst_video_frame_map(&frame,&info,buffer,GST_MAP_READ)){gst_sample_unref(sample);return -1;}size_t total=(size_t)width*height*3/2;unsigned char *out=malloc(total);if(!out){gst_video_frame_unmap(&frame);gst_sample_unref(sample);return -1;}for(int row=0;row<height;row++)memcpy(out+(size_t)row*width,(unsigned char*)GST_VIDEO_FRAME_PLANE_DATA(&frame,0)+(ptrdiff_t)row*GST_VIDEO_FRAME_PLANE_STRIDE(&frame,0),width);for(int row=0;row<height/2;row++)memcpy(out+(size_t)width*height+(size_t)row*width,(unsigned char*)GST_VIDEO_FRAME_PLANE_DATA(&frame,1)+(ptrdiff_t)row*GST_VIDEO_FRAME_PLANE_STRIDE(&frame,1),width);*data=out;*length=total;*pts=GST_BUFFER_PTS_IS_VALID(buffer)?GST_BUFFER_PTS(buffer):0;*w=width;*h=height;gst_video_frame_unmap(&frame);gst_sample_unref(sample);return 1;}GstMapInfo map;if(!buffer||!gst_buffer_map(buffer,&map,GST_MAP_READ)){gst_sample_unref(sample);return -1;}*data=malloc(map.size);if(!*data){gst_buffer_unmap(buffer,&map);gst_sample_unref(sample);return -1;}memcpy(*data,map.data,map.size);*length=map.size;*pts=GST_BUFFER_PTS_IS_VALID(buffer)?GST_BUFFER_PTS(buffer):0;*w=*h=0;GstCaps *caps=gst_sample_get_caps(sample);GstVideoInfo info;if(caps&&gst_video_info_from_caps(&info,caps)){*w=GST_VIDEO_INFO_WIDTH(&info);*h=GST_VIDEO_INFO_HEIGHT(&info);}gst_buffer_unmap(buffer,&map);gst_sample_unref(sample);return 1;}
int godeck_push(godeck_media *m,const unsigned char *data,size_t len,uint64_t pts){if(!m||!m->source)return -1;GstBuffer *b=gst_buffer_new_allocate(NULL,len,NULL);if(!b)return -1;gst_buffer_fill(b,0,data,len);GST_BUFFER_PTS(b)=pts;return gst_app_src_push_buffer(GST_APP_SRC(m->source),b)==GST_FLOW_OK?0:-1;}
char *godeck_device_path(godeck_media *m){if(!m||!m->pipeline)return NULL;GstElement *element=gst_bin_get_by_name(GST_BIN(m->pipeline),"encoder");if(!element)element=gst_bin_get_by_name(GST_BIN(m->pipeline),"decoder");if(!element)return NULL;char *path=NULL;if(g_object_class_find_property(G_OBJECT_GET_CLASS(element),"device-path"))g_object_get(element,"device-path",&path,NULL);gst_object_unref(element);if(!path)return NULL;char *result=strdup(path);g_free(path);return result;}
void godeck_interrupt(godeck_media *m){if(m&&m->pipeline)gst_element_set_state(m->pipeline,GST_STATE_NULL);}
void godeck_force_key(godeck_media *m){if(!m||!m->pipeline)return;GstElement *enc=gst_bin_get_by_name(GST_BIN(m->pipeline),"encoder");if(enc){GstEvent *event=gst_video_event_new_upstream_force_key_unit(GST_CLOCK_TIME_NONE,TRUE,0);gst_element_send_event(enc,event);gst_object_unref(enc);}if(m->sink){GstEvent *event=gst_video_event_new_upstream_force_key_unit(GST_CLOCK_TIME_NONE,TRUE,0);gst_element_send_event(m->sink,event);}}
void godeck_stop(godeck_media *m){if(!m)return;if(m->pipeline)gst_element_set_state(m->pipeline,GST_STATE_NULL);if(m->sink)gst_object_unref(m->sink);if(m->source)gst_object_unref(m->source);if(m->pipeline)gst_object_unref(m->pipeline);free(m);}
void godeck_free_error(char *p){free(p);}void godeck_free_buffer(void *p){free(p);}
