#!/usr/bin/env bash

set -ex

cd x264

make distclean >/dev/null 2>&1 || make clean >/dev/null 2>&1 || true
git clean -fdX >/dev/null 2>&1 || true

X264_CONFIGURE_ARGS=()
if [ -n "${X264_EXTRA_ARGS:-}" ]; then
	eval "X264_CONFIGURE_ARGS=($X264_EXTRA_ARGS)"
fi

./configure \
	--prefix="$DIST" \
	--exec-prefix="$DIST" \
	--enable-static \
	--enable-pic \
	--enable-strip \
	--disable-cli \
	--disable-opencl \
	"${X264_CONFIGURE_ARGS[@]}"

make -j$NPROCS
make install
