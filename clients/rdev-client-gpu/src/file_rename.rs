//! Native no-replace rename: never emulate with a check followed by overwrite.
use std::{io, path::Path};

#[cfg(any(target_os = "linux", target_os = "macos"))]
pub fn no_replace(from: &Path, to: &Path) -> io::Result<()> {
    use std::{ffi::CString, os::unix::ffi::OsStrExt};
    let a = CString::new(from.as_os_str().as_bytes())?;
    let b = CString::new(to.as_os_str().as_bytes())?;
    #[cfg(target_os = "linux")]
    let result = unsafe {
        extern "C" {
            fn renameat2(
                a: i32,
                from: *const std::ffi::c_char,
                b: i32,
                to: *const std::ffi::c_char,
                flags: u32,
            ) -> i32;
        }
        renameat2(-100, a.as_ptr(), -100, b.as_ptr(), 1)
    };
    #[cfg(target_os = "macos")]
    let result = unsafe {
        extern "C" {
            fn renamex_np(
                from: *const std::ffi::c_char,
                to: *const std::ffi::c_char,
                flags: u32,
            ) -> i32;
        }
        renamex_np(a.as_ptr(), b.as_ptr(), 4)
    };
    if result == 0 {
        Ok(())
    } else {
        Err(io::Error::last_os_error())
    }
}
#[cfg(windows)]
pub fn no_replace(from: &Path, to: &Path) -> io::Result<()> {
    use std::os::windows::ffi::OsStrExt;
    let a: Vec<_> = from.as_os_str().encode_wide().chain(Some(0)).collect();
    let b: Vec<_> = to.as_os_str().encode_wide().chain(Some(0)).collect();
    let result =
        unsafe { windows_sys::Win32::Storage::FileSystem::MoveFileExW(a.as_ptr(), b.as_ptr(), 0) };
    if result != 0 {
        Ok(())
    } else {
        Err(io::Error::last_os_error())
    }
}
#[cfg(not(any(target_os = "linux", target_os = "macos", windows)))]
pub fn no_replace(_: &Path, _: &Path) -> io::Result<()> {
    Err(io::Error::new(
        io::ErrorKind::Unsupported,
        "atomic no-replace rename unavailable",
    ))
}
