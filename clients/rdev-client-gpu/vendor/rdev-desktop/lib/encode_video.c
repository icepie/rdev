#include <stdint.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>
#include <errno.h>

#include <libavcodec/avcodec.h>
#include <libavfilter/buffersink.h>
#include <libavfilter/buffersrc.h>
#include <libavformat/avformat.h>
#include <libavformat/avio.h>
#include <libavutil/buffer.h>
#include <libavutil/common.h>
#include <libavutil/dict.h>
#include <libavutil/error.h>
#include <libavutil/frame.h>
#include <libavutil/hwcontext.h>
#include <libavutil/hwcontext_drm.h>
#include <libavutil/imgutils.h>
#include <libavutil/mem.h>
#include <libavutil/opt.h>
#include <libavutil/pixdesc.h>
#include <libavutil/pixfmt.h>
#include <libavutil/version.h>

#include "error.h"
#include "log.h"

#ifdef HAS_VAAPI
#include <libavutil/hwcontext_vaapi.h>
#include <va/va.h>
#endif

const AVRational TIME_BASE = (AVRational){1, 1000};

typedef struct ScaleContext
{
	AVFilterGraph* filter_graph_scale;
	AVFilterContext* buffersink_scale_ctx;
	AVFilterContext* buffersrc_scale_ctx;
	AVFrame* frame_in;
	AVFrame* frame_out;
} ScaleContext;

typedef struct Scalers
{
	ScaleContext bgr0;
	ScaleContext rgb0;
	ScaleContext rgb;
	ScaleContext drm_prime;
	AVBufferRef* hw_frames_ctx;
	AVFrame* frame_out;
	enum AVPixelFormat pix_fmt_out;
	enum AVPixelFormat pix_fmt_sw_out;
	enum AVPixelFormat drm_prime_sw_format;
} Scalers;

typedef struct VideoContext
{
	AVFormatContext* oc;
	AVCodecContext* c;

	// pointer to the frame to be encoded, one of frame_out in scalers.bgr0/rgb0/rgb
	AVFrame* frame;

	Scalers scalers;

	AVBufferRef* hw_device_ctx;

	AVPacket* pkt;
	AVStream* st;
	int width_out;
	int height_out;
	int width_in;
	int height_in;
	void* buf;
	void* rust_ctx;
	int pts;
	int initialized;
	int frame_allocated;
	int try_vaapi;
	int try_nvenc;
	int try_vulkan_video;
	int try_videotoolbox;
	int try_mediafoundation;
	int using_drm_prime;
	char codec_name[64];
} VideoContext;

// this is a rust function and lives in src/video.rs
int write_video_packet(void* rust_ctx, const uint8_t* buf, int buf_size);

#if defined(__clang__) || defined(__GNUC__)
void log_callback(__attribute__((unused)) void* _ptr, int level, const char* fmt_orig, va_list args)
#else
void log_callback(void* _ptr, int level, const char* fmt_orig, va_list args)
#endif
{
	char fmt[256] = {0};
	strncpy(fmt, fmt_orig, sizeof(fmt) - 1);
	int done = 0;
	// strip whitespaces from end
	for (int i = sizeof(fmt) - 1; i >= 0 && !done; --i)
		switch (fmt[i])
		{
		case ' ':
		case '\n':
		case '\t':
		case '\r':
			fmt[i] = '\0';
			break;
		case '\0':
			break;
		default:
			done = 1;
		}
	char buf[2048];
	vsnprintf(buf, sizeof(buf), fmt, args);
	switch (level)
	{
	case AV_LOG_FATAL:
	case AV_LOG_ERROR:
	case AV_LOG_PANIC:
		log_error("%s", buf);
		break;
	case AV_LOG_INFO:
		log_info("%s", buf);
		break;
	case AV_LOG_WARNING:
		log_warn("%s", buf);
		break;
	case AV_LOG_QUIET:
		break;
	case AV_LOG_VERBOSE:
		log_debug("%s", buf);
		break;
	case AV_LOG_DEBUG:
		log_trace("%s", buf);
		break;
	}
}

// called in src/log.rs
void init_ffmpeg_logger() { av_log_set_callback(log_callback); }

void set_codec_params(VideoContext* ctx)
{
	/* resolution must be a multiple of two */
	ctx->c->width = ctx->width_out;
	ctx->c->height = ctx->height_out;
	ctx->c->time_base = TIME_BASE;
	ctx->c->framerate = (AVRational){0, 1};

	ctx->c->gop_size = 12;
	// no B-frames to reduce latency
	ctx->c->max_b_frames = 0;
	if (ctx->oc->oformat->flags & AVFMT_GLOBALHEADER)
		ctx->c->flags |= AV_CODEC_FLAG_GLOBAL_HEADER;
}

void destroy_scale_ctx(ScaleContext* ctx)
{
	avfilter_graph_free(&ctx->filter_graph_scale);
	if (ctx->frame_in)
		av_frame_free(&ctx->frame_in);
}

void init_scaler(
	ScaleContext* ctx,
	int width_in,
	int height_in,
	int width_out,
	int height_out,
	enum AVPixelFormat pix_fmt_in,
	enum AVPixelFormat pix_fmt_out,
	AVBufferRef* hw_device_ctx,
	AVBufferRef* input_hw_frames_ctx,
	enum AVPixelFormat pix_fmt_sw_out,
	AVFrame* frame_out,
	Error* err)
{
	int ret = 0;

	ctx->frame_in = av_frame_alloc();
	if (!ctx->frame_in)
		ERROR(err, 1, "Failed to allocate frame_in for scale filter!");

	ctx->frame_out = frame_out;

	ctx->frame_in->format = pix_fmt_in;
	ctx->frame_in->width = width_in;
	ctx->frame_in->height = height_in;
	if (pix_fmt_in != AV_PIX_FMT_DRM_PRIME)
	{
		ret = av_frame_get_buffer(ctx->frame_in, 0);
		if (ret)
		{
			destroy_scale_ctx(ctx);
			ERROR(
				err,
				1,
				"Failed to allocate buffer for frame_in for scale filter: %s!",
				av_err2str(ret));
		}
	}

	char args[512];
	const AVFilter* buffersrc = avfilter_get_by_name("buffer");
	const AVFilter* buffersink = avfilter_get_by_name("buffersink");
	AVFilterInOut* outputs = avfilter_inout_alloc();
	AVFilterInOut* inputs = avfilter_inout_alloc();

	ctx->filter_graph_scale = avfilter_graph_alloc();
	if (!outputs || !inputs || !ctx->filter_graph_scale)
	{
		ret = AVERROR(ENOMEM);
		goto end;
	}

	if (pix_fmt_out != AV_PIX_FMT_VAAPI)
		avfilter_graph_set_auto_convert(ctx->filter_graph_scale, AVFILTER_AUTO_CONVERT_NONE);

	/* buffer video source: the decoded frames from the decoder will be inserted here. */
	snprintf(
		args,
		sizeof(args),
		"video_size=%dx%d:pix_fmt=%d:time_base=%d/%d:pixel_aspect=%d/%d",
		width_in,
		height_in,
		pix_fmt_in,
		TIME_BASE.num,
		TIME_BASE.den,
		1,
		1);

	if (input_hw_frames_ctx != NULL)
	{
		ctx->buffersrc_scale_ctx =
			avfilter_graph_alloc_filter(ctx->filter_graph_scale, buffersrc, "in");
		if (!ctx->buffersrc_scale_ctx)
		{
			ret = AVERROR(ENOMEM);
			log_warn("Cannot allocate buffer source");
			goto end;
		}
		if (hw_device_ctx != NULL)
		{
			ctx->buffersrc_scale_ctx->hw_device_ctx = av_buffer_ref(hw_device_ctx);
		}
		AVBufferSrcParameters* params = av_buffersrc_parameters_alloc();
		if (!params)
		{
			ret = AVERROR(ENOMEM);
			goto end;
		}
		params->format = pix_fmt_in;
		params->time_base = TIME_BASE;
		params->width = width_in;
		params->height = height_in;
		params->sample_aspect_ratio = (AVRational){1, 1};
		params->hw_frames_ctx = input_hw_frames_ctx;
		ret = av_buffersrc_parameters_set(ctx->buffersrc_scale_ctx, params);
		av_free(params);
		if (ret < 0)
		{
			log_warn("Cannot set buffer source parameters: %s", av_err2str(ret));
			goto end;
		}
		ret = avfilter_init_str(ctx->buffersrc_scale_ctx, NULL);
		if (ret < 0)
		{
			log_warn("Cannot init buffer source: %s", av_err2str(ret));
			goto end;
		}
	}
	else
	{
		ret = avfilter_graph_create_filter(
			&ctx->buffersrc_scale_ctx, buffersrc, "in", args, NULL, ctx->filter_graph_scale);
		if (ret < 0)
		{
			log_warn("Cannot create buffer source");
			goto end;
		}

		if (hw_device_ctx != NULL)
		{
			ctx->buffersrc_scale_ctx->hw_device_ctx = av_buffer_ref(hw_device_ctx);
		}
	}

	/* buffer video sink: to terminate the filter chain. */
	ctx->buffersink_scale_ctx =
		avfilter_graph_alloc_filter(ctx->filter_graph_scale, buffersink, "out");

	if (ctx->buffersink_scale_ctx == NULL)
	{
		log_warn("Cannot allocate buffer sink");
		goto end;
	}

#if LIBAVUTIL_VERSION_MAJOR >= 59
	ret = av_opt_set_array(
		ctx->buffersink_scale_ctx,
		"pixel_formats",
		AV_OPT_SEARCH_CHILDREN,
		0,
		1,
		AV_OPT_TYPE_PIXEL_FMT,
		&pix_fmt_out);
#else
	enum AVPixelFormat pix_fmts[] = { pix_fmt_out, AV_PIX_FMT_NONE };
	ret = av_opt_set_int_list(
		ctx->buffersink_scale_ctx,
		"pix_fmts",
		pix_fmts,
		AV_PIX_FMT_NONE,
		AV_OPT_SEARCH_CHILDREN);
#endif
	if (ret < 0)
	{
		log_warn("Cannot set output pixel format: %s", av_err2str(ret));
		goto end;
	}

	ret = avfilter_init_dict(ctx->buffersink_scale_ctx, NULL);
	if (ret < 0)
	{
		log_warn("Cannot init buffer sink");
		goto end;
	}

	outputs->name = av_strdup("in");
	outputs->filter_ctx = ctx->buffersrc_scale_ctx;
	outputs->pad_idx = 0;
	outputs->next = NULL;

	inputs->name = av_strdup("out");
	inputs->filter_ctx = ctx->buffersink_scale_ctx;
	inputs->pad_idx = 0;
	inputs->next = NULL;

	switch (pix_fmt_out)
	{
	case AV_PIX_FMT_CUDA:
#ifdef HAS_LIBNPP
		snprintf(
			args,
			sizeof(args),
			"scale,format=nv12,hwupload_cuda,scale_npp=w=%d:h=%d:format=%s:interp_algo=nn",
			width_out,
			height_out,
			av_get_pix_fmt_name(pix_fmt_sw_out));
#else
		// Keep the default NVENC path independent from scale_cuda. Some static
		// builds include h264_nvenc and hwupload_cuda but not the CUDA scaler.
		snprintf(
			args,
			sizeof(args),
			"scale=w=%d:h=%d:flags=fast_bilinear,format=%s,hwupload_cuda",
			width_out,
			height_out,
			av_get_pix_fmt_name(pix_fmt_sw_out));
#endif
		break;
#ifdef HAS_VAAPI
	case AV_PIX_FMT_VAAPI:
		if (pix_fmt_in == AV_PIX_FMT_DRM_PRIME)
			snprintf(
				args,
				sizeof(args),
				"hwmap=derive_device=vaapi,scale_vaapi=w=%d:h=%d:format=%s:mode=fast",
				width_out,
				height_out,
				av_get_pix_fmt_name(pix_fmt_sw_out));
		else if (pix_fmt_in == AV_PIX_FMT_RGB24)
			snprintf(
				args,
				sizeof(args),
				"scale=w=%d:h=%d:flags=fast_bilinear,hwupload",
				width_out,
				height_out);
		else
			snprintf(
				args,
				sizeof(args),
				"format=%s,hwupload,scale_vaapi=w=%d:h=%d:format=%s:mode=fast",
				av_get_pix_fmt_name(pix_fmt_sw_out),
				width_out,
				height_out,
				av_get_pix_fmt_name(pix_fmt_sw_out));
		break;
#endif
#ifdef HAS_VULKAN_VIDEO
	case AV_PIX_FMT_VULKAN:
		snprintf(
			args,
			sizeof(args),
			"scale=w=%d:h=%d:flags=fast_bilinear,format=%s,hwupload",
			width_out,
			height_out,
			av_get_pix_fmt_name(pix_fmt_sw_out));
		break;
#endif
	default:
		snprintf(args, sizeof(args), "scale=w=%d:h=%d:flags=fast_bilinear", width_out, height_out);
	}

#if LIBAVFILTER_VERSION_MAJOR >= 9
	if (hw_device_ctx != NULL)
	{
		// Use segment API so we can set hw_device_ctx on each filter before init.
		// avfilter_graph_parse_ptr initializes filters internally, too late for hwupload.
		AVFilterGraphSegment* seg = NULL;
		AVFilterInOut* seg_inputs = NULL;
		AVFilterInOut* seg_outputs = NULL;

		ret = avfilter_graph_segment_parse(ctx->filter_graph_scale, args, 0, &seg);
		if (ret < 0)
		{
			log_warn("Failed to parse filter segment: %s", av_err2str(ret));
			goto end;
		}

		ret = avfilter_graph_segment_create_filters(seg, 0);
		if (ret < 0)
		{
			avfilter_graph_segment_free(&seg);
			log_warn("Failed to create filter contexts: %s", av_err2str(ret));
			goto end;
		}

		for (size_t ci = 0; ci < seg->nb_chains; ci++)
		{
			AVFilterChain* chain = seg->chains[ci];
			for (size_t fi = 0; fi < chain->nb_filters; fi++)
			{
				AVFilterContext* filt = chain->filters[fi]->filter;
				if (filt && !filt->hw_device_ctx)
					filt->hw_device_ctx = av_buffer_ref(hw_device_ctx);
			}
		}

		ret = avfilter_graph_segment_apply_opts(seg, 0);
		if (ret < 0)
		{
			avfilter_graph_segment_free(&seg);
			log_warn("Failed to apply filter opts: %s", av_err2str(ret));
			goto end;
		}

		ret = avfilter_graph_segment_init(seg, 0);
		if (ret < 0)
		{
			avfilter_graph_segment_free(&seg);
			log_warn("Failed to init filters: %s", av_err2str(ret));
			goto end;
		}

		ret = avfilter_graph_segment_link(seg, 0, &seg_inputs, &seg_outputs);
		avfilter_graph_segment_free(&seg);
		if (ret < 0)
		{
			avfilter_inout_free(&seg_inputs);
			avfilter_inout_free(&seg_outputs);
			log_warn("Failed to link filters: %s", av_err2str(ret));
			goto end;
		}

		if (seg_inputs)
		{
			ret = avfilter_link(
				ctx->buffersrc_scale_ctx, 0, seg_inputs->filter_ctx, seg_inputs->pad_idx);
			avfilter_inout_free(&seg_inputs);
			if (ret < 0)
			{
				avfilter_inout_free(&seg_outputs);
				log_warn("Failed to link buffersrc to filter: %s", av_err2str(ret));
				goto end;
			}
		}

		if (seg_outputs)
		{
			ret = avfilter_link(
				seg_outputs->filter_ctx, seg_outputs->pad_idx, ctx->buffersink_scale_ctx, 0);
			avfilter_inout_free(&seg_outputs);
			if (ret < 0)
			{
				log_warn("Failed to link filter to buffersink: %s", av_err2str(ret));
				goto end;
			}
		}
	}
	else
#endif
	{
		if ((ret = avfilter_graph_parse_ptr(
				 ctx->filter_graph_scale, args, &inputs, &outputs, NULL)) < 0)
		{
			log_warn("Failed to parse filter: %s", av_err2str(ret));
			goto end;
		}
	}

	if ((ret = avfilter_graph_config(ctx->filter_graph_scale, NULL)) < 0)
	{
		log_warn("Failed to configure filter graph: %s", av_err2str(ret));
		goto end;
	}

end:
	avfilter_inout_free(&inputs);
	avfilter_inout_free(&outputs);

	if (ret != 0)
	{
		destroy_scale_ctx(ctx);
		ERROR(
			err,
			1,
			"Setting up scale filter %s -> %s (sw: %s) failed!",
			av_get_pix_fmt_name(pix_fmt_in),
			av_get_pix_fmt_name(pix_fmt_out),
			av_get_pix_fmt_name(pix_fmt_sw_out));
	}
	else
	{
		log_debug(
			"Scale filter set %s -> %s (sw: %s) up!",
			av_get_pix_fmt_name(pix_fmt_in),
			av_get_pix_fmt_name(pix_fmt_out),
			av_get_pix_fmt_name(pix_fmt_sw_out));
	}
}

void destroy_scalers(Scalers* s)
{
	destroy_scale_ctx(&s->bgr0);
	destroy_scale_ctx(&s->rgb0);
	destroy_scale_ctx(&s->rgb);
	destroy_scale_ctx(&s->drm_prime);
	if (s->frame_out)
		av_frame_free(&s->frame_out);
}

static int create_drm_device_ctx(AVBufferRef** drm_device_ctx)
{
	const char* env_drm_device = getenv("WEYLUS_DRM_DEVICE");
	const char* env_vaapi_device = getenv("WEYLUS_VAAPI_DEVICE");
	const char* candidates[] = {
		env_drm_device ? env_drm_device : "",
		env_vaapi_device ? env_vaapi_device : "",
		"/dev/dri/renderD128",
		"/dev/dri/renderD129",
		"/dev/dri/card0",
		"/dev/dri/card1",
		NULL,
	};
	int last_ret = AVERROR(EINVAL);

	for (int i = 0; candidates[i]; i++)
	{
		if (!candidates[i] || !candidates[i][0])
			continue;
		int ret = av_hwdevice_ctx_create(drm_device_ctx, AV_HWDEVICE_TYPE_DRM, candidates[i], NULL, 0);
		if (ret == 0)
		{
			log_info("DRM PRIME input using DRM device: %s", candidates[i]);
			return 0;
		}
		last_ret = ret;
		log_warn("DRM PRIME input could not open DRM device %s: %s", candidates[i], av_err2str(ret));
	}

	return last_ret;
}

static enum AVPixelFormat drm_prime_sw_format_from_fourcc(uint32_t fourcc)
{
	switch (fourcc)
	{
	case MKTAG('N', 'V', '1', '2'):
		return AV_PIX_FMT_NV12;
	default:
		return AV_PIX_FMT_NONE;
	}
}

static void init_drm_prime_scaler(
	Scalers* ctx,
	int width_in,
	int height_in,
	int width_out,
	int height_out,
	AVBufferRef* hw_device_ctx,
	enum AVPixelFormat drm_prime_sw_format,
	Error* err)
{
	int ret;
	AVBufferRef* drm_device_ctx = NULL;
	AVBufferRef* drm_frames_ctx = NULL;

	if (ctx->drm_prime.filter_graph_scale && ctx->drm_prime_sw_format == drm_prime_sw_format)
		return;

	destroy_scale_ctx(&ctx->drm_prime);
	memset(&ctx->drm_prime, 0, sizeof(ctx->drm_prime));
	ctx->drm_prime_sw_format = AV_PIX_FMT_NONE;

	ret = create_drm_device_ctx(&drm_device_ctx);
	if (ret < 0)
	{
		ERROR(err, ret, "failed to create DRM device: %s", av_err2str(ret));
	}

	drm_frames_ctx = av_hwframe_ctx_alloc(drm_device_ctx);
	if (!drm_frames_ctx)
	{
		av_buffer_unref(&drm_device_ctx);
		ERROR(err, 1, "failed to allocate DRM frames context");
	}
	AVHWFramesContext* drm_frames = (AVHWFramesContext*)drm_frames_ctx->data;
	drm_frames->format = AV_PIX_FMT_DRM_PRIME;
	drm_frames->sw_format = drm_prime_sw_format;
	drm_frames->width = width_in;
	drm_frames->height = height_in;
	ret = av_hwframe_ctx_init(drm_frames_ctx);
	if (ret < 0)
	{
		av_buffer_unref(&drm_frames_ctx);
		av_buffer_unref(&drm_device_ctx);
		ERROR(err, ret, "failed to initialize DRM frames context: %s", av_err2str(ret));
	}

	init_scaler(
		&ctx->drm_prime,
		width_in,
		height_in,
		width_out,
		height_out,
		AV_PIX_FMT_DRM_PRIME,
		ctx->pix_fmt_out,
		hw_device_ctx,
		drm_frames_ctx,
		ctx->pix_fmt_sw_out,
		ctx->frame_out,
		err);
	av_buffer_unref(&drm_frames_ctx);
	av_buffer_unref(&drm_device_ctx);
	OK_OR_ABORT(err);
	ctx->drm_prime_sw_format = drm_prime_sw_format;
}

void init_scalers(
	Scalers* ctx,
	int width_in,
	int height_in,
	int width_out,
	int height_out,
	enum AVPixelFormat pix_fmt_out,
	enum AVPixelFormat pix_fmt_sw_out,
	AVBufferRef* hw_device_ctx,
	Error* err)
{
	int ret;
	ctx->frame_out = av_frame_alloc();
	ctx->pix_fmt_out = pix_fmt_out;
	ctx->pix_fmt_sw_out = pix_fmt_sw_out;
	ctx->drm_prime_sw_format = AV_PIX_FMT_NONE;
	if (!ctx->frame_out)
	{
		destroy_scalers(ctx);
		ERROR(err, 1, "Failed to allocate frame_out for scale filter!");
	}

	if (hw_device_ctx != NULL)
	{

		AVBufferRef* hw_frames_ref;
		AVHWFramesContext* frames_ctx = NULL;
		if (!(hw_frames_ref = av_hwframe_ctx_alloc(hw_device_ctx)))
		{
			destroy_scalers(ctx);
			ERROR(err, 1, "Failed to create HW frame context.");
		}
		frames_ctx = (AVHWFramesContext*)(hw_frames_ref->data);
		frames_ctx->format = pix_fmt_out;
		frames_ctx->sw_format = pix_fmt_sw_out;
		frames_ctx->width = width_out;
		frames_ctx->height = height_out;
		frames_ctx->initial_pool_size = 20;
		if ((ret = av_hwframe_ctx_init(hw_frames_ref)) < 0)
		{
			av_buffer_unref(&hw_frames_ref);
			destroy_scalers(ctx);
			ERROR(
				err,
				1,
				"Failed to initialize HW frame context."
				"Error code: %s",
				av_err2str(ret));
		}

		ctx->hw_frames_ctx = av_buffer_ref(hw_frames_ref);
		ret = av_hwframe_get_buffer(ctx->hw_frames_ctx, ctx->frame_out, 0);
		if (ret < 0)
		{
			av_buffer_unref(&hw_frames_ref);
			destroy_scalers(ctx);
			ERROR(
				err,
				1,
				"Could not allocate video hardware frame data for scaling: %s",
				av_err2str(ret));
		}
		av_buffer_unref(&hw_frames_ref);
	}

	enum AVPixelFormat pix_fmts[] = {AV_PIX_FMT_BGR0, AV_PIX_FMT_RGB0, AV_PIX_FMT_RGB24};
	ScaleContext* scalers[] = {&ctx->bgr0, &ctx->rgb0, &ctx->rgb};
	for (int i = 0; i < 3; i++)
	{
		init_scaler(
			scalers[i],
			width_in,
			height_in,
			width_out,
			height_out,
			pix_fmts[i],
			pix_fmt_out,
			hw_device_ctx,
			NULL,
			pix_fmt_sw_out,
			ctx->frame_out,
			err);
		OK_OR_ABORT(err);
	}
}

void scale_frame(ScaleContext* ctx, Error* err)
{
	int ret;
	if ((ret = av_buffersrc_add_frame_flags(
				   ctx->buffersrc_scale_ctx, ctx->frame_in, AV_BUFFERSRC_FLAG_KEEP_REF) < 0))
	{
		ERROR(err, ret, "Error adding frame to buffer source: %s.", av_err2str(ret));
	}

	av_frame_unref(ctx->frame_out);

	while (1)
	{
		int ret = av_buffersink_get_frame(ctx->buffersink_scale_ctx, ctx->frame_out);
		if (ret == AVERROR(EAGAIN) || ret == AVERROR_EOF)
			break;
		if (ret < 0)
		{
			ERROR(err, ret, "Error reading frame from buffer sink: %s.", av_err2str(ret));
		}
	}
}

void open_video(VideoContext* ctx, Error* err)
{
	if (ctx->width_out <= 1 || ctx->height_out <= 1)
		ERROR(
			err,
			1,
			"Invalid size for video: width = %d, height = %d",
			ctx->width_out,
			ctx->height_out);

	const AVCodec* codec;
	int ret;

	avformat_alloc_output_context2(&ctx->oc, NULL, "mp4", NULL);
	if (!ctx->oc)
	{
		ERROR(err, 1, "Could not find output format mp4.");
	}

	int using_hw = 0;

#ifdef HAS_VAAPI
	char* vaapi_device = getenv("WEYLUS_VAAPI_DEVICE");

	if (ctx->try_vaapi &&
		av_hwdevice_ctx_create(
			&ctx->hw_device_ctx, AV_HWDEVICE_TYPE_VAAPI, vaapi_device, NULL, 0) == 0)
	{
		log_info(
			"Attempting VAAPI hardware encoding with device: %s",
			vaapi_device ? vaapi_device : "(default)");

		if (ctx->hw_device_ctx)
		{
			AVHWFramesConstraints* cst =
				av_hwdevice_get_hwframe_constraints(ctx->hw_device_ctx, NULL);
			if (cst)
			{
				for (enum AVPixelFormat* fmt = cst->valid_sw_formats; *fmt != AV_PIX_FMT_NONE;
					 ++fmt)
				{
					log_debug("VAAPI: valid pix_fmt: %s", av_get_pix_fmt_name(*fmt));
				}
				av_hwframe_constraints_free(&cst);
			}
		}

		codec = avcodec_find_encoder_by_name("h264_vaapi");
		if (codec)
		{
			ctx->c = avcodec_alloc_context3(codec);
			if (ctx->c)
			{
				Error err = {0};
				init_scalers(
					&ctx->scalers,
					ctx->width_in,
					ctx->height_in,
					ctx->width_out,
					ctx->height_out,
					AV_PIX_FMT_VAAPI,
					AV_PIX_FMT_NV12,
					ctx->hw_device_ctx,
					&err);
				if (err.code)
				{
					log_warn("Failed to initialize scaler: %s", err.error_str);
					avcodec_free_context(&ctx->c);
				}
				else
				{
					ctx->c->pix_fmt = AV_PIX_FMT_VAAPI;
					ctx->c->hw_frames_ctx = ctx->scalers.hw_frames_ctx;
					av_opt_set(ctx->c->priv_data, "quality", "7", 0);
					av_opt_set(ctx->c->priv_data, "qp", "23", 0);
					set_codec_params(ctx);

					ret = avcodec_open2(ctx->c, codec, NULL);
					if (ret == 0)
						using_hw = 1;
					else
					{
						log_warn("Could not open VAAPI codec: %s!", av_err2str(ret));
						avcodec_free_context(&ctx->c);
						av_buffer_unref(&ctx->hw_device_ctx);
						destroy_scalers(&ctx->scalers);
					}
				}
			}
		}
		else
		{
			log_warn("Codec 'h264_vaapi' not found!");
			av_buffer_unref(&ctx->hw_device_ctx);
		}
	}
	else if (ctx->try_vaapi)
	{
		log_warn(
			"Failed to initialise VAAPI connection for device %s: %s",
			vaapi_device ? vaapi_device : "(default)",
			av_err2str(AVERROR_UNKNOWN));
	}
#endif

#ifdef HAS_MEDIAFOUNDATION
	if (ctx->try_mediafoundation && !using_hw)
	{
		codec = avcodec_find_encoder_by_name("h264_mf");
		if (codec)
		{
			ctx->c = avcodec_alloc_context3(codec);
			if (ctx->c)
			{
				Error err = {0};
				init_scalers(
					&ctx->scalers,
					ctx->width_in,
					ctx->height_in,
					ctx->width_out,
					ctx->height_out,
					AV_PIX_FMT_NV12,
					AV_PIX_FMT_NV12,
					NULL,
					&err);
				if (err.code)
				{
					log_warn("Failed to initialize scaler: %s", err.error_str);
					avcodec_free_context(&ctx->c);
				}
				else
				{
					ctx->c->pix_fmt = AV_PIX_FMT_NV12;
					av_opt_set(ctx->c->priv_data, "rate_control", "ld_vbr", 0);
					av_opt_set(ctx->c->priv_data, "scenario", "display_remoting", 0);
					av_opt_set(ctx->c->priv_data, "quality", "100", 0);
					set_codec_params(ctx);
					int ret = avcodec_open2(ctx->c, codec, NULL);
					if (ret == 0)
						using_hw = 1;
					else
					{
						log_debug("Could not open codec: %s!", av_err2str(ret));
						avcodec_free_context(&ctx->c);
						destroy_scalers(&ctx->scalers);
					}
				}
			}
			else
				log_debug("Could not allocate video codec context for 'h264_mf'!");
		}
		else
			log_debug("Codec 'h264_mf' not found!");
	}
#endif

#ifdef HAS_NVENC
	if (ctx->try_nvenc && !using_hw &&
		av_hwdevice_ctx_create(&ctx->hw_device_ctx, AV_HWDEVICE_TYPE_CUDA, NULL, NULL, 0) == 0)
	{
		codec = avcodec_find_encoder_by_name("h264_nvenc");
		if (codec)
		{
			ctx->c = avcodec_alloc_context3(codec);
			if (ctx->c)
			{
				Error err = {0};
				init_scalers(
					&ctx->scalers,
					ctx->width_in,
					ctx->height_in,
					ctx->width_out,
					ctx->height_out,
					AV_PIX_FMT_CUDA,
					AV_PIX_FMT_NV12,
					ctx->hw_device_ctx,
					&err);
				if (err.code)
				{
					log_warn("Failed to initialize scaler: %s", err.error_str);
					avcodec_free_context(&ctx->c);
				}
				else
				{
					ctx->c->pix_fmt = AV_PIX_FMT_CUDA;
					ctx->c->hw_frames_ctx = ctx->scalers.hw_frames_ctx;
					av_opt_set(ctx->c->priv_data, "preset", "p1", 0);
					av_opt_set(ctx->c->priv_data, "zerolatency", "1", 0);
					av_opt_set(ctx->c->priv_data, "tune", "ull", 0);
					av_opt_set(ctx->c->priv_data, "rc", "cbr", 0);
					av_opt_set(ctx->c->priv_data, "cq", "21", 0);
					av_opt_set(ctx->c->priv_data, "delay", "0", 0);
					set_codec_params(ctx);

					int ret = avcodec_open2(ctx->c, codec, NULL);
					if (ret == 0)
						using_hw = 1;
					else
					{
						log_debug("Could not open codec: %s!", av_err2str(ret));
						avcodec_free_context(&ctx->c);
						destroy_scalers(&ctx->scalers);
					}
				}
			}
			else
				log_debug("Could not allocate video codec context for 'h264_nvenc'!");
		}
		else
			log_debug("Codec 'h264_nvenc' not found!");
	}
#endif

#ifdef HAS_VULKAN_VIDEO
	char* vulkan_device = getenv("WEYLUS_VULKAN_DEVICE");

	if (ctx->try_vulkan_video && !using_hw &&
		av_hwdevice_ctx_create(
			&ctx->hw_device_ctx, AV_HWDEVICE_TYPE_VULKAN, vulkan_device, NULL, 0) == 0)
	{
		log_info(
			"Attempting Vulkan Video hardware encoding with device: %s",
			vulkan_device ? vulkan_device : "(default)");

		codec = avcodec_find_encoder_by_name("h264_vulkan");
		if (codec)
		{
			ctx->c = avcodec_alloc_context3(codec);
			if (ctx->c)
			{
				Error err = {0};
				init_scalers(
					&ctx->scalers,
					ctx->width_in,
					ctx->height_in,
					ctx->width_out,
					ctx->height_out,
					AV_PIX_FMT_VULKAN,
					AV_PIX_FMT_NV12,
					ctx->hw_device_ctx,
					&err);
				if (err.code)
				{
					log_warn("Failed to initialize Vulkan Video scaler: %s", err.error_str);
					avcodec_free_context(&ctx->c);
					av_buffer_unref(&ctx->hw_device_ctx);
					destroy_scalers(&ctx->scalers);
				}
				else
				{
					ctx->c->pix_fmt = AV_PIX_FMT_VULKAN;
					ctx->c->hw_frames_ctx = ctx->scalers.hw_frames_ctx;
					ctx->c->profile = AV_PROFILE_H264_CONSTRAINED_BASELINE;
					av_opt_set(ctx->c->priv_data, "tune", "ull", 0);
					av_opt_set(ctx->c->priv_data, "usage", "stream", 0);
					av_opt_set(ctx->c->priv_data, "content", "desktop", 0);
					av_opt_set(ctx->c->priv_data, "rc_mode", "cqp", 0);
					av_opt_set(ctx->c->priv_data, "qp", "23", 0);
					set_codec_params(ctx);

					ret = avcodec_open2(ctx->c, codec, NULL);
					if (ret == 0)
						using_hw = 1;
					else
					{
						log_warn("Could not open Vulkan Video codec: %s!", av_err2str(ret));
						avcodec_free_context(&ctx->c);
						av_buffer_unref(&ctx->hw_device_ctx);
						destroy_scalers(&ctx->scalers);
					}
				}
			}
			else
			{
				log_debug("Could not allocate video codec context for 'h264_vulkan'!");
				av_buffer_unref(&ctx->hw_device_ctx);
			}
		}
		else
		{
			log_warn("Codec 'h264_vulkan' not found!");
			av_buffer_unref(&ctx->hw_device_ctx);
		}
	}
	else if (ctx->try_vulkan_video && !using_hw)
	{
		log_warn(
			"Failed to initialise Vulkan connection for device %s: %s",
			vulkan_device ? vulkan_device : "(default)",
			av_err2str(AVERROR_UNKNOWN));
	}
#endif

#ifdef HAS_VIDEOTOOLBOX
	if (ctx->try_videotoolbox && !using_hw)
	{
		codec = avcodec_find_encoder_by_name("h264_videotoolbox");
		if (codec)
		{
			ctx->c = avcodec_alloc_context3(codec);
			if (ctx->c)
			{
				Error err = {0};
				init_scalers(
					&ctx->scalers,
					ctx->width_in,
					ctx->height_in,
					ctx->width_out,
					ctx->height_out,
					AV_PIX_FMT_YUV420P,
					AV_PIX_FMT_YUV420P,
					ctx->hw_device_ctx,
					&err);
				if (err.code)
				{
					log_warn("Failed to initialize scaler: %s", err.error_str);
					avcodec_free_context(&ctx->c);
				}
				else
				{
					ctx->c->pix_fmt = AV_PIX_FMT_YUV420P;
					av_opt_set(ctx->c->priv_data, "realtime", "true", 0);
					av_opt_set(ctx->c->priv_data, "allow_sw", "true", 0);
					av_opt_set(ctx->c->priv_data, "profile", "extended", 0);
					av_opt_set(ctx->c->priv_data, "level", "5.2", 0);
					set_codec_params(ctx);
					if (avcodec_open2(ctx->c, codec, NULL) == 0)
						using_hw = 1;
					else
					{
						log_debug("Could not open codec: %s!", av_err2str(ret));
						avcodec_free_context(&ctx->c);
						destroy_scalers(&ctx->scalers);
					}
				}
			}
		}
	}
#endif

	if (!using_hw)
	{
		codec = avcodec_find_encoder_by_name("libx264");
		if (!codec)
		{
			ERROR(err, 1, "Codec 'libx264' not found");
		}

		ctx->c = avcodec_alloc_context3(codec);
		if (!ctx->c)
		{
			ERROR(err, 1, "Could not allocate video codec context");
		}

		init_scalers(
			&ctx->scalers,
			ctx->width_in,
			ctx->height_in,
			ctx->width_out,
			ctx->height_out,
			AV_PIX_FMT_YUV420P,
			AV_PIX_FMT_YUV420P,
			NULL,
			err);
		if (err->code)
		{
			avcodec_free_context(&ctx->c);
			return;
		}

		ctx->c->pix_fmt = AV_PIX_FMT_YUV420P;
		av_opt_set(ctx->c->priv_data, "preset", "ultrafast", 0);
		av_opt_set(ctx->c->priv_data, "tune", "zerolatency", 0);
		av_opt_set(ctx->c->priv_data, "crf", "23", 0);
		set_codec_params(ctx);

		ret = avcodec_open2(ctx->c, codec, NULL);
		if (ret < 0)
		{
			avcodec_free_context(&ctx->c);
			ERROR(err, 1, "Could not open codec: %s", av_err2str(ret));
		}
	}

	ctx->st = avformat_new_stream(ctx->oc, NULL);
	avcodec_parameters_from_context(ctx->st->codecpar, ctx->c);

	ctx->pkt = av_packet_alloc();
	if (!ctx->pkt)
		ERROR(err, 1, "Failed to allocate packet");

	int buf_size = 1024 * 1024;
	ctx->buf = av_malloc(buf_size);
	ctx->oc->pb = avio_alloc_context(
		ctx->buf, buf_size, AVIO_FLAG_WRITE, ctx->rust_ctx, NULL, write_video_packet, NULL);
	if (!ctx->oc->pb)
		ERROR(err, 1, "Failed to allocate avio context");

	AVDictionary* opt = NULL;

	// enable writing fragmented mp4
	av_dict_set(&opt, "movflags", "frag_custom+empty_moov+default_base_moof", 0);
	ret = avformat_write_header(ctx->oc, &opt);
	if (ret < 0)
		log_warn("Video: failed to write header!");
	av_dict_free(&opt);

	if (av_pix_fmt_desc_get(ctx->c->pix_fmt)->flags & AV_PIX_FMT_FLAG_HWACCEL &&
		ctx->c->hw_frames_ctx)
	{
		const char* pix_fmt_sw =
			av_get_pix_fmt_name(((AVHWFramesContext*)ctx->c->hw_frames_ctx->data)->sw_format);
		log_info(
			"Video: %dx%d@%s pix_fmt: %s (%s)",
			ctx->width_out,
			ctx->height_out,
			ctx->c->codec->name,
			av_get_pix_fmt_name(ctx->c->pix_fmt),
			pix_fmt_sw);
	}
	else
		log_info(
			"Video: %dx%d@%s pix_fmt: %s",
			ctx->width_out,
			ctx->height_out,
			ctx->c->codec->name,
			av_get_pix_fmt_name(ctx->c->pix_fmt));

	ctx->initialized = 1;
}

void destroy_video_encoder(VideoContext* ctx)
{
	if (ctx->initialized)
	{
		av_write_trailer(ctx->oc);
		avio_context_free(&ctx->oc->pb);
		avformat_free_context(ctx->oc);
		avcodec_free_context(&ctx->c);
		av_packet_free(&ctx->pkt);
		av_free(ctx->buf);
		destroy_scalers(&ctx->scalers);
	}
	if (ctx->hw_device_ctx)
		av_buffer_unref(&ctx->hw_device_ctx);
	free(ctx);
}

const char* video_encoder_codec_name(VideoContext* ctx)
{
	if (!ctx || !ctx->c || !ctx->c->codec || !ctx->c->codec->name)
		return "";
	if (ctx->using_drm_prime)
	{
		snprintf(ctx->codec_name, sizeof(ctx->codec_name), "%s/drm-prime", ctx->c->codec->name);
		return ctx->codec_name;
	}
	return ctx->c->codec->name;
}

int video_encoder_supports_drm_prime(VideoContext* ctx)
{
	return ctx && ctx->c && ctx->c->pix_fmt == AV_PIX_FMT_VAAPI && ctx->hw_device_ctx != NULL;
}

void encode_video_frame(VideoContext* ctx, int millis, Error* err)
{
	int ret;
	AVFrame* frame = ctx->frame;
	if (!frame)
		ERROR(err, 1, "Frame not initialized!");

	frame->pts = millis;

	ret = avcodec_send_frame(ctx->c, frame);
	if (ret < 0)
		ERROR(err, 1, "Error sending a frame for encoding: %s", av_err2str(ret));

	while (ret >= 0)
	{
		ret = avcodec_receive_packet(ctx->c, ctx->pkt);
		if (ret == AVERROR(EAGAIN) || ret == AVERROR_EOF)
			return;
		else if (ret < 0)
		{
			ERROR(err, 1, "Error during encoding");
		}

		av_packet_rescale_ts(ctx->pkt, ctx->c->time_base, ctx->st->time_base);
		av_write_frame(ctx->oc, ctx->pkt);
		av_packet_unref(ctx->pkt);

		// new fragment on every frame for lowest latency
		av_write_frame(ctx->oc, NULL);
	}
}

static void free_drm_prime_desc(void* opaque, uint8_t* data)
{
	(void)opaque;
	AVDRMFrameDescriptor* desc = (AVDRMFrameDescriptor*)data;
	if (!desc)
		return;
	for (int i = 0; i < desc->nb_objects && i < AV_DRM_MAX_PLANES; i++)
	{
		if (desc->objects[i].fd >= 0)
			close(desc->objects[i].fd);
	}
	av_free(desc);
}

VideoContext* init_video_encoder(
	void* rust_ctx,
	int width_in,
	int height_in,
	int width_out,
	int height_out,
	int try_vaapi,
	int try_nvenc,
	int try_vulkan_video,
	int try_videotoolbox,
	int try_mediafoundation)
{
	VideoContext* ctx = malloc(sizeof(VideoContext));
	ctx->rust_ctx = rust_ctx;
	log_info(
		"Video encoder init: try_vaapi=%d try_nvenc=%d try_vulkan_video=%d try_videotoolbox=%d try_mediafoundation=%d",
		try_vaapi,
		try_nvenc,
		try_vulkan_video,
		try_videotoolbox,
		try_mediafoundation);
	ctx->width_out = width_out - width_out % 2;
	ctx->height_out = height_out - height_out % 2;
	ctx->width_in = width_in;
	ctx->height_in = height_in;
	ctx->pts = 0;
	ctx->initialized = 0;
	ctx->frame_allocated = 0;
	ctx->try_vaapi = try_vaapi;
	ctx->try_nvenc = try_nvenc;
	ctx->try_vulkan_video = try_vulkan_video;
	ctx->try_videotoolbox = try_videotoolbox;
	ctx->try_mediafoundation = try_mediafoundation;
	ctx->using_drm_prime = 0;
	ctx->codec_name[0] = '\0';
	ctx->hw_device_ctx = NULL;

	// make sure all scalers are zero initialized so that destroy can always be called
	memset(&ctx->scalers, 0, sizeof(Scalers));
	return ctx;
}

void fill_bgr0(VideoContext* ctx, const void* data, int stride, Error* err)
{
	ctx->frame = NULL;
	ScaleContext* scaler = &ctx->scalers.bgr0;
	scaler->frame_in->data[0] = (uint8_t*)data;
	scaler->frame_in->linesize[0] = stride;

	scale_frame(scaler, err);
	OK_OR_ABORT(err)
	ctx->frame = scaler->frame_out;
}

void fill_rgb(VideoContext* ctx, const void* data, Error* err)
{
	ctx->frame = NULL;
	ScaleContext* scaler = &ctx->scalers.rgb;
	ctx->frame = NULL;
	scaler->frame_in->data[0] = (uint8_t*)data;
	scaler->frame_in->linesize[0] = ctx->width_in * 3;

	scale_frame(scaler, err);
	OK_OR_ABORT(err)
	ctx->frame = scaler->frame_out;
}

void fill_rgb0(VideoContext* ctx, const void* data, Error* err)
{
	ctx->frame = NULL;
	ScaleContext* scaler = &ctx->scalers.rgb0;
	scaler->frame_in->data[0] = (uint8_t*)data;
	scaler->frame_in->linesize[0] = ctx->width_in * 4;

	scale_frame(scaler, err);
	OK_OR_ABORT(err)
	ctx->frame = scaler->frame_out;
}

void fill_drm_prime(
	VideoContext* ctx,
	int width,
	int height,
	int object_count,
	const int* object_fds,
	const size_t* object_sizes,
	const uint64_t* object_modifiers,
	int layer_count,
	const uint32_t* layer_formats,
	const int* layer_plane_counts,
	const int* plane_object_indices,
	const ptrdiff_t* plane_offsets,
	const ptrdiff_t* plane_pitches,
	Error* err)
{
	ctx->frame = NULL;
	if (!video_encoder_supports_drm_prime(ctx))
		ERROR(err, 1, "DRM PRIME input is only available with VAAPI encoding.");
	if (width != ctx->width_in || height != ctx->height_in)
		ERROR(
			err,
			1,
			"DRM PRIME frame size changed unexpectedly: %dx%d != %dx%d",
			width,
			height,
			ctx->width_in,
			ctx->height_in);
	if (object_count <= 0 || object_count > AV_DRM_MAX_PLANES)
		ERROR(err, 1, "Invalid DRM PRIME object count: %d", object_count);
	if (layer_count <= 0 || layer_count > AV_DRM_MAX_PLANES)
		ERROR(err, 1, "Invalid DRM PRIME layer count: %d", layer_count);
	enum AVPixelFormat drm_prime_sw_format = drm_prime_sw_format_from_fourcc(layer_formats[0]);
	if (drm_prime_sw_format == AV_PIX_FMT_NONE)
		ERROR(err, 1, "Unsupported DRM PRIME layer format: 0x%08x", layer_formats[0]);
	init_drm_prime_scaler(
		&ctx->scalers,
		ctx->width_in,
		ctx->height_in,
		ctx->width_out,
		ctx->height_out,
		ctx->hw_device_ctx,
		drm_prime_sw_format,
		err);
	OK_OR_ABORT(err);

	int total_planes = 0;
	for (int i = 0; i < layer_count; i++)
	{
		if (layer_plane_counts[i] <= 0 || layer_plane_counts[i] > AV_DRM_MAX_PLANES)
			ERROR(err, 1, "Invalid DRM PRIME plane count for layer %d: %d", i, layer_plane_counts[i]);
		total_planes += layer_plane_counts[i];
	}
	if (total_planes <= 0 || total_planes > AV_DRM_MAX_PLANES)
		ERROR(err, 1, "Invalid DRM PRIME total plane count: %d", total_planes);

	AVDRMFrameDescriptor* desc = av_mallocz(sizeof(*desc));
	if (!desc)
		ERROR(err, 1, "Failed to allocate DRM PRIME descriptor.");
	for (int i = 0; i < AV_DRM_MAX_PLANES; i++)
		desc->objects[i].fd = -1;

	desc->nb_objects = object_count;
	for (int i = 0; i < object_count; i++)
	{
		int dup_fd = dup(object_fds[i]);
		if (dup_fd < 0)
		{
			free_drm_prime_desc(NULL, (uint8_t*)desc);
			ERROR(err, 1, "Failed to duplicate DRM PRIME fd: %s", strerror(errno));
		}
		desc->objects[i].fd = dup_fd;
		desc->objects[i].size = object_sizes[i];
		desc->objects[i].format_modifier = object_modifiers[i];
	}

	desc->nb_layers = layer_count;
	int plane_idx = 0;
	for (int layer_idx = 0; layer_idx < layer_count; layer_idx++)
	{
		desc->layers[layer_idx].format = layer_formats[layer_idx];
		desc->layers[layer_idx].nb_planes = layer_plane_counts[layer_idx];
		for (int plane_in_layer = 0; plane_in_layer < layer_plane_counts[layer_idx];
			 plane_in_layer++)
		{
			int object_index = plane_object_indices[plane_idx];
			if (object_index < 0 || object_index >= object_count)
			{
				free_drm_prime_desc(NULL, (uint8_t*)desc);
				ERROR(err, 1, "Invalid DRM PRIME object index: %d", object_index);
			}
			desc->layers[layer_idx].planes[plane_in_layer].object_index = object_index;
			desc->layers[layer_idx].planes[plane_in_layer].offset = plane_offsets[plane_idx];
			desc->layers[layer_idx].planes[plane_in_layer].pitch = plane_pitches[plane_idx];
			plane_idx++;
		}
	}

	AVFrame* frame = ctx->scalers.drm_prime.frame_in;
	av_frame_unref(frame);
	frame->format = AV_PIX_FMT_DRM_PRIME;
	frame->width = width;
	frame->height = height;
	frame->data[0] = (uint8_t*)desc;
	frame->buf[0] = av_buffer_create(
		(uint8_t*)desc, sizeof(*desc), free_drm_prime_desc, NULL, AV_BUFFER_FLAG_READONLY);
	if (!frame->buf[0])
	{
		free_drm_prime_desc(NULL, (uint8_t*)desc);
		ERROR(err, 1, "Failed to create DRM PRIME descriptor buffer.");
	}

	scale_frame(&ctx->scalers.drm_prime, err);
	av_frame_unref(frame);
	OK_OR_ABORT(err)
	ctx->using_drm_prime = 1;
	ctx->frame = ctx->scalers.drm_prime.frame_out;
}
