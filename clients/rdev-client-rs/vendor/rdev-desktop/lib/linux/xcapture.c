#include <X11/X.h>
#include <X11/Xlib.h>
#include <X11/Xutil.h>

#include <X11/extensions/XShm.h>
#include <X11/extensions/Xcomposite.h>
#include <X11/extensions/Xfixes.h>
#include <stdlib.h>
#include <string.h>
#include <sys/ipc.h>
#include <sys/shm.h>

#include <stdint.h>

#include "../error.h"
#include "../log.h"
#include "xhelper.h"

int clamp(int x, int lb, int ub)
{
	if (x < lb)
		return lb;
	if (x > ub)
		return ub;
	return x;
}

struct CaptureContext
{
	Capturable cap;
	XImage* ximg;
	XShmSegmentInfo shminfo;
	int use_xshm;
	int has_xfixes;
	int has_offscreen;
	int wayland;
	Bool last_img_return;
};

typedef struct CaptureContext CaptureContext;

struct Image
{
	char* data;
	unsigned int width;
	unsigned int height;
};

static void free_capture_image(CaptureContext* ctx)
{
	if (!ctx || !ctx->ximg)
		return;

	if (ctx->use_xshm)
	{
		XShmDetach(ctx->cap.disp, &ctx->shminfo);
		if (ctx->shminfo.shmaddr)
			shmdt(ctx->shminfo.shmaddr);
		if (ctx->shminfo.shmid >= 0)
			shmctl(ctx->shminfo.shmid, IPC_RMID, NULL);
		ctx->shminfo.shmaddr = NULL;
		ctx->shminfo.shmid = -1;
	}

	XDestroyImage(ctx->ximg);
	ctx->ximg = NULL;
	ctx->use_xshm = 0;
}

static void free_unattached_xshm_image(CaptureContext* ctx)
{
	if (!ctx || !ctx->ximg)
		return;

	if (ctx->shminfo.shmaddr)
	{
		shmdt(ctx->shminfo.shmaddr);
		ctx->shminfo.shmaddr = NULL;
	}
	if (ctx->shminfo.shmid >= 0)
	{
		shmctl(ctx->shminfo.shmid, IPC_RMID, NULL);
		ctx->shminfo.shmid = -1;
	}
	XDestroyImage(ctx->ximg);
	ctx->ximg = NULL;
}

static int ensure_xshm_image(CaptureContext* ctx, unsigned int width, unsigned int height)
{
	if (XShmQueryExtension(ctx->cap.disp) != True)
		return 0;

	ctx->ximg = XShmCreateImage(
		ctx->cap.disp,
		DefaultVisualOfScreen(ctx->cap.screen),
		DefaultDepthOfScreen(ctx->cap.screen),
		ZPixmap,
		NULL,
		&ctx->shminfo,
		width,
		height);
	if (!ctx->ximg)
		return 0;

	ctx->shminfo.shmid =
		shmget(IPC_PRIVATE, ctx->ximg->bytes_per_line * ctx->ximg->height, IPC_CREAT | 0777);
	if (ctx->shminfo.shmid < 0)
	{
		XDestroyImage(ctx->ximg);
		ctx->ximg = NULL;
		return 0;
	}

	ctx->shminfo.shmaddr = shmat(ctx->shminfo.shmid, 0, 0);
	if (ctx->shminfo.shmaddr == (char*)-1)
	{
		ctx->shminfo.shmaddr = NULL;
		shmctl(ctx->shminfo.shmid, IPC_RMID, NULL);
		ctx->shminfo.shmid = -1;
		XDestroyImage(ctx->ximg);
		ctx->ximg = NULL;
		return 0;
	}

	ctx->ximg->data = ctx->shminfo.shmaddr;
	ctx->shminfo.readOnly = False;

	x11_clear_error_state();
	if (!XShmAttach(ctx->cap.disp, &ctx->shminfo))
	{
		free_unattached_xshm_image(ctx);
		return 0;
	}
	ctx->use_xshm = 1;
	XSync(ctx->cap.disp, False);
	if (x11_take_error_state(NULL, NULL, NULL))
	{
		free_capture_image(ctx);
		return 0;
	}

	return 1;
}

static Bool capture_drawable(
	CaptureContext* ctx, Drawable drawable, int x, int y, unsigned int width, unsigned int height)
{
	if (ctx->use_xshm)
	{
		int error_code = 0, request_code = 0, minor_code = 0;
		x11_clear_error_state();
		Bool ret = XShmGetImage(ctx->cap.disp, drawable, ctx->ximg, x, y, 0x00ffffff);
		XSync(ctx->cap.disp, False);
		if (ret == True && !x11_take_error_state(&error_code, &request_code, &minor_code))
			return True;

		log_warn(
			"XShm capture failed, falling back to XGetImage (error=%d request=%d minor=%d).",
			error_code,
			request_code,
			minor_code);
		free_capture_image(ctx);
	}

	if (ctx->ximg)
	{
		XDestroyImage(ctx->ximg);
		ctx->ximg = NULL;
	}

	x11_clear_error_state();
	ctx->ximg = XGetImage(ctx->cap.disp, drawable, x, y, width, height, 0x00ffffff, ZPixmap);
	XSync(ctx->cap.disp, False);
	if (!ctx->ximg || x11_take_error_state(NULL, NULL, NULL))
	{
		if (ctx->ximg)
		{
			XDestroyImage(ctx->ximg);
			ctx->ximg = NULL;
		}
		return False;
	}

	return True;
}

void* start_capture(Capturable* cap, CaptureContext* ctx, Error* err)
{
	if (!ctx)
	{
		ctx = malloc(sizeof(CaptureContext));
		memset(ctx, 0, sizeof(CaptureContext));
		ctx->shminfo.shmid = -1;

		int major, minor;
		Bool pixmaps = False;
		if (XShmQueryExtension(cap->disp) == True)
		{
			XShmQueryVersion(cap->disp, &major, &minor, &pixmaps);
			ctx->has_offscreen = pixmaps == True;
		}
		else
			ctx->has_offscreen = 0;
		if (ctx->has_offscreen && cap->type == WINDOW && cap->c.winfo.is_regular_window)
		{
			int event_base, error_base;
			ctx->has_offscreen =
				XCompositeQueryExtension(cap->disp, &event_base, &error_base) == True;
			if (ctx->has_offscreen)
				XCompositeRedirectWindow(cap->disp, cap->c.winfo.win, False);
		}
		const char* session_type = getenv("XDG_SESSION_TYPE");
		if (session_type && strcmp(session_type, "wayland") == 0)
			ctx->wayland = 1;
		else
			ctx->wayland = 0;
	}
	ctx->cap = *cap;
	ctx->last_img_return = True;

	if (&ctx->cap != cap)
		strncpy(ctx->cap.name, cap->name, sizeof(ctx->cap.name));

	int event_base, error_base;
	ctx->has_xfixes = XFixesQueryExtension(cap->disp, &event_base, &error_base) == True;

	int x, y;
	unsigned int width, height;
	get_geometry(cap, &x, &y, &width, &height, err);
	if (err->code)
		return NULL;
	if (!ensure_xshm_image(ctx, width, height))
		log_warn("X11 capture is using XGetImage fallback instead of XShm.");

	return ctx;
}

void stop_capture(CaptureContext* ctx, Error* err)
{
	(void)err;
	free_capture_image(ctx);
	if (ctx->has_offscreen && ctx->cap.type == WINDOW && ctx->cap.c.winfo.is_regular_window)
		XCompositeUnredirectWindow(ctx->cap.disp, ctx->cap.c.winfo.win, False);
	free(ctx);
}

void capture_screen(CaptureContext* ctx, struct Image* img, int capture_cursor, Error* err)
{
	Window root = DefaultRootWindow(ctx->cap.disp);
	int x, y;
	unsigned int width, height;
	get_geometry(&ctx->cap, &x, &y, &width, &height, err);
	OK_OR_ABORT(err);
	// if window resized, create new cap...
	if (ctx->use_xshm &&
		(width != (unsigned int)ctx->ximg->width || height != (unsigned int)ctx->ximg->height))
	{
		free_capture_image(ctx);
		if (!ensure_xshm_image(ctx, width, height))
			log_warn("X11 capture resized into XGetImage fallback instead of XShm.");
	}
	else if (!ctx->use_xshm && ctx->ximg &&
			 (width != (unsigned int)ctx->ximg->width || height != (unsigned int)ctx->ximg->height))
	{
		XDestroyImage(ctx->ximg);
		ctx->ximg = NULL;
	}

	Bool get_img_ret = False;

	switch (ctx->cap.type)
	{
	case WINDOW:
	{
		Window* active_window;
		unsigned long size;
		Error active_window_err = {0};

		int is_offscreen = ctx->cap.c.winfo.is_regular_window &&
						   (x < 0 || y < 0 || x + (int)width > ctx->cap.screen->width ||
							y + (int)height > ctx->cap.screen->height);

		active_window = (Window*)get_property(
			ctx->cap.disp, root, XA_WINDOW, "_NET_ACTIVE_WINDOW", &size, &active_window_err);
		if (active_window_err.code)
			log_debug(
				"Ignoring _NET_ACTIVE_WINDOW lookup failure during X11 capture: %s",
				active_window_err.error_str);
		if (!ctx->wayland && active_window && *active_window == ctx->cap.c.winfo.win && !is_offscreen)
		{
			// cap window within its root so menus are visible as strictly speaking menus do not
			// belong to the window itself ...
			// But don't do this on (X)Wayland as the root window is just black in that case.
			get_img_ret = capture_drawable(ctx, root, x, y, width, height);
		}
		else
		{
			// ... but only if it is the active window as we might be recording the wrong thing
			// otherwise. If it is not active just record the window itself.
			// also if pixmaps are supported use those as they support capturing windows even if
			// they are offscreen
			if (is_offscreen)
			{
				if (ctx->has_offscreen)
				{
					Pixmap pm = XCompositeNameWindowPixmap(ctx->cap.disp, ctx->cap.c.winfo.win);
					get_img_ret = capture_drawable(ctx, pm, 0, 0, width, height);
					XFreePixmap(ctx->cap.disp, pm);
				}
				else
				{
					fill_error(
						err,
						1,
						"Can not capture window as it is off screen and Xcomposite is "
						"unavailable!");
					free(active_window);
					return;
				}
			}
			else
				get_img_ret = capture_drawable(ctx, ctx->cap.c.winfo.win, 0, 0, width, height);
		}
		free(active_window);
		break;
	}
	case RECT:
		get_img_ret = capture_drawable(ctx, root, x, y, width, height);
		break;
	}

	Bool last_img_return = ctx->last_img_return;
	ctx->last_img_return = get_img_ret;
	// only print an error once and do not repeat this message if consecutive calls fail to avoid
	// spamming the logs.
	if (get_img_ret != True || !ctx->ximg)
	{
		if (last_img_return != get_img_ret)
			fill_error(err, 1, "X11 image capture failed!");
		else
			fill_error(err, 2, "X11 image capture failed!");
		return;
	}

	// capture cursor if requested and if XFixes is available
	if (capture_cursor && ctx->has_xfixes)
	{
		XFixesCursorImage* cursor_img = XFixesGetCursorImage(ctx->cap.disp);
		if (cursor_img != NULL)
		{
			uint32_t* data = (uint32_t*)ctx->ximg->data;

			// coordinates of cursor inside ximg
			int x0 = cursor_img->x - cursor_img->xhot - x;
			int y0 = cursor_img->y - cursor_img->yhot - y;

			// clamp part of cursor image to draw to the part of the cursor that is inside
			// the captured area
			int i0 = clamp(0, -x0, width - x0);
			int i1 = clamp(cursor_img->width, -x0, width - x0);
			int j0 = clamp(0, -y0, height - y0);
			int j1 = clamp(cursor_img->height, -y0, height - y0);
			// paint cursor image into captured image
			for (int j = j0; j < j1; ++j)
				for (int i = i0; i < i1; ++i)
				{
					uint32_t c_pixel = cursor_img->pixels[j * cursor_img->width + i];
					unsigned char a = (c_pixel & 0xff000000) >> 24;
					if (a)
					{
						uint32_t d_pixel = data[(j + y0) * width + i + x0];

						unsigned char c1 = (c_pixel & 0x00ff0000) >> 16;
						unsigned char c2 = (c_pixel & 0x0000ff00) >> 8;
						unsigned char c3 = (c_pixel & 0x000000ff) >> 0;
						unsigned char d1 = (d_pixel & 0x00ff0000) >> 16;
						unsigned char d2 = (d_pixel & 0x0000ff00) >> 8;
						unsigned char d3 = (d_pixel & 0x000000ff) >> 0;
						// colors from the cursor image are premultiplied with the alpha channel
						unsigned char f1 = c1 + d1 * (255 - a) / 255;
						unsigned char f2 = c2 + d2 * (255 - a) / 255;
						unsigned char f3 = c3 + d3 * (255 - a) / 255;
						data[(j + y0) * width + i + x0] = (f1 << 16) | (f2 << 8) | (f3 << 0);
					}
				}

			XFree(cursor_img);
		}
		else
		{
			log_warn(
				"Failed to obtain cursor image, XFixesGetCursorImage has returned a null pointer.");
		}
	}

	img->width = ctx->ximg->width;
	img->height = ctx->ximg->height;
	img->data = ctx->ximg->data;
}
