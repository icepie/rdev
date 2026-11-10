use std::ffi::{CStr, CString};
use std::os::raw::c_char;
use std::panic::{catch_unwind, AssertUnwindSafe};
use std::sync::Mutex;
#[cfg(feature = "cua-native")]
use std::sync::OnceLock;

use crate::{Request, Session};

struct Handle {
    session: Mutex<Session>,
}

#[cfg(feature = "cua-native")]
fn runtime() -> Result<&'static tokio::runtime::Runtime, String> {
    static RUNTIME: OnceLock<Result<tokio::runtime::Runtime, String>> = OnceLock::new();
    RUNTIME
        .get_or_init(|| {
            tokio::runtime::Builder::new_multi_thread()
                .enable_all()
                .build()
                .map_err(|error| error.to_string())
        })
        .as_ref()
        .map_err(Clone::clone)
}

fn encode(value: serde_json::Value) -> *mut c_char {
    CString::new(value.to_string())
        .map(CString::into_raw)
        .unwrap_or(std::ptr::null_mut())
}

fn error_json(message: impl Into<String>) -> *mut c_char {
    encode(serde_json::json!({ "error": message.into() }))
}

#[no_mangle]
pub extern "C" fn rdev_cua_session_new() -> *mut std::ffi::c_void {
    catch_unwind(|| {
        Box::into_raw(Box::new(Handle {
            session: Mutex::new(Session::new()),
        }))
        .cast()
    })
    .unwrap_or(std::ptr::null_mut())
}

#[no_mangle]
pub unsafe extern "C" fn rdev_cua_session_request(
    session: *mut std::ffi::c_void,
    request_json: *const c_char,
) -> *mut c_char {
    catch_unwind(AssertUnwindSafe(|| {
        if session.is_null() || request_json.is_null() {
            return error_json("session and request must be non-null");
        }
        let text = match CStr::from_ptr(request_json).to_str() {
            Ok(text) => text,
            Err(error) => return error_json(format!("request is not UTF-8: {error}")),
        };
        let request: Request = match serde_json::from_str(text) {
            Ok(request) => request,
            Err(error) => return error_json(format!("invalid Cua request: {error}")),
        };
        let handle = &*(session.cast::<Handle>());
        let mut session = match handle.session.lock() {
            Ok(session) => session,
            Err(_) => return error_json("Cua session lock is poisoned"),
        };
        #[cfg(feature = "cua-native")]
        let result = match runtime() {
            Ok(runtime) => runtime.block_on(session.request(request)),
            Err(error) => return error_json(error),
        };
        #[cfg(not(feature = "cua-native"))]
        let result = futures_unavailable(session.request(request));
        match result {
            Ok(result) => encode(serde_json::json!({ "result": result })),
            Err(error) => error_json(error.to_string()),
        }
    }))
    .unwrap_or_else(|_| error_json("panic in Cua bridge"))
}

#[no_mangle]
pub extern "C" fn rdev_cua_supported() -> bool {
    catch_unwind(crate::supported).unwrap_or(false)
}

#[cfg(not(feature = "cua-native"))]
fn futures_unavailable<F>(_future: F) -> Result<serde_json::Value, anyhow::Error> {
    Err(anyhow::anyhow!("native CUA is unsupported on this build"))
}

#[no_mangle]
pub unsafe extern "C" fn rdev_cua_session_free(session: *mut std::ffi::c_void) {
    let _ = catch_unwind(AssertUnwindSafe(|| {
        if !session.is_null() {
            drop(Box::from_raw(session.cast::<Handle>()));
        }
    }));
}

#[no_mangle]
pub unsafe extern "C" fn rdev_cua_string_free(value: *mut c_char) {
    let _ = catch_unwind(AssertUnwindSafe(|| {
        if !value.is_null() {
            drop(CString::from_raw(value));
        }
    }));
}
