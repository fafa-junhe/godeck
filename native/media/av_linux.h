#ifndef GODECK_AV_LINUX_H
#define GODECK_AV_LINUX_H
#include <stddef.h>
#include <stdint.h>
typedef struct godeck_av godeck_av;
typedef struct godeck_av_output { uint8_t *data; int size, width, height, stride, key; int64_t pts; } godeck_av_output;
godeck_av *godeck_av_encoder(int w,int h,int fps,int level,const char *device,char **error);
godeck_av *godeck_av_decoder(const char *device,char **error);
int godeck_av_encode(godeck_av *,const uint8_t *,size_t,int64_t,int,godeck_av_output *,char **);
int godeck_av_decode(godeck_av *,const uint8_t *,size_t,int64_t,godeck_av_output *,char **);
void godeck_av_output_free(godeck_av_output *);
void godeck_av_close(godeck_av *);
void godeck_av_error_free(char *);
#endif

