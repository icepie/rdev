use crate::config::Args;

#[cfg(target_os = "linux")]
pub fn prepare(args: &mut Args) {
    if std::env::var_os("DISPLAY").is_some() || std::env::var_os("WAYLAND_DISPLAY").is_some() {
        tracing::info!(
            "desktop environment detected: DISPLAY={:?} WAYLAND_DISPLAY={:?}",
            std::env::var("DISPLAY"),
            std::env::var("WAYLAND_DISPLAY")
        );
        return;
    }

    let wayland_detected = detect_wayland_session();
    let x11_detected = detect_x11_session();
    if x11_detected {
        return;
    }
    if wayland_detected && cfg!(feature = "embedded-rdev-desktop-wayland") {
        args.gpu_desktop_wayland = true;
        return;
    }

    if current_uid() == Some(0) && !args.gpu_desktop_kms {
        args.gpu_desktop_kms = true;
        if wayland_detected {
            tracing::warn!(
                "Wayland session detected but this package has no PipeWire support; enabling Linux KMS capture fallback for root"
            );
        } else {
            tracing::warn!(
                "no DISPLAY/WAYLAND_DISPLAY detected; enabling Linux KMS capture fallback for root"
            );
        }
    }
}

#[cfg(not(target_os = "linux"))]
pub fn prepare(_args: &mut Args) {}

#[cfg(target_os = "linux")]
fn detect_wayland_session() -> bool {
    let Ok(entries) = std::fs::read_dir("/run/user") else {
        return false;
    };
    let mut runtime_dirs = entries
        .filter_map(Result::ok)
        .map(|entry| entry.path())
        .filter(|path| path.is_dir())
        .collect::<Vec<_>>();
    runtime_dirs.sort();

    for runtime_dir in runtime_dirs {
        let Ok(entries) = std::fs::read_dir(&runtime_dir) else {
            continue;
        };
        let mut wayland_sockets = entries
            .filter_map(Result::ok)
            .map(|entry| entry.file_name().to_string_lossy().into_owned())
            .filter(|name| name.starts_with("wayland-"))
            .collect::<Vec<_>>();
        wayland_sockets.sort();
        let Some(wayland_display) = wayland_sockets.into_iter().next() else {
            continue;
        };

        std::env::set_var("XDG_RUNTIME_DIR", &runtime_dir);
        std::env::set_var("WAYLAND_DISPLAY", &wayland_display);
        let bus = runtime_dir.join("bus");
        if bus.exists() && std::env::var_os("DBUS_SESSION_BUS_ADDRESS").is_none() {
            std::env::set_var(
                "DBUS_SESSION_BUS_ADDRESS",
                format!("unix:path={}", bus.display()),
            );
        }
        tracing::info!(
            "auto-detected Wayland desktop session: XDG_RUNTIME_DIR={} WAYLAND_DISPLAY={}",
            runtime_dir.display(),
            wayland_display
        );
        return true;
    }
    false
}

#[cfg(target_os = "linux")]
fn detect_x11_session() -> bool {
    let Ok(entries) = std::fs::read_dir("/tmp/.X11-unix") else {
        return false;
    };
    let mut displays = entries
        .filter_map(Result::ok)
        .filter_map(|entry| {
            let name = entry.file_name().to_string_lossy().into_owned();
            name.strip_prefix('X')
                .filter(|display| display.chars().all(|ch| ch.is_ascii_digit()))
                .map(|display| display.to_string())
        })
        .collect::<Vec<_>>();
    displays.sort_by_key(|display| display.parse::<u16>().unwrap_or(u16::MAX));
    let Some(display) = displays.into_iter().next() else {
        return false;
    };

    let display_value = format!(":{display}");
    std::env::set_var("DISPLAY", &display_value);
    if std::env::var_os("XAUTHORITY").is_none() {
        if let Some(xauthority) = find_xauthority() {
            std::env::set_var("XAUTHORITY", &xauthority);
            tracing::info!(
                "auto-detected X11 desktop session: DISPLAY={} XAUTHORITY={}",
                display_value,
                xauthority.display()
            );
            return true;
        }
    }
    tracing::info!("auto-detected X11 desktop session: DISPLAY={display_value}");
    true
}

#[cfg(target_os = "linux")]
fn find_xauthority() -> Option<std::path::PathBuf> {
    let mut candidates = Vec::new();
    if let Some(home) = std::env::var_os("HOME") {
        candidates.push(std::path::PathBuf::from(home).join(".Xauthority"));
    }
    candidates.push(std::path::PathBuf::from("/root/.Xauthority"));
    if let Ok(entries) = std::fs::read_dir("/home") {
        for entry in entries.filter_map(Result::ok) {
            candidates.push(entry.path().join(".Xauthority"));
        }
    }
    candidates.into_iter().find(|path| {
        std::fs::metadata(path)
            .map(|metadata| metadata.is_file() && metadata.len() > 0)
            .unwrap_or(false)
    })
}

#[cfg(target_os = "linux")]
fn current_uid() -> Option<u32> {
    let status = std::fs::read_to_string("/proc/self/status").ok()?;
    let uid_line = status.lines().find(|line| line.starts_with("Uid:"))?;
    uid_line.split_whitespace().nth(1)?.parse().ok()
}

#[cfg(all(test, target_os = "linux"))]
mod tests {
    use super::*;

    #[test]
    fn current_uid_is_available() {
        assert!(current_uid().is_some());
    }
}
