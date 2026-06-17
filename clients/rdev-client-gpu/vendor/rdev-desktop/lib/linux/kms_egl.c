#include <epoxy/egl.h>
#include <epoxy/gl.h>
#include <stdbool.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include <libdrm/drm_fourcc.h>

#include "../error.h"
#include "../log.h"
#include "kms_egl.h"

typedef EGLImage (*PFNEGLCREATEIMAGEFALLBACKPROC)(
	EGLDisplay dpy,
	EGLContext ctx,
	EGLenum target,
	EGLClientBuffer buffer,
	const EGLAttrib* attrib_list
);
typedef EGLBoolean (*PFNEGLDESTROYIMAGEFALLBACKPROC)(EGLDisplay dpy, EGLImage image);

struct KmsEglContext
{
	EGLDisplay display;
	EGLContext context;
	EGLSurface surface;
	EGLConfig config;
	int gles_major;
	int width;
	int height;
	GLuint program;
	GLuint texture;
	GLuint vbo;
	GLint read_format;
	PFNEGLCREATEIMAGEFALLBACKPROC create_image;
	PFNEGLDESTROYIMAGEFALLBACKPROC destroy_image;
};

static void append_attrib(EGLAttrib** cursor, EGLAttrib key, EGLAttrib value)
{
	*(*cursor)++ = key;
	*(*cursor)++ = value;
}

static GLuint compile_shader(GLenum type, const char* source)
{
	GLuint shader = glCreateShader(type);
	if (!shader)
		return 0;
	glShaderSource(shader, 1, &source, NULL);
	glCompileShader(shader);

	GLint compiled = 0;
	glGetShaderiv(shader, GL_COMPILE_STATUS, &compiled);
	if (!compiled)
	{
		glDeleteShader(shader);
		return 0;
	}
	return shader;
}

static GLuint make_program(void)
{
	const char vs[] =
		"attribute vec2 a_pos;\n"
		"attribute vec2 a_uv;\n"
		"varying vec2 v_uv;\n"
		"void main() {\n"
		"  gl_Position = vec4(a_pos, 0.0, 1.0);\n"
		"  v_uv = a_uv;\n"
		"}\n";
	const char fs[] =
		"#extension GL_OES_EGL_image_external : require\n"
		"precision highp float;\n"
		"uniform samplerExternalOES u_tex;\n"
		"varying vec2 v_uv;\n"
		"void main() {\n"
		"  gl_FragColor = texture2D(u_tex, v_uv);\n"
		"}\n";

	GLuint vertex = compile_shader(GL_VERTEX_SHADER, vs);
	GLuint fragment = compile_shader(GL_FRAGMENT_SHADER, fs);
	if (!vertex || !fragment)
	{
		if (vertex)
			glDeleteShader(vertex);
		if (fragment)
			glDeleteShader(fragment);
		return 0;
	}

	GLuint program = glCreateProgram();
	if (!program)
	{
		glDeleteShader(vertex);
		glDeleteShader(fragment);
		return 0;
	}

	glAttachShader(program, vertex);
	glAttachShader(program, fragment);
	glLinkProgram(program);
	glDeleteShader(vertex);
	glDeleteShader(fragment);

	GLint linked = 0;
	glGetProgramiv(program, GL_LINK_STATUS, &linked);
	if (!linked)
	{
		glDeleteProgram(program);
		return 0;
	}

	return program;
}

static EGLDisplay get_egl_display_from_drm_card(const char* card_path)
{
	PFNEGLQUERYDEVICESEXTPROC eglQueryDevicesEXT =
		(PFNEGLQUERYDEVICESEXTPROC)eglGetProcAddress("eglQueryDevicesEXT");
	PFNEGLQUERYDEVICESTRINGEXTPROC eglQueryDeviceStringEXT =
		(PFNEGLQUERYDEVICESTRINGEXTPROC)eglGetProcAddress("eglQueryDeviceStringEXT");
	PFNEGLGETPLATFORMDISPLAYEXTPROC eglGetPlatformDisplayEXT =
		(PFNEGLGETPLATFORMDISPLAYEXTPROC)eglGetProcAddress("eglGetPlatformDisplayEXT");

	if (!eglQueryDevicesEXT || !eglQueryDeviceStringEXT || !eglGetPlatformDisplayEXT)
		return EGL_NO_DISPLAY;

	EGLint max_devices = 0;
	if (!eglQueryDevicesEXT(0, NULL, &max_devices) || max_devices <= 0)
		return EGL_NO_DISPLAY;

	EGLDeviceEXT* devices = calloc((size_t)max_devices, sizeof(*devices));
	if (!devices)
		return EGL_NO_DISPLAY;

	EGLDisplay display = EGL_NO_DISPLAY;
	EGLint num_devices = 0;
	if (eglQueryDevicesEXT(max_devices, devices, &num_devices))
	{
		for (EGLint i = 0; i < num_devices; ++i)
		{
			const char* device_file =
				eglQueryDeviceStringEXT(devices[i], EGL_DRM_DEVICE_FILE_EXT);
			if (device_file && strcmp(device_file, card_path) == 0)
			{
				display =
					eglGetPlatformDisplayEXT(EGL_PLATFORM_DEVICE_EXT, devices[i], NULL);
				break;
			}
		}
	}
	free(devices);
	return display;
}

static void fill_error_egl(Error* err, const char* what)
{
	fill_error(err, 1, "%s: EGL error %#x", what, eglGetError());
}

static int ensure_surface(KmsEglContext* ctx, int width, int height, Error* err)
{
	if (ctx->surface != EGL_NO_SURFACE && ctx->width == width && ctx->height == height)
		return 0;

	if (ctx->surface != EGL_NO_SURFACE)
	{
		eglDestroySurface(ctx->display, ctx->surface);
		ctx->surface = EGL_NO_SURFACE;
	}

	EGLint surface_attribs[] = {
		EGL_WIDTH, width,
		EGL_HEIGHT, height,
		EGL_NONE
	};
	ctx->surface =
		eglCreatePbufferSurface(ctx->display, ctx->config, surface_attribs);
	if (ctx->surface == EGL_NO_SURFACE)
	{
		fill_error_egl(err, "EGL: Failed to create pbuffer surface.");
		return -1;
	}

	ctx->width = width;
	ctx->height = height;
	return 0;
}

static int ensure_context_current(KmsEglContext* ctx, Error* err)
{
	if (!eglMakeCurrent(ctx->display, ctx->surface, ctx->surface, ctx->context))
	{
		fill_error_egl(err, "EGL: Failed to make context current.");
		return -1;
	}
	return 0;
}

static int setup_gl_state(KmsEglContext* ctx, Error* err)
{
	(void)err;
	ctx->program = make_program();
	if (!ctx->program)
	{
		fill_error_egl(err, "GL: Failed to build shader program.");
		return -1;
	}

	glUseProgram(ctx->program);
	glUniform1i(glGetUniformLocation(ctx->program, "u_tex"), 0);

	glGenTextures(1, &ctx->texture);
	glBindTexture(GL_TEXTURE_EXTERNAL_OES, ctx->texture);
	glTexParameteri(GL_TEXTURE_EXTERNAL_OES, GL_TEXTURE_MIN_FILTER, GL_LINEAR);
	glTexParameteri(GL_TEXTURE_EXTERNAL_OES, GL_TEXTURE_MAG_FILTER, GL_LINEAR);
	glTexParameteri(GL_TEXTURE_EXTERNAL_OES, GL_TEXTURE_WRAP_S, GL_CLAMP_TO_EDGE);
	glTexParameteri(GL_TEXTURE_EXTERNAL_OES, GL_TEXTURE_WRAP_T, GL_CLAMP_TO_EDGE);

	glGenBuffers(1, &ctx->vbo);
	glBindBuffer(GL_ARRAY_BUFFER, ctx->vbo);
	const float vertices[] = {
		-1.0f, -1.0f, 0.0f, 1.0f,
		1.0f, -1.0f, 1.0f, 1.0f,
		-1.0f, 1.0f, 0.0f, 0.0f,
		1.0f, 1.0f, 1.0f, 0.0f,
	};
	glBufferData(GL_ARRAY_BUFFER, sizeof(vertices), vertices, GL_STATIC_DRAW);

	GLint pos = glGetAttribLocation(ctx->program, "a_pos");
	GLint uv = glGetAttribLocation(ctx->program, "a_uv");
	glEnableVertexAttribArray((GLuint)pos);
	glVertexAttribPointer((GLuint)pos, 2, GL_FLOAT, GL_FALSE, 4 * sizeof(float), (void*)0);
	glEnableVertexAttribArray((GLuint)uv);
	glVertexAttribPointer((GLuint)uv, 2, GL_FLOAT, GL_FALSE, 4 * sizeof(float), (void*)(2 * sizeof(float)));

	glBindBuffer(GL_ARRAY_BUFFER, 0);
	glUseProgram(0);

	const char* extensions = (const char*)glGetString(GL_EXTENSIONS);
	ctx->read_format =
		extensions && strstr(extensions, "GL_EXT_read_format_bgra") ? GL_BGRA_EXT : GL_RGBA;
	return 0;
}

KmsEglContext* init_kms_egl(const char* card_path, Error* err)
{
	KmsEglContext* ctx = calloc(1, sizeof(*ctx));
	if (!ctx)
	{
		fill_error(err, 1, "Failed to allocate KmsEglContext.");
		return NULL;
	}

	ctx->display = get_egl_display_from_drm_card(card_path);
	if (ctx->display == EGL_NO_DISPLAY)
		ctx->display = eglGetDisplay(EGL_DEFAULT_DISPLAY);
	if (ctx->display == EGL_NO_DISPLAY)
	{
		fill_error_egl(err, "EGL: Failed to get display.");
		goto fail;
	}

	EGLint major = 0;
	EGLint minor = 0;
	if (!eglInitialize(ctx->display, &major, &minor))
	{
		fill_error_egl(err, "EGL: Failed to initialize.");
		goto fail;
	}

	eglBindAPI(EGL_OPENGL_ES_API);
	EGLint config_attribs[] = {
		EGL_RENDERABLE_TYPE, EGL_OPENGL_ES2_BIT,
		EGL_SURFACE_TYPE, EGL_PBUFFER_BIT,
		EGL_RED_SIZE, 8,
		EGL_GREEN_SIZE, 8,
		EGL_BLUE_SIZE, 8,
		EGL_ALPHA_SIZE, 8,
		EGL_NONE
	};
	EGLint num_configs = 0;
	eglChooseConfig(ctx->display, config_attribs, &ctx->config, 1, &num_configs);
	if (!num_configs)
	{
		fill_error_egl(err, "EGL: Failed to choose config.");
		goto fail;
	}

	EGLint ctx_attribs[] = {
		EGL_CONTEXT_CLIENT_VERSION, 2,
		EGL_NONE
	};
	ctx->context = eglCreateContext(ctx->display, ctx->config, EGL_NO_CONTEXT, ctx_attribs);
	if (ctx->context == EGL_NO_CONTEXT)
	{
		fill_error_egl(err, "EGL: Failed to create context.");
		goto fail;
	}

	ctx->surface = EGL_NO_SURFACE;
	ctx->create_image = (PFNEGLCREATEIMAGEFALLBACKPROC)eglGetProcAddress("eglCreateImage");
	if (!ctx->create_image)
		ctx->create_image = (PFNEGLCREATEIMAGEFALLBACKPROC)eglGetProcAddress("eglCreateImageKHR");
	ctx->destroy_image = (PFNEGLDESTROYIMAGEFALLBACKPROC)eglGetProcAddress("eglDestroyImage");
	if (!ctx->destroy_image)
		ctx->destroy_image = (PFNEGLDESTROYIMAGEFALLBACKPROC)eglGetProcAddress("eglDestroyImageKHR");
	if (!ctx->create_image || !ctx->destroy_image)
	{
		fill_error(err, 1, "EGL: image extension entry points are unavailable.");
		goto fail;
	}
	ctx->width = 0;
	ctx->height = 0;
	if (ensure_surface(ctx, 16, 16, err) != 0)
		goto fail;
	if (ensure_context_current(ctx, err) != 0)
		goto fail;
	if (setup_gl_state(ctx, err) != 0)
		goto fail;

	return ctx;

fail:
	destroy_kms_egl(ctx);
	return NULL;
}

void destroy_kms_egl(KmsEglContext* ctx)
{
	if (!ctx)
		return;
	bool context_current = false;
	if (
		ctx->display != EGL_NO_DISPLAY &&
		ctx->context != EGL_NO_CONTEXT &&
		ctx->surface != EGL_NO_SURFACE
	)
	{
		context_current =
			eglMakeCurrent(ctx->display, ctx->surface, ctx->surface, ctx->context);
	}
	if (ctx->vbo)
		glDeleteBuffers(1, &ctx->vbo);
	if (ctx->texture)
		glDeleteTextures(1, &ctx->texture);
	if (ctx->program)
		glDeleteProgram(ctx->program);
	if (context_current)
		eglMakeCurrent(ctx->display, EGL_NO_SURFACE, EGL_NO_SURFACE, EGL_NO_CONTEXT);
	if (ctx->surface != EGL_NO_SURFACE)
		eglDestroySurface(ctx->display, ctx->surface);
	if (ctx->context != EGL_NO_CONTEXT)
		eglDestroyContext(ctx->display, ctx->context);
	if (ctx->display != EGL_NO_DISPLAY)
		eglTerminate(ctx->display);
	free(ctx);
}

static EGLImage make_image(KmsEglContext* ctx, const struct KmsFrameMetadata* md, const int* fds)
{
	static const EGLAttrib fd_keys[4] = {
		EGL_DMA_BUF_PLANE0_FD_EXT,
		EGL_DMA_BUF_PLANE1_FD_EXT,
		EGL_DMA_BUF_PLANE2_FD_EXT,
		EGL_DMA_BUF_PLANE3_FD_EXT,
	};
	static const EGLAttrib offset_keys[4] = {
		EGL_DMA_BUF_PLANE0_OFFSET_EXT,
		EGL_DMA_BUF_PLANE1_OFFSET_EXT,
		EGL_DMA_BUF_PLANE2_OFFSET_EXT,
		EGL_DMA_BUF_PLANE3_OFFSET_EXT,
	};
	static const EGLAttrib pitch_keys[4] = {
		EGL_DMA_BUF_PLANE0_PITCH_EXT,
		EGL_DMA_BUF_PLANE1_PITCH_EXT,
		EGL_DMA_BUF_PLANE2_PITCH_EXT,
		EGL_DMA_BUF_PLANE3_PITCH_EXT,
	};
	static const EGLAttrib modifier_lo_keys[4] = {
		EGL_DMA_BUF_PLANE0_MODIFIER_LO_EXT,
		EGL_DMA_BUF_PLANE1_MODIFIER_LO_EXT,
		EGL_DMA_BUF_PLANE2_MODIFIER_LO_EXT,
		EGL_DMA_BUF_PLANE3_MODIFIER_LO_EXT,
	};
	static const EGLAttrib modifier_hi_keys[4] = {
		EGL_DMA_BUF_PLANE0_MODIFIER_HI_EXT,
		EGL_DMA_BUF_PLANE1_MODIFIER_HI_EXT,
		EGL_DMA_BUF_PLANE2_MODIFIER_HI_EXT,
		EGL_DMA_BUF_PLANE3_MODIFIER_HI_EXT,
	};

	EGLAttrib attribs[64];
	EGLAttrib* cursor = attribs;

	append_attrib(&cursor, EGL_WIDTH, md->fb_width);
	append_attrib(&cursor, EGL_HEIGHT, md->fb_height);
	append_attrib(&cursor, EGL_LINUX_DRM_FOURCC_EXT, md->fourcc);
	for (unsigned int i = 0; i < md->length && i < 4; ++i)
	{
		append_attrib(&cursor, fd_keys[i], fds[i]);
		append_attrib(&cursor, offset_keys[i], md->offsets[i]);
		append_attrib(&cursor, pitch_keys[i], md->pitches[i]);
		if (md->modifier != DRM_FORMAT_MOD_INVALID)
		{
			append_attrib(&cursor, modifier_lo_keys[i], md->modifier & 0xffffffffu);
			append_attrib(&cursor, modifier_hi_keys[i], md->modifier >> 32);
		}
	}
	append_attrib(&cursor, EGL_NONE, EGL_NONE);

	return ctx->create_image(
		ctx->display,
		EGL_NO_CONTEXT,
		EGL_LINUX_DMA_BUF_EXT,
		(EGLClientBuffer)NULL,
		attribs
	);
}

static void rgba_to_bgra(char* dst, size_t pixels)
{
	for (size_t i = 0; i < pixels; ++i)
	{
		char* pixel = dst + i * 4;
		char tmp = pixel[0];
		pixel[0] = pixel[2];
		pixel[2] = tmp;
	}
}

static void flip_rows(char* dst, uint32_t width, uint32_t height)
{
	const size_t stride = (size_t)width * 4;
	char* tmp = malloc(stride);
	if (!tmp)
		return;
	for (uint32_t y = 0; y < height / 2; ++y)
	{
		char* top = dst + (size_t)y * stride;
		char* bottom = dst + (size_t)(height - 1 - y) * stride;
		memcpy(tmp, top, stride);
		memcpy(top, bottom, stride);
		memcpy(bottom, tmp, stride);
	}
	free(tmp);
}

void kms_egl_read_bgra(
	KmsEglContext* ctx,
	const struct KmsFrameMetadata* md,
	const int* fds,
	char* dst,
	size_t dst_len,
	Error* err
)
{
	if (!ctx || !md || !fds || !dst)
	{
		fill_error(err, 1, "EGL: invalid arguments.");
		return;
	}
	if (md->length == 0 || fds[0] < 0)
	{
		fill_error(err, 1, "EGL: invalid dma-buf set.");
		return;
	}
	if (md->length > 4)
	{
		fill_error(err, 1, "EGL: too many framebuffer planes.");
		return;
	}
	const uint32_t dst_w = md->dst_w ? md->dst_w : md->fb_width;
	const uint32_t dst_h = md->dst_h ? md->dst_h : md->fb_height;
	const uint32_t src_x = md->src_x;
	const uint32_t src_y = md->src_y;
	const uint32_t src_w = md->src_w ? md->src_w : md->fb_width;
	const uint32_t src_h = md->src_h ? md->src_h : md->fb_height;
	if (
		src_x >= md->fb_width ||
		src_y >= md->fb_height ||
		src_w == 0 ||
		src_h == 0 ||
		src_x + src_w > md->fb_width ||
		src_y + src_h > md->fb_height ||
		dst_w == 0 ||
		dst_h == 0
	)
	{
		fill_error(
			err,
			1,
			"EGL: invalid crop src=%ux%u+%u+%u fb=%ux%u dst=%ux%u.",
			src_w,
			src_h,
			src_x,
			src_y,
			md->fb_width,
			md->fb_height,
			dst_w,
			dst_h
		);
		return;
	}

	if (ensure_surface(ctx, (int)dst_w, (int)dst_h, err) != 0)
		return;
	if (ensure_context_current(ctx, err) != 0)
		return;

	size_t needed = (size_t)dst_w * (size_t)dst_h * 4;
	if (dst_len < needed)
	{
		fill_error(err, 1, "EGL: output buffer too small.");
		return;
	}

	EGLImage image = make_image(ctx, md, fds);
	if (image == EGL_NO_IMAGE)
	{
		fill_error(err, 1, "EGL: failed to create image: EGL error %#x", eglGetError());
		return;
	}

	glViewport(0, 0, dst_w, dst_h);
	glClearColor(0.0f, 0.0f, 0.0f, 1.0f);
	glClear(GL_COLOR_BUFFER_BIT);

	const float left = (float)src_x / (float)md->fb_width;
	const float right = (float)(src_x + src_w) / (float)md->fb_width;
	const float top = (float)src_y / (float)md->fb_height;
	const float bottom = (float)(src_y + src_h) / (float)md->fb_height;
	const float vertices[] = {
		-1.0f, -1.0f, left, bottom,
		1.0f, -1.0f, right, bottom,
		-1.0f, 1.0f, left, top,
		1.0f, 1.0f, right, top,
	};
	glBindBuffer(GL_ARRAY_BUFFER, ctx->vbo);
	glBufferSubData(GL_ARRAY_BUFFER, 0, sizeof(vertices), vertices);

	glUseProgram(ctx->program);
	glActiveTexture(GL_TEXTURE0);
	glBindTexture(GL_TEXTURE_EXTERNAL_OES, ctx->texture);
	glEGLImageTargetTexture2DOES(GL_TEXTURE_EXTERNAL_OES, image);
	glDrawArrays(GL_TRIANGLE_STRIP, 0, 4);
	glFinish();

	glPixelStorei(GL_PACK_ALIGNMENT, 4);
	glReadPixels(0, 0, dst_w, dst_h, ctx->read_format, GL_UNSIGNED_BYTE, dst);
	GLenum gl_err = glGetError();
	if (gl_err == GL_NO_ERROR && ctx->read_format == GL_RGBA)
		rgba_to_bgra(dst, (size_t)dst_w * (size_t)dst_h);
	if (gl_err == GL_NO_ERROR)
		flip_rows(dst, dst_w, dst_h);

	glBindBuffer(GL_ARRAY_BUFFER, 0);
	glActiveTexture(GL_TEXTURE0);
	glBindTexture(GL_TEXTURE_EXTERNAL_OES, 0);
	ctx->destroy_image(ctx->display, image);

	if (gl_err != GL_NO_ERROR)
	{
		fill_error(err, 1, "GL: readback failed: GL error %#x", gl_err);
		return;
	}
}
