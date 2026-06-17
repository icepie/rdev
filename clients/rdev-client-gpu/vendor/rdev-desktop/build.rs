use std::env;
use std::path::{Path, PathBuf};
use std::process::Command;

fn bash_command() -> Command {
    #[cfg(target_os = "windows")]
    {
        if let Ok(msys2_bash) = env::var("RDEV_DESKTOP_MSYS2_BASH") {
            let msys2_bash = Path::new(&msys2_bash);
            if msys2_bash.exists() {
                return Command::new(msys2_bash);
            }
        }
        let msys2_bash = Path::new(r"C:\msys64\usr\bin\bash.exe");
        if msys2_bash.exists() {
            return Command::new(msys2_bash);
        }
    }
    Command::new("bash")
}

fn shell_path(path: &Path) -> String {
    let path = path.to_string_lossy();
    #[cfg(target_os = "windows")]
    {
        path.strip_prefix(r"\\?\").unwrap_or(&path).replace('\\', "/")
    }
    #[cfg(not(target_os = "windows"))]
    {
        path.into_owned()
    }
}

fn host_tool_path(path: PathBuf) -> PathBuf {
    #[cfg(target_os = "windows")]
    {
        let path = path.to_string_lossy();
        return PathBuf::from(path.strip_prefix(r"\\?\").unwrap_or(&path).to_string());
    }
    #[cfg(not(target_os = "windows"))]
    {
        path
    }
}

fn build_ffmpeg(dist_dir: &Path, enable_libnpp: bool, enable_nvenc: bool, enable_vulkan_video: bool) {
    if dist_dir.join("include/libavcodec/avcodec.h").exists() && dist_dir.join("lib/libavcodec.a").exists() {
        return;
    }

    let dist_env = shell_path(dist_dir);
    let status = bash_command()
        .arg(Path::new("build.sh"))
        .current_dir("deps")
        .env("DIST", dist_env)
        .env("ENABLE_LIBNPP", if enable_libnpp { "y" } else { "n" })
        .env("ENABLE_NVENC", if enable_nvenc { "y" } else { "n" })
        .env("ENABLE_VULKAN_VIDEO", if enable_vulkan_video { "y" } else { "n" })
        .env("ENABLE_VAAPI", if env::var("CARGO_FEATURE_VAAPI").is_ok() { "y" } else { "n" })
        .status()
        .expect("failed to run deps/build.sh");
    if !status.success() {
        panic!("failed to build vendored FFmpeg/x264 dependencies");
    }
}

fn main() {
    let target_os = env::var("CARGO_CFG_TARGET_OS").unwrap();
    let target_arch = env::var("CARGO_CFG_TARGET_ARCH").unwrap_or_default();
    let vaapi_enabled = env::var("CARGO_FEATURE_VAAPI").is_ok();
    let nvenc_enabled = env::var("CARGO_FEATURE_NVENC").is_ok();
    let vulkan_video_enabled = env::var("CARGO_FEATURE_VULKAN_VIDEO").is_ok();
    let ffmpeg_system = env::var("CARGO_FEATURE_FFMPEG_SYSTEM").is_ok();

    let deps_dir = Path::new("deps").canonicalize().expect("deps directory exists");
    let dist_dir = deps_dir.join({
        let mut name = format!("dist_{}", target_os);
        if target_os == "windows" && !target_arch.is_empty() {
            name.push('_');
            name.push_str(&target_arch);
        }
        if nvenc_enabled {
            name.push_str("_nvenc");
        }
        if target_os == "linux" && vaapi_enabled {
            name.push_str("_vaapi");
        }
        if target_os == "linux" && vulkan_video_enabled {
            name.push_str("_vulkan_video");
        }
        name
    });

    let enable_libnpp = env::var("I_AM_BUILDING_THIS_AT_HOME_AND_WANT_LIBNPP").map_or(false, |v| {
        ["y", "yes", "true", "1"].contains(&v.to_lowercase().as_str())
    });

    if !ffmpeg_system {
        build_ffmpeg(&dist_dir, enable_libnpp, nvenc_enabled, target_os == "linux" && vulkan_video_enabled);
    }

    println!("cargo:rerun-if-changed=deps/build.sh");
    println!("cargo:rerun-if-changed=deps/download.sh");
    println!("cargo:rerun-if-changed=deps/ffmpeg.sh");
    println!("cargo:rerun-if-changed=deps/x264.sh");
    println!("cargo:rerun-if-changed=deps/nv-codec-headers.sh");
    println!("cargo:rerun-if-changed=deps/libva.sh");
    println!("cargo:rerun-if-changed=deps/awk.patch");
    println!("cargo:rerun-if-changed=deps/command_limit.patch");
    println!("cargo:rerun-if-changed=lib/encode_video.c");
    println!("cargo:rerun-if-changed=lib/error.h");
    println!("cargo:rerun-if-changed=lib/error.c");
    println!("cargo:rerun-if-changed=lib/log.h");
    println!("cargo:rerun-if-changed=lib/log.c");

    let mut cc_video = cc::Build::new();
    cc_video.file("lib/encode_video.c");
    if !ffmpeg_system {
        cc_video.include(host_tool_path(dist_dir.join("include")));
    }
    if nvenc_enabled {
        cc_video.define("HAS_NVENC", None);
    }
    if target_os == "linux" && vaapi_enabled {
        cc_video.define("HAS_VAAPI", None);
    }
    if target_os == "linux" && vulkan_video_enabled {
        cc_video.define("HAS_VULKAN_VIDEO", None);
    }
    if target_os == "macos" {
        cc_video.define("HAS_VIDEOTOOLBOX", None);
    }
    if target_os == "windows" {
        cc_video.define("HAS_MEDIAFOUNDATION", None);
        cc_video.flag_if_supported("-mcrtdll=msvcrt-os");
    }
    if enable_libnpp {
        cc_video.define("HAS_LIBNPP", None);
    }
    cc_video.compile("video");

    let mut cc_error = cc::Build::new();
    cc_error.file("lib/error.c");
    if target_os == "windows" {
        cc_error.flag_if_supported("-mcrtdll=msvcrt-os");
    }
    cc_error.compile("error");

    let mut cc_log = cc::Build::new();
    cc_log.file("lib/log.c");
    if target_os == "windows" {
        cc_log.flag_if_supported("-mcrtdll=msvcrt-os");
    }
    cc_log.compile("log");

    let link_kind = if target_os == "windows" || ffmpeg_system { "dylib" } else { "static" };
    for lib in ["avdevice", "avformat", "avfilter", "avcodec", "swresample", "swscale", "avutil", "x264"] {
        println!("cargo:rustc-link-lib={}={}", link_kind, lib);
    }
    if !ffmpeg_system {
        println!("cargo:rustc-link-search={}", host_tool_path(dist_dir.join("lib")).display());
    }

    if target_os == "linux" {
        linux(vaapi_enabled, vulkan_video_enabled);
    }
    if target_os == "macos" {
        println!("cargo:rustc-link-lib=framework=VideoToolbox");
        println!("cargo:rustc-link-lib=framework=CoreMedia");
    }
    if target_os == "windows" {
        println!("cargo:rustc-link-lib=dylib=mfplat");
        println!("cargo:rustc-link-lib=dylib=mfuuid");
        println!("cargo:rustc-link-lib=dylib=ole32");
        println!("cargo:rustc-link-lib=dylib=strmiids");
        println!("cargo:rustc-link-lib=dylib=vfw32");
        println!("cargo:rustc-link-lib=dylib=shlwapi");
        println!("cargo:rustc-link-lib=dylib=bcrypt");
        println!("cargo:rustc-link-lib=dylib=gdi32");
        println!("cargo:rustc-link-lib=dylib=user32");
        if target_arch == "x86_64" {
            if let Ok(winpty_lib_dir) = env::var("RDEV_DESKTOP_WINPTY_LIB_DIR") {
                println!("cargo:rustc-link-search=native={winpty_lib_dir}");
            }
        }
    }
}

fn linux(vaapi_enabled: bool, vulkan_video_enabled: bool) {
    println!("cargo:rerun-if-changed=lib/linux/uinput.c");
    println!("cargo:rerun-if-changed=lib/linux/xcapture.c");
    println!("cargo:rerun-if-changed=lib/linux/xhelper.c");
    println!("cargo:rerun-if-changed=lib/linux/xhelper.h");
    println!("cargo:rerun-if-changed=lib/linux/kms_egl.c");
    println!("cargo:rerun-if-changed=lib/linux/kms_egl.h");

    cc::Build::new()
        .file("lib/linux/uinput.c")
        .file("lib/linux/xcapture.c")
        .file("lib/linux/xhelper.c")
        .file("lib/linux/kms_egl.c")
        .compile("linux");

    println!("cargo:rustc-link-lib=X11");
    println!("cargo:rustc-link-lib=Xext");
    println!("cargo:rustc-link-lib=Xrandr");
    println!("cargo:rustc-link-lib=Xfixes");
    println!("cargo:rustc-link-lib=Xcomposite");
    println!("cargo:rustc-link-lib=Xi");
    if vaapi_enabled {
        let va_link_kind = if env::var("CARGO_FEATURE_VA_STATIC").is_ok() { "static" } else { "dylib" };
        println!("cargo:rustc-link-lib={}=va", va_link_kind);
        println!("cargo:rustc-link-lib={}=va-drm", va_link_kind);
        println!("cargo:rustc-link-lib={}=va-x11", va_link_kind);
    }
    if vulkan_video_enabled {
        println!("cargo:rustc-link-lib=dylib=vulkan");
    }
    println!("cargo:rustc-link-lib=drm");
    println!("cargo:rustc-link-lib=epoxy");
    println!("cargo:rustc-link-lib=xcb-dri3");
    println!("cargo:rustc-link-lib=X11-xcb");
    println!("cargo:rustc-link-lib=xcb");
}
