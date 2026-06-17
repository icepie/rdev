#pragma once

#include <stdint.h>
#include "../error.h"

typedef struct KmsEglContext KmsEglContext;

struct KmsFrameMetadata
{
	unsigned int length;
	uint32_t src_x;
	uint32_t src_y;
	uint32_t src_w;
	uint32_t src_h;
	uint32_t dst_w;
	uint32_t dst_h;
	uint32_t fb_width;
	uint32_t fb_height;
	uint32_t fourcc;
	uint64_t modifier;
	uint32_t offsets[4];
	uint32_t pitches[4];
};

KmsEglContext* init_kms_egl(const char* card_path, Error* err);
void destroy_kms_egl(KmsEglContext* ctx);
void kms_egl_read_bgra(
	KmsEglContext* ctx,
	const struct KmsFrameMetadata* md,
	const int* fds,
	char* dst,
	size_t dst_len,
	Error* err
);
