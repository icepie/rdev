use anyhow::{anyhow, Context, Result};
use libloading::Library;
use std::{
    ffi::{c_void, OsStr},
    fs::File,
    os::windows::{ffi::OsStrExt, io::FromRawHandle},
    path::PathBuf,
    ptr,
    sync::{Arc, Mutex},
};
use windows_sys::Win32::{
    Foundation::{CloseHandle, GetLastError, HANDLE, INVALID_HANDLE_VALUE},
    Storage::FileSystem::{
        CreateFileW, FILE_ATTRIBUTE_NORMAL, FILE_GENERIC_READ, FILE_GENERIC_WRITE, FILE_SHARE_NONE,
        OPEN_EXISTING,
    },
    System::{
        SystemInformation::{GetVersionExW, OSVERSIONINFOW},
        Threading::{GetExitCodeProcess, TerminateProcess},
    },
};

const STILL_ACTIVE: u32 = 259;

pub fn is_legacy_windows() -> bool {
    let version = rtl_windows_version().or_else(get_version_ex_windows_version);
    version.is_some_and(|(major, _minor)| major < 10)
}

fn rtl_windows_version() -> Option<(u32, u32)> {
    type RtlGetVersion = unsafe extern "system" fn(*mut OSVERSIONINFOW) -> i32;

    let lib = unsafe { Library::new("ntdll.dll") }.ok()?;
    let rtl_get_version = unsafe { *lib.get::<RtlGetVersion>(b"RtlGetVersion\0").ok()? };
    let mut info = unsafe { std::mem::zeroed::<OSVERSIONINFOW>() };
    info.dwOSVersionInfoSize = std::mem::size_of::<OSVERSIONINFOW>() as u32;
    let status = unsafe { rtl_get_version(&mut info) };
    if status >= 0 {
        Some((info.dwMajorVersion, info.dwMinorVersion))
    } else {
        None
    }
}

fn get_version_ex_windows_version() -> Option<(u32, u32)> {
    unsafe {
        let mut info = std::mem::zeroed::<OSVERSIONINFOW>();
        info.dwOSVersionInfoSize = std::mem::size_of::<OSVERSIONINFOW>() as u32;
        if GetVersionExW(&mut info) == 0 {
            None
        } else {
            Some((info.dwMajorVersion, info.dwMinorVersion))
        }
    }
}

type WinptyConfig = c_void;
type Winpty = c_void;
type WinptySpawnConfig = c_void;
type WinptyError = c_void;

type WinptyErrorPtr = *mut WinptyError;

type WinptyConfigNew = unsafe extern "C" fn(u64, *mut WinptyErrorPtr) -> *mut WinptyConfig;
type WinptyConfigFree = unsafe extern "C" fn(*mut WinptyConfig);
type WinptyConfigSetInitialSize = unsafe extern "C" fn(*mut WinptyConfig, i32, i32);
type WinptyConfigSetMouseMode = unsafe extern "C" fn(*mut WinptyConfig, i32);
type WinptyConfigSetAgentTimeout = unsafe extern "C" fn(*mut WinptyConfig, u32);
type WinptyOpen = unsafe extern "C" fn(*const WinptyConfig, *mut WinptyErrorPtr) -> *mut Winpty;
type WinptyConName = unsafe extern "C" fn(*mut Winpty) -> *const u16;
type WinptySpawnConfigNew = unsafe extern "C" fn(
    u64,
    *const u16,
    *const u16,
    *const u16,
    *const u16,
    *mut WinptyErrorPtr,
) -> *mut WinptySpawnConfig;
type WinptySpawnConfigFree = unsafe extern "C" fn(*mut WinptySpawnConfig);
type WinptySpawn = unsafe extern "C" fn(
    *mut Winpty,
    *const WinptySpawnConfig,
    *mut HANDLE,
    *mut HANDLE,
    *mut u32,
    *mut WinptyErrorPtr,
) -> bool;
type WinptySetSize = unsafe extern "C" fn(*mut Winpty, i32, i32, *mut WinptyErrorPtr) -> bool;
type WinptyFree = unsafe extern "C" fn(*mut Winpty);
type WinptyErrorMsg = unsafe extern "C" fn(WinptyErrorPtr) -> *const u16;
type WinptyErrorFree = unsafe extern "C" fn(WinptyErrorPtr);

fn load_winpty_library() -> Result<Library> {
    let mut candidates = Vec::new();
    if let Ok(dir) = std::env::var("RDEV_WINPTY_DIR") {
        candidates.push(PathBuf::from(dir).join("winpty.dll"));
    }
    if let Ok(dir) = std::env::var("WINPTY_DIR") {
        candidates.push(PathBuf::from(dir).join("winpty.dll"));
    }
    if let Ok(exe) = std::env::current_exe() {
        if let Some(dir) = exe.parent() {
            candidates.push(dir.join("winpty.dll"));
        }
    }

    let mut errors = Vec::new();
    for path in candidates {
        if !path.exists() {
            continue;
        }
        match unsafe { Library::new(&path) } {
            Ok(lib) => return Ok(lib),
            Err(err) => errors.push(format!("{}: {err}", path.display())),
        }
    }

    unsafe { Library::new("winpty.dll") }.with_context(|| {
        if errors.is_empty() {
            "load winpty.dll from RDEV_WINPTY_DIR, exe directory, or PATH".to_string()
        } else {
            format!(
                "load winpty.dll from RDEV_WINPTY_DIR, exe directory, or PATH; tried {}",
                errors.join("; ")
            )
        }
    })
}

struct WinptyApi {
    _lib: Library,
    config_new: WinptyConfigNew,
    config_free: WinptyConfigFree,
    config_set_initial_size: WinptyConfigSetInitialSize,
    config_set_mouse_mode: WinptyConfigSetMouseMode,
    config_set_agent_timeout: WinptyConfigSetAgentTimeout,
    open: WinptyOpen,
    conin_name: WinptyConName,
    conout_name: WinptyConName,
    spawn_config_new: WinptySpawnConfigNew,
    spawn_config_free: WinptySpawnConfigFree,
    spawn: WinptySpawn,
    set_size: WinptySetSize,
    free: WinptyFree,
    error_msg: WinptyErrorMsg,
    error_free: WinptyErrorFree,
}

impl WinptyApi {
    fn load() -> Result<Arc<Self>> {
        let lib = load_winpty_library()?;
        unsafe fn sym<T: Copy>(lib: &Library, name: &[u8]) -> Result<T> {
            Ok(*lib
                .get::<T>(name)
                .with_context(|| format!("load winpty symbol {}", String::from_utf8_lossy(name)))?)
        }
        let api = unsafe {
            Self {
                config_new: sym(&lib, b"winpty_config_new\0")?,
                config_free: sym(&lib, b"winpty_config_free\0")?,
                config_set_initial_size: sym(&lib, b"winpty_config_set_initial_size\0")?,
                config_set_mouse_mode: sym(&lib, b"winpty_config_set_mouse_mode\0")?,
                config_set_agent_timeout: sym(&lib, b"winpty_config_set_agent_timeout\0")?,
                open: sym(&lib, b"winpty_open\0")?,
                conin_name: sym(&lib, b"winpty_conin_name\0")?,
                conout_name: sym(&lib, b"winpty_conout_name\0")?,
                spawn_config_new: sym(&lib, b"winpty_spawn_config_new\0")?,
                spawn_config_free: sym(&lib, b"winpty_spawn_config_free\0")?,
                spawn: sym(&lib, b"winpty_spawn\0")?,
                set_size: sym(&lib, b"winpty_set_size\0")?,
                free: sym(&lib, b"winpty_free\0")?,
                error_msg: sym(&lib, b"winpty_error_msg\0")?,
                error_free: sym(&lib, b"winpty_error_free\0")?,
                _lib: lib,
            }
        };
        Ok(Arc::new(api))
    }

    unsafe fn take_error(&self, err: &mut WinptyErrorPtr) -> String {
        if err.is_null() {
            return "unknown winpty error".into();
        }
        let msg = wide_ptr_to_string((self.error_msg)(*err));
        (self.error_free)(*err);
        *err = ptr::null_mut();
        msg
    }
}

pub struct WinptySession {
    inner: Arc<WinptyInner>,
    input: Mutex<File>,
    output: Mutex<Option<File>>,
}

struct WinptyInner {
    api: Arc<WinptyApi>,
    pty: *mut Winpty,
    process: HANDLE,
}

unsafe impl Send for WinptyInner {}
unsafe impl Sync for WinptyInner {}
unsafe impl Send for WinptySession {}
unsafe impl Sync for WinptySession {}

impl Drop for WinptyInner {
    fn drop(&mut self) {
        unsafe {
            (self.api.free)(self.pty);
            if !self.process.is_null() {
                CloseHandle(self.process);
            }
        }
    }
}

impl WinptySession {
    pub fn take_output(&self) -> Option<File> {
        self.output.lock().unwrap().take()
    }

    pub fn write(&self, data: &[u8]) -> std::io::Result<()> {
        use std::io::Write;
        self.input.lock().unwrap().write_all(data)
    }

    pub fn resize(&self, cols: i32, rows: i32) -> Result<()> {
        let mut err = ptr::null_mut();
        let ok = unsafe { (self.inner.api.set_size)(self.inner.pty, cols, rows, &mut err) };
        if ok {
            Ok(())
        } else {
            Err(anyhow!(unsafe { self.inner.api.take_error(&mut err) }))
        }
    }

    pub fn exit_status(&self) -> Result<Option<i32>> {
        let mut code = 0u32;
        let ok = unsafe { GetExitCodeProcess(self.inner.process, &mut code) };
        if ok == 0 {
            return Err(anyhow!("GetExitCodeProcess failed: {}", unsafe {
                GetLastError()
            }));
        }
        if code == STILL_ACTIVE {
            Ok(None)
        } else {
            Ok(Some(code as i32))
        }
    }

    pub fn terminate(&self) {
        unsafe {
            let _ = TerminateProcess(self.inner.process, 1);
        }
    }
}

pub fn spawn(shell: &str, command: Option<&str>, cols: i32, rows: i32) -> Result<WinptySession> {
    const WINPTY_FLAG_COLOR_ESCAPES: u64 = 0x4;
    const WINPTY_SPAWN_FLAG_AUTO_SHUTDOWN: u64 = 0x1;

    let api = WinptyApi::load()?;
    let mut err = ptr::null_mut();
    let cfg = unsafe { (api.config_new)(WINPTY_FLAG_COLOR_ESCAPES, &mut err) };
    if cfg.is_null() {
        return Err(anyhow!(unsafe { api.take_error(&mut err) }));
    }
    unsafe {
        (api.config_set_initial_size)(cfg, cols.max(1), rows.max(1));
        (api.config_set_mouse_mode)(cfg, 0);
        (api.config_set_agent_timeout)(cfg, 10000);
    }

    let pty = unsafe { (api.open)(cfg, &mut err) };
    unsafe { (api.config_free)(cfg) };
    if pty.is_null() {
        return Err(anyhow!(unsafe { api.take_error(&mut err) }));
    }

    let conin = unsafe { open_pipe((api.conin_name)(pty), FILE_GENERIC_WRITE) }
        .context("open winpty input pipe")?;
    let conout = unsafe { open_pipe((api.conout_name)(pty), FILE_GENERIC_READ) }
        .context("open winpty output pipe")?;

    let app = wide_null(shell);
    let cmdline = command.map(|cmd| wide_null(&format!("/c {cmd}")));
    let spawn_cfg = unsafe {
        (api.spawn_config_new)(
            WINPTY_SPAWN_FLAG_AUTO_SHUTDOWN,
            app.as_ptr(),
            cmdline.as_ref().map_or(ptr::null(), |v| v.as_ptr()),
            ptr::null(),
            ptr::null(),
            &mut err,
        )
    };
    if spawn_cfg.is_null() {
        unsafe { (api.free)(pty) };
        return Err(anyhow!(unsafe { api.take_error(&mut err) }));
    }

    let mut process = ptr::null_mut();
    let mut thread = ptr::null_mut();
    let mut create_process_error = 0u32;
    let ok = unsafe {
        (api.spawn)(
            pty,
            spawn_cfg,
            &mut process,
            &mut thread,
            &mut create_process_error,
            &mut err,
        )
    };
    unsafe { (api.spawn_config_free)(spawn_cfg) };
    if !thread.is_null() {
        unsafe { CloseHandle(thread) };
    }
    if !ok {
        unsafe { (api.free)(pty) };
        return Err(anyhow!(
            "{}; CreateProcess error={create_process_error}",
            unsafe { api.take_error(&mut err) }
        ));
    }

    Ok(WinptySession {
        inner: Arc::new(WinptyInner { api, pty, process }),
        input: Mutex::new(conin),
        output: Mutex::new(Some(conout)),
    })
}

unsafe fn open_pipe(name: *const u16, access: u32) -> Result<File> {
    if name.is_null() {
        return Err(anyhow!("winpty pipe name is null"));
    }
    let handle = CreateFileW(
        name,
        access,
        FILE_SHARE_NONE,
        ptr::null(),
        OPEN_EXISTING,
        FILE_ATTRIBUTE_NORMAL,
        ptr::null_mut(),
    );
    if handle == INVALID_HANDLE_VALUE {
        Err(anyhow!("CreateFileW failed: {}", GetLastError()))
    } else {
        Ok(File::from_raw_handle(handle as _))
    }
}

fn wide_null(value: &str) -> Vec<u16> {
    OsStr::new(value).encode_wide().chain([0]).collect()
}

unsafe fn wide_ptr_to_string(ptr: *const u16) -> String {
    if ptr.is_null() {
        return "unknown winpty error".into();
    }
    let mut len = 0;
    while *ptr.add(len) != 0 {
        len += 1;
    }
    String::from_utf16_lossy(std::slice::from_raw_parts(ptr, len))
}
