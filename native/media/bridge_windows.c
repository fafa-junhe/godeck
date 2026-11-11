//go:build windows && cgo && amd64

#include "bridge.h"
#include <gst/gst.h>
#include <gst/app/gstappsink.h>
#include <gst/app/gstappsrc.h>
#include <gst/video/video.h>
#include <gst/video/video-event.h>
#include <stdlib.h>
#include <string.h>

struct godeck_media { GstElement *pipeline; GstElement *sink; GstElement *source; };

static godeck_media *start(const char *desc, char **error) {
	gst_init(NULL, NULL);
	GError *err = NULL;
	GstElement *pipe = gst_parse_launch(desc, &err);
	if (!pipe || err) {
		if (error) *error = strdup(err ? err->message : "无法创建 GStreamer 管线");
		if (err) g_error_free(err);
		if (pipe) gst_object_unref(pipe);
		return NULL;
	}
	godeck_media *m = calloc(1, sizeof(*m));
	if (!m) {
		gst_object_unref(pipe);
		return NULL;
	}
	m->pipeline = pipe;
	m->sink = gst_bin_get_by_name(GST_BIN(pipe), "output");
	m->source = gst_bin_get_by_name(GST_BIN(pipe), "input");
	if (!m->sink || gst_element_set_state(pipe, GST_STATE_PLAYING) == GST_STATE_CHANGE_FAILURE) {
		if (error) *error = strdup("无法启动媒体管线，请检查 GStreamer 插件和设备权限");
		godeck_stop(m);
		return NULL;
	}
	return m;
}

static godeck_media *try_encoder_pipeline(const char *capture_conv, const char *encoder, int width, int height, int fps, char **error) {
	char *desc = g_strdup_printf(
		"d3d11screencapturesrc monitor-index=-1 show-cursor=true ! %s ! "
		"video/x-raw,format=NV12,width=%d,height=%d,framerate=%d/1 ! "
		"queue max-size-buffers=2 leaky=downstream ! %s name=encoder ! "
		"h264parse config-interval=-1 ! "
		"video/x-h264,profile=constrained-baseline,stream-format=byte-stream,alignment=au ! "
		"appsink name=output sync=false max-buffers=2 drop=false",
		capture_conv, width, height, fps, encoder);
	godeck_media *m = start(desc, error);
	g_free(desc);
	return m;
}

godeck_media *godeck_encoder_with_profile(int width, int height, int fps, int level, int mode, char **error) {
	if (width <= 0) width = 1280;
	if (height <= 0) height = 720;
	if (fps <= 0) fps = 30;
	width &= ~1;
	height &= ~1;
	if (width < 2) width = 2;
	if (height < 2) height = 2;

	int bitrate = (width * height > 1280 * 720 || level >= 40) ? 4000 : 2500;
	const char *gpu_conv = "d3d11convert ! d3d11download ! videorate drop-only=true";
	const char *cpu_conv = "videoconvert ! videoscale add-borders=false ! videorate drop-only=true";

	char *enc = NULL;
	if (mode == 1) {
		enc = g_strdup_printf("mfh264enc gop-size=60 low-latency=true bframes=0 cabac=false threads=1 bitrate=%d", bitrate);
	} else {
		enc = g_strdup_printf("x264enc tune=zerolatency speed-preset=ultrafast key-int-max=60 bframes=0 sliced-threads=false threads=2 cabac=false bitrate=%d vbv-buf-capacity=300 option-string=\"ref=1\"", bitrate);
	}

	godeck_media *m = try_encoder_pipeline(gpu_conv, enc, width, height, fps, error);
	if (!m) {
		if (error && *error) { free(*error); *error = NULL; }
		m = try_encoder_pipeline(cpu_conv, enc, width, height, fps, error);
	}
	g_free(enc);

	// Fallback if a specific Windows MFT driver rejects optional threads/cabac properties.
	if (!m && mode == 1) {
		if (error && *error) { free(*error); *error = NULL; }
		enc = g_strdup_printf("mfh264enc gop-size=60 low-latency=true bframes=0 bitrate=%d", bitrate);
		m = try_encoder_pipeline(gpu_conv, enc, width, height, fps, error);
		if (!m) {
			if (error && *error) { free(*error); *error = NULL; }
			m = try_encoder_pipeline(cpu_conv, enc, width, height, fps, error);
		}
		g_free(enc);
	}
	return m;
}

godeck_media *godeck_encoder(int fd, unsigned int node, int mode, char **error) {
	(void)fd;
	(void)node;
	return godeck_encoder_with_profile(1280, 720, 30, 31, mode, error);
}

godeck_media *godeck_raw_capture(int fd, unsigned int node, int width, int height, char **error) {
	(void)fd;
	(void)node;
	(void)width;
	(void)height;
	if (error) *error = strdup("Windows 不支持直接原始 PipeWire 采集");
	return NULL;
}

godeck_media *godeck_decoder(int mode, char **error) {
	const char *decoder = (mode == 1) ? "d3d11h264dec" : "openh264dec";
	char *desc = g_strdup_printf(
		"appsrc name=input is-live=true block=false max-bytes=8388608 format=time do-timestamp=false caps=video/x-h264,stream-format=byte-stream,alignment=au ! "
		"h264parse ! %s name=decoder ! videoconvert ! video/x-raw,format=RGBA ! appsink name=output sync=false max-buffers=1 drop=true",
		decoder);
	godeck_media *m = start(desc, error);
	g_free(desc);
	if (!m && mode == 1) {
		// D3D11 硬件解码不可用时，自动回退到轻量级 OpenH264 软件解码
		if (error && *error) {
			free(*error);
			*error = NULL;
		}
		desc = g_strdup_printf(
			"appsrc name=input is-live=true block=false max-bytes=8388608 format=time do-timestamp=false caps=video/x-h264,stream-format=byte-stream,alignment=au ! "
			"h264parse ! openh264dec name=decoder ! videoconvert ! video/x-raw,format=RGBA ! appsink name=output sync=false max-buffers=1 drop=true");
		m = start(desc, error);
		g_free(desc);
	}
	return m;
}

int godeck_pull(godeck_media *m, unsigned char **data, size_t *length, uint64_t *pts, int *w, int *h) {
	if (!m || !m->sink) return -1;
	GstSample *sample = gst_app_sink_try_pull_sample(GST_APP_SINK(m->sink), 100 * GST_MSECOND);
	if (!sample) {
		GstBus *bus = gst_element_get_bus(m->pipeline);
		GstMessage *msg = gst_bus_pop_filtered(bus, GST_MESSAGE_ERROR | GST_MESSAGE_EOS);
		gst_object_unref(bus);
		if (msg) {
			gst_message_unref(msg);
			return -1;
		}
		return gst_app_sink_is_eos(GST_APP_SINK(m->sink)) ? -1 : 0;
	}
	GstBuffer *buffer = gst_sample_get_buffer(sample);
	GstMapInfo map;
	if (!buffer || !gst_buffer_map(buffer, &map, GST_MAP_READ)) {
		gst_sample_unref(sample);
		return -1;
	}
	*data = malloc(map.size);
	if (!*data) {
		gst_buffer_unmap(buffer, &map);
		gst_sample_unref(sample);
		return -1;
	}
	memcpy(*data, map.data, map.size);
	*length = map.size;
	*pts = GST_BUFFER_PTS_IS_VALID(buffer) ? GST_BUFFER_PTS(buffer) : 0;
	*w = *h = 0;
	GstCaps *caps = gst_sample_get_caps(sample);
	if (caps && gst_caps_get_size(caps) > 0) {
		GstStructure *s = gst_caps_get_structure(caps, 0);
		gst_structure_get_int(s, "width", w);
		gst_structure_get_int(s, "height", h);
	}
	if (*w == 0 || *h == 0) {
		GstVideoInfo info;
		if (caps && gst_video_info_from_caps(&info, caps)) {
			*w = GST_VIDEO_INFO_WIDTH(&info);
			*h = GST_VIDEO_INFO_HEIGHT(&info);
		}
	}
	gst_buffer_unmap(buffer, &map);
	gst_sample_unref(sample);
	return 1;
}

int godeck_push(godeck_media *m, const unsigned char *data, size_t len, uint64_t pts) {
	if (!m || !m->source) return -1;
	GstBuffer *b = gst_buffer_new_allocate(NULL, len, NULL);
	if (!b) return -1;
	gst_buffer_fill(b, 0, data, len);
	GST_BUFFER_PTS(b) = pts;
	return gst_app_src_push_buffer(GST_APP_SRC(m->source), b) == GST_FLOW_OK ? 0 : -1;
}

char *godeck_device_path(godeck_media *m) {
	(void)m;
	return NULL;
}

void godeck_interrupt(godeck_media *m) {
	if (m && m->pipeline) gst_element_set_state(m->pipeline, GST_STATE_NULL);
}

void godeck_force_key(godeck_media *m) {
	if (!m || !m->pipeline) return;
	GstElement *enc = gst_bin_get_by_name(GST_BIN(m->pipeline), "encoder");
	if (enc) {
		GstEvent *event = gst_video_event_new_upstream_force_key_unit(GST_CLOCK_TIME_NONE, TRUE, 0);
		gst_element_send_event(enc, event);
		gst_object_unref(enc);
	}
	if (m->sink) {
		GstEvent *event = gst_video_event_new_upstream_force_key_unit(GST_CLOCK_TIME_NONE, TRUE, 0);
		gst_element_send_event(m->sink, event);
	}
}

void godeck_stop(godeck_media *m) {
	if (!m) return;
	if (m->pipeline) gst_element_set_state(m->pipeline, GST_STATE_NULL);
	if (m->sink) gst_object_unref(m->sink);
	if (m->source) gst_object_unref(m->source);
	if (m->pipeline) gst_object_unref(m->pipeline);
	free(m);
}

void godeck_free_error(char *p) {
	free(p);
}

void godeck_free_buffer(void *p) {
	free(p);
}
