use std::ffi::CStr;
use std::os::raw::c_char;

use tracing::{debug, error, info, trace, warn};

pub fn get_log_level() -> tracing::Level {
    #[cfg(debug_assertions)]
    let mut level = tracing::Level::DEBUG;

    #[cfg(not(debug_assertions))]
    let mut level = tracing::Level::INFO;

    if let Ok(var) = std::env::var("WEYLUS_LOG_LEVEL") {
        if let Ok(parsed) = var.parse() {
            level = parsed;
        }
    }
    level
}

#[no_mangle]
fn log_error_rust(msg: *const c_char) {
    let msg = unsafe { CStr::from_ptr(msg) }.to_string_lossy();
    error!("{}", msg);
}

#[no_mangle]
fn log_debug_rust(msg: *const c_char) {
    let msg = unsafe { CStr::from_ptr(msg) }.to_string_lossy();
    debug!("{}", msg);
}

#[no_mangle]
fn log_info_rust(msg: *const c_char) {
    let msg = unsafe { CStr::from_ptr(msg) }.to_string_lossy();
    info!("{}", msg);
}

#[no_mangle]
fn log_trace_rust(msg: *const c_char) {
    let msg = unsafe { CStr::from_ptr(msg) }.to_string_lossy();
    trace!("{}", msg);
}

#[no_mangle]
fn log_warn_rust(msg: *const c_char) {
    let msg = unsafe { CStr::from_ptr(msg) }.to_string_lossy();
    warn!("{}", msg);
}
