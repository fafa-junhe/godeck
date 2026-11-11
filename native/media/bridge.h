#ifndef GODECK_MEDIA_H
#define GODECK_MEDIA_H
#include <stddef.h>
#include <stdint.h>
typedef struct godeck_media godeck_media;
godeck_media *godeck_raw_capture(int fd, unsigned int node, int width, int height, char **error);
godeck_media *godeck_encoder(int fd, unsigned int node, int mode, char **error);
godeck_media *godeck_encoder_with_profile(int width, int height, int fps, int level, int mode, char **error);
godeck_media *godeck_decoder(int mode, char **error);
int godeck_pull(godeck_media *, unsigned char **, size_t *, uint64_t *, int *, int *);
int godeck_push(godeck_media *, const unsigned char *, size_t, uint64_t);
char *godeck_device_path(godeck_media *);
void godeck_force_key(godeck_media *);
void godeck_interrupt(godeck_media *);
void godeck_stop(godeck_media *);
void godeck_free_error(char *);
void godeck_free_buffer(void *);
#endif
