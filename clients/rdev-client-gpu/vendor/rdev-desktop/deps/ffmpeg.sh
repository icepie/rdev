#!/usr/bin/env bash

set -ex

cd ffmpeg

PKG_CONFIG_DIST="$DIST"
if command -v cygpath >/dev/null 2>&1; then
	PKG_CONFIG_DIST="$(cygpath -u "$DIST")"
fi

export PKG_CONFIG_PATH="$PKG_CONFIG_DIST/lib/pkgconfig:/usr/lib64/pkgconfig:/usr/lib/x86_64-linux-gnu/pkgconfig:/usr/lib/aarch64-linux-gnu/pkgconfig:/usr/share/pkgconfig:/usr/lib/pkgconfig${PKG_CONFIG_PATH:+:$PKG_CONFIG_PATH}"
export PKG_CONFIG_ALLOW_SYSTEM_CFLAGS=1
export PKG_CONFIG_ALLOW_SYSTEM_LIBS=1

if ! ./configure \
	--prefix="$DIST" \
	--disable-debug \
	--enable-static \
	--disable-shared \
	--enable-pic \
	--enable-stripping \
	--disable-programs \
	--enable-gpl \
	--enable-libx264 \
	--disable-autodetect \
	--extra-cflags="$FFMPEG_CFLAGS" \
	--extra-ldflags="$FFMPEG_LIBRARY_PATH" \
	$FFMPEG_EXTRA_ARGS; then
	if [ -f ffbuild/config.log ]; then
		tail -200 ffbuild/config.log
	fi
	exit 1
fi

make -j$NPROCS
make install
