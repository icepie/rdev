#!/usr/bin/env bash

set -ex

export TARGET_OS="$CARGO_CFG_TARGET_OS"

if [ "$OSTYPE" == "linux-gnu" ]; then
    export HOST_OS="linux"
fi

if [[ "$OSTYPE" == "darwin"* ]]; then
    export HOST_OS="macos"
fi

if [ "$OS" == "Windows_NT" ]; then
    export HOST_OS="windows"
fi

[ -z "$DIST" ] && export DIST="$PWD/dist"
[ -z "$TARGET_OS" ] && export TARGET_OS="$HOST_OS"
[ -z "$TARGET_ARCH" ] && export TARGET_ARCH="$CARGO_CFG_TARGET_ARCH"
[ -z "$TARGET_ENV" ] && export TARGET_ENV="$CARGO_CFG_TARGET_ENV"
[ -z "$TARGET_ABI" ] && export TARGET_ABI="$CARGO_CFG_TARGET_ABI"
[ -z "$ENABLE_VAAPI" ] && export ENABLE_VAAPI="n"
[ -z "$ENABLE_NVENC" ] && export ENABLE_NVENC="n"
[ -z "$ENABLE_VULKAN_VIDEO" ] && export ENABLE_VULKAN_VIDEO="n"

export NPROCS="$(nproc || echo 4)"

./download.sh

if [ "$TARGET_OS" == "windows" ]; then
    if [ "$HOST_OS" == "linux" ] && [ "$TARGET_ARCH" == "aarch64" ]; then
        export MINGW_PREFIX="aarch64-w64-mingw32-"
        export MINGW_CC="${RDEV_MINGW_CC:-aarch64-w64-mingw32-clang}"
        export MINGW_CXX="${RDEV_MINGW_CXX:-aarch64-w64-mingw32-clang++}"
        export MINGW_ARCH="aarch64"
        export MINGW_DISABLE_ASM="--disable-asm"
        export MINGW_HOST="aarch64-w64-mingw32"
    elif [ "$HOST_OS" == "linux" ]; then
        export MINGW_PREFIX="x86_64-w64-mingw32-"
        export MINGW_CC="${RDEV_MINGW_CC:-x86_64-w64-mingw32-clang}"
        export MINGW_CXX="${RDEV_MINGW_CXX:-x86_64-w64-mingw32-clang++}"
        export MINGW_ARCH="x86_64"
        export MINGW_DISABLE_ASM=""
        export MINGW_HOST="x86_64-w64-mingw32"
    fi

    if [ "$HOST_OS" == "linux" ]; then
        export CROSS_COMPILE="$MINGW_PREFIX"
        export CC="$MINGW_CC"
        export CXX="$MINGW_CXX"
        export LD="$MINGW_CC"
        export AR="llvm-ar"
        export RANLIB="llvm-ranlib"
        export NM="llvm-nm"
        export STRIP="llvm-strip"
        export MINGW_CRT_FLAGS=""
        if [ "${RDEV_MINGW_CRT:-}" != "msvcrt" ] && printf 'int main(void){return 0;}\n' | "$MINGW_CC" -x c - -mcrtdll=msvcrt-os -c -o /tmp/rdev-mingw-crt-test.o >/dev/null 2>&1; then
            export MINGW_CRT_FLAGS="-mcrtdll=msvcrt-os"
            rm -f /tmp/rdev-mingw-crt-test.o
        fi
        export FFMPEG_EXTRA_ARGS="--arch=$MINGW_ARCH --target-os=mingw64 \
            --enable-cross-compile $MINGW_DISABLE_ASM --disable-nvenc --disable-ffnvcodec \
            --enable-mediafoundation --enable-d3d11va --pkg-config=pkg-config \
            --cc=$MINGW_CC --cxx=$MINGW_CXX --ld=$MINGW_CC \
            --ar=llvm-ar --ranlib=llvm-ranlib --nm=llvm-nm --strip=llvm-strip"
        export FFMPEG_CFLAGS="-I$DIST/include $MINGW_CRT_FLAGS"
        export FFMPEG_LIBRARY_PATH="-L$DIST/lib $MINGW_CRT_FLAGS"
    elif [ "$TARGET_ENV" != "msvc" ]; then
        export CC="clang"
        export CXX="clang++"
        export LD="clang"
        export AR="llvm-ar"
        export RANLIB="llvm-ranlib"
        export NM="llvm-nm"
        export STRIP="llvm-strip"
        export FFMPEG_LLVM_ARGS="--cc=clang --cxx=clang++ --ld=clang \
            --ar=llvm-ar --ranlib=llvm-ranlib --nm=llvm-nm --strip=llvm-strip"
        if [ "$TARGET_ARCH" == "aarch64" ]; then
            export FFMPEG_EXTRA_ARGS="--arch=aarch64 --target-os=win64 \
                --disable-asm --disable-nvenc --disable-ffnvcodec \
                --enable-mediafoundation --enable-d3d11va $FFMPEG_LLVM_ARGS"
            export X264_EXTRA_ARGS="--host=aarch64-w64-mingw32 --disable-asm"
        else
            export FFMPEG_EXTRA_ARGS="--arch=x86_64 --target-os=mingw64 \
                --disable-nvenc --disable-ffnvcodec \
                --enable-mediafoundation --pkg-config=pkg-config --enable-d3d11va \
                $FFMPEG_LLVM_ARGS"
        fi
        export FFMPEG_CFLAGS="-I$DIST/include"
        export FFMPEG_LIBRARY_PATH="-L$DIST/lib"
    elif [ "$TARGET_ARCH" == "aarch64" ]; then
        export CC="cl"
        export FFMPEG_EXTRA_ARGS="--toolchain=msvc --arch=aarch64 --target-os=win64 \
            --enable-cross-compile --disable-asm --disable-nvenc --disable-ffnvcodec \
            --enable-mediafoundation --enable-d3d11va"
        export FFMPEG_CFLAGS="-I$DIST/include"
        export FFMPEG_LIBRARY_PATH="-LIBPATH:$DIST/lib"
        export X264_EXTRA_ARGS="--host=aarch64-w64-mingw32 --disable-asm"
    else
        export CC="cl"
        export FFMPEG_EXTRA_ARGS="--toolchain=msvc --disable-nvenc --disable-ffnvcodec \
            --enable-mediafoundation --enable-d3d11va"
        export FFMPEG_CFLAGS="-I$DIST/include"
        export FFMPEG_LIBRARY_PATH="-LIBPATH:$DIST/lib"
    fi
else
    export FFMPEG_CFLAGS="-I$DIST/include"
    export FFMPEG_LIBRARY_PATH="-L$DIST/lib"
    if [ "$TARGET_OS" == "linux" ]; then
        if [ "$ENABLE_NVENC" == "y" ]; then
            export FFMPEG_EXTRA_ARGS="--enable-nvenc \
                --enable-ffnvcodec"
        else
            export FFMPEG_EXTRA_ARGS="--disable-nvenc \
                --disable-ffnvcodec"
        fi
        if [ "$ENABLE_VULKAN_VIDEO" == "y" ]; then
            export FFMPEG_EXTRA_ARGS="$FFMPEG_EXTRA_ARGS \
                --enable-vulkan"
        fi
        if [ "$ENABLE_VAAPI" == "y" ]; then
            export FFMPEG_EXTRA_ARGS="$FFMPEG_EXTRA_ARGS \
                --enable-vaapi \
                --enable-libdrm \
                --enable-xlib"
        else
            export FFMPEG_EXTRA_ARGS="$FFMPEG_EXTRA_ARGS \
                --disable-vaapi"
        fi
    fi
    if [ "$TARGET_OS" == "macos" ]; then
        export FFMPEG_EXTRA_ARGS="--enable-videotoolbox"
        if [ -n "${TARGET_ARCH:-}" ]; then
            export MACOS_CLANG_ARCH="$TARGET_ARCH"
            if [ "$TARGET_ARCH" == "aarch64" ]; then
                export MACOS_CLANG_ARCH="arm64"
            fi
            export FFMPEG_CFLAGS="$FFMPEG_CFLAGS -arch $MACOS_CLANG_ARCH"
            export FFMPEG_LIBRARY_PATH="$FFMPEG_LIBRARY_PATH -arch $MACOS_CLANG_ARCH"
            if [ "$TARGET_ARCH" == "x86_64" ]; then
                export FFMPEG_EXTRA_ARGS="$FFMPEG_EXTRA_ARGS --arch=x86_64 --target-os=darwin"
                export X264_EXTRA_ARGS="--host=x86_64-apple-darwin --extra-cflags=-arch\ x86_64 --extra-ldflags=-arch\ x86_64"
            elif [ "$TARGET_ARCH" == "aarch64" ]; then
                export FFMPEG_EXTRA_ARGS="$FFMPEG_EXTRA_ARGS --arch=aarch64 --target-os=darwin"
                export X264_EXTRA_ARGS="--host=aarch64-apple-darwin --extra-cflags=-arch\ arm64 --extra-ldflags=-arch\ arm64"
            fi
        fi
    fi
fi

if [ "$ENABLE_LIBNPP" == "y" ]; then
    export FFMPEG_EXTRA_ARGS="$FFMPEG_EXTRA_ARGS --enable-libnpp --enable-nonfree"
fi

if [ "$TARGET_OS" == "windows" ] && [ "$HOST_OS" == "linux" ]; then
    export X264_EXTRA_ARGS="--host=$MINGW_HOST --disable-cli"
    if [ "$TARGET_ARCH" == "aarch64" ]; then
        export X264_EXTRA_ARGS="$X264_EXTRA_ARGS --disable-asm"
    fi
    if [ -n "${MINGW_CRT_FLAGS:-}" ]; then
        export X264_EXTRA_ARGS="$X264_EXTRA_ARGS --extra-cflags=$MINGW_CRT_FLAGS --extra-ldflags=$MINGW_CRT_FLAGS"
    fi
fi
if [ "${RDEV_DISABLE_ASM:-n}" == "y" ]; then
    export FFMPEG_EXTRA_ARGS="$FFMPEG_EXTRA_ARGS --disable-asm"
    export X264_EXTRA_ARGS="${X264_EXTRA_ARGS:-} --disable-asm"
fi
./x264.sh
if [ "$TARGET_OS" == "windows" ] && [ "$HOST_OS" == "windows" ]; then
    if [ -f "$DIST/lib/libx264.lib" ]; then
        cp "$DIST/lib/libx264.lib" "$DIST/lib/x264.lib"
    fi
fi
if [ "$TARGET_OS" == "linux" ]; then
    if [ "$ENABLE_NVENC" == "y" ] || [ "$ENABLE_VULKAN_VIDEO" == "y" ]; then
        ./nv-codec-headers.sh
    fi
    if [ "$ENABLE_VAAPI" == "y" ]; then
        ./libva.sh
    fi
fi
./ffmpeg.sh

if [ "$TARGET_OS" == "windows" ] && [ "$HOST_OS" == "windows" ]; then
    cd "$DIST/lib"
    for l in *.a; do
        [ -e "$l" ] || continue
        d=${l#lib}
        cp "$l" "${d%.a}.lib"
    done
    if [ -f libx264.lib ]; then
        cp libx264.lib x264.lib
    fi
fi
