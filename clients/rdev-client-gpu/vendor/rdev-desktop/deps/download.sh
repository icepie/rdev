#!/usr/bin/env bash

set -ex

clone_repo() {
    local dir="$1"
    local branch="$2"
    shift 2

    if [ -d "$dir" ]; then
        if git -C "$dir" rev-parse --is-inside-work-tree >/dev/null 2>&1 && \
           find "$dir" -mindepth 1 -maxdepth 2 ! -path "$dir/.git" ! -path "$dir/.git/*" | grep -q .; then
            return 0
        fi
        rm -rf "$dir"
    fi

    local url attempt
    for url in "$@"; do
        for attempt in 1 2 3; do
            rm -rf "$dir"
            if [ -n "$branch" ]; then
                git clone --depth 1 -b "$branch" "$url" "$dir" && return 0
            else
                git clone --depth 1 "$url" "$dir" && return 0
            fi
            sleep $((attempt * 5))
        done
    done
    return 1
}

clone_repo x264 stable \
    https://code.videolan.org/videolan/x264.git \
    https://github.com/videolan/x264.git
clone_repo ffmpeg n8.0 \
    https://git.ffmpeg.org/ffmpeg.git \
    https://github.com/FFmpeg/FFmpeg.git
if [ "$TARGET_OS" == "linux" ]; then
    if [ "${ENABLE_NVENC:-n}" = "y" ] || [ "${ENABLE_VULKAN_VIDEO:-n}" = "y" ]; then
        clone_repo nv-codec-headers "n13.0.19.0" \
            https://git.videolan.org/git/ffmpeg/nv-codec-headers.git \
            https://github.com/FFmpeg/nv-codec-headers.git
    fi
    if [ "${ENABLE_VAAPI:-n}" = "y" ]; then
        clone_repo libva 2.22.0 https://github.com/intel/libva
    fi
fi
if [ "$TARGET_OS" == "windows" ] && [ "${ENABLE_NVENC:-n}" = "y" ]; then
    clone_repo nv-codec-headers "n13.0.19.0" \
        https://git.videolan.org/git/ffmpeg/nv-codec-headers.git \
        https://github.com/FFmpeg/nv-codec-headers.git
fi

if [ "$TARGET_OS" == "windows" ] && [ "$HOST_OS" == "windows" ]; then
    cd ffmpeg
    git apply ../command_limit.patch || true
    git apply ../awk.patch || {
        cat > msvc_dep.awk <<'EOF'
/including/ { sub(/^.*file: */, ""); gsub(/\\/, "/"); if (!match($0, / /)) print target ":", $0 }
EOF
        python3 - <<'PY'
from pathlib import Path

path = Path("configure")
text = path.read_text()
old = r"""_DEPCMD='$(DEP$(1)) $(DEP$(1)FLAGS) $($(1)DEP_FLAGS) $< 2>&1 | awk '\''/including/ { sub(/^.*file: */, ""); gsub(/\\/, "/"); if (!match($$0, / /)) print "$@:", $$0 }'\'' > $(@:.o=.d)'"""
new = r"""_DEPCMD='$(DEP$(1)) $(DEP$(1)FLAGS) $($(1)DEP_FLAGS) $< 2>&1 | awk -v target="$@" -f ./msvc_dep.awk > $(@:.o=.d)'"""
if old in text:
    path.write_text(text.replace(old, new, 1))
elif new not in text:
    raise SystemExit("Unable to patch FFmpeg MSVC dependency command")
PY
    }
fi
