#!/usr/bin/env bash

set -ex

cd libva

make distclean >/dev/null 2>&1 || make clean >/dev/null 2>&1 || true
git clean -fdX >/dev/null 2>&1 || true
git checkout -- pkgconfig/libva.pc.in pkgconfig/libva-x11.pc.in pkgconfig/libva-drm.pc.in >/dev/null 2>&1 || true

export CFLAGS="${CFLAGS:-} -fPIC"
export CXXFLAGS="${CXXFLAGS:-} -fPIC"

# required to make ffmpeg's configure work
sed -i -e "s/-lva$/-lva -ldrm -ldl/" pkgconfig/libva.pc.in
sed -i -e 's/-lva-\${display}$/-lva-\${display} -lX11 -lXext -lXfixes -ldrm/' pkgconfig/libva-x11.pc.in
sed -i -e 's/-lva-\${display}$/-lva-\${display} -ldrm/' pkgconfig/libva-drm.pc.in

./autogen.sh --prefix=$(readlink -f "$DIST") \
    --enable-static=yes \
    --enable-shared=yes \
    --enable-drm \
    --enable-x11 \
    --disable-glx \
    --with-drivers-path="/usr/lib/dri"

make -j$NPROCS
make install
