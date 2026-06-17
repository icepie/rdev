use std::env;

fn main() {
    if env::var("CARGO_FEATURE_FFMPEG_SYSTEM").is_err() {
        panic!("vendored auroraops-client requires the ffmpeg-system feature");
    }

    let target_os = env::var("CARGO_CFG_TARGET_OS").unwrap();
    let target_arch = env::var("CARGO_CFG_TARGET_ARCH").unwrap_or_default();
    let vaapi_enabled = env::var("CARGO_FEATURE_VAAPI").is_ok();
    let vulkan_video_enabled = env::var("CARGO_FEATURE_VULKAN_VIDEO").is_ok();
    let enable_libnpp = env::var("I_AM_BUILDING_THIS_AT_HOME_AND_WANT_LIBNPP").map_or(false, |v| {
        ["y", "yes", "true", "1"].contains(&v.to_lowercase().as_str())
    });

    println!("cargo:rerun-if-changed=lib/encode_video.c");
    println!("cargo:rerun-if-changed=lib/error.h");
    println!("cargo:rerun-if-changed=lib/error.c");
    println!("cargo:rerun-if-changed=lib/log.h");
    println!("cargo:rerun-if-changed=lib/log.c");

    let mut cc_video = cc::Build::new();
    cc_video.file("lib/encode_video.c");
    if ["linux", "windows"].contains(&target_os.as_str()) {
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
    }
    if enable_libnpp {
        cc_video.define("HAS_LIBNPP", None);
    }
    cc_video.compile("video");

    cc::Build::new().file("lib/error.c").compile("error");
    cc::Build::new().file("lib/log.c").compile("log");

    println!("cargo:rustc-link-lib=dylib=avdevice");
    println!("cargo:rustc-link-lib=dylib=avformat");
    println!("cargo:rustc-link-lib=dylib=avfilter");
    println!("cargo:rustc-link-lib=dylib=avcodec");
    println!("cargo:rustc-link-lib=dylib=swresample");
    println!("cargo:rustc-link-lib=dylib=swscale");
    println!("cargo:rustc-link-lib=dylib=avutil");
    println!("cargo:rustc-link-lib=dylib=x264");

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
            if let Ok(winpty_lib_dir) = env::var("AURORAOPS_WINPTY_LIB_DIR") {
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
        let va_link_kind = if env::var("CARGO_FEATURE_VA_STATIC").is_ok() {
            "static"
        } else {
            "dylib"
        };
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
