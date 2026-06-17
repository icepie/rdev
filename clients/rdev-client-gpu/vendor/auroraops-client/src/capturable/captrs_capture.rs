use crate::capturable::{Capturable, Recorder};
use captrs::Capturer;
use std::any::Any;
use std::boxed::Box;
use std::error::Error;
use std::mem;
use std::panic::{catch_unwind, AssertUnwindSafe};
use std::ptr;
use winapi::shared::minwindef::{BOOL, DWORD, LPARAM, TRUE};
use winapi::shared::windef::{HDESK, HWND, POINT, RECT};
use winapi::um::errhandlingapi::GetLastError;
use winapi::um::wingdi::{
    BitBlt, CreateCompatibleBitmap, CreateCompatibleDC, DeleteDC, DeleteObject, GetDIBits,
    SelectObject, BITMAPINFO, BITMAPINFOHEADER, BI_RGB, CAPTUREBLT, DIB_RGB_COLORS, SRCCOPY,
};
use winapi::um::winuser::{
    BringWindowToTop, CloseDesktop, CopyIcon, DestroyIcon, DrawIconEx, EnumWindows, GetAncestor,
    GetCursorInfo, GetCursorPos, GetDC, GetIconInfo, GetWindowRect, GetWindowTextLengthW,
    GetWindowTextW, IsIconic, IsWindowVisible, LoadCursorW, OpenInputDesktop, PrintWindow,
    ReleaseDC, SetForegroundWindow, SetThreadDesktop, ShowWindow, CURSORINFO, CURSOR_SHOWING,
    DESKTOP_CREATEMENU, DESKTOP_CREATEWINDOW, DESKTOP_ENUMERATE, DESKTOP_HOOKCONTROL,
    DESKTOP_JOURNALPLAYBACK, DESKTOP_JOURNALRECORD, DESKTOP_READOBJECTS, DESKTOP_SWITCHDESKTOP,
    DESKTOP_WRITEOBJECTS, GA_ROOT, ICONINFO, IDC_ARROW, SW_RESTORE,
};

use super::Geometry;

const DI_NORMAL: u32 = 0x0003;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum WindowsCaptureSource {
    Auto,
    Dxgi,
    Gdi,
}

impl WindowsCaptureSource {
    pub fn parse(value: &str) -> Self {
        match value.trim().to_ascii_lowercase().as_str() {
            "dxgi" => Self::Dxgi,
            "gdi" => Self::Gdi,
            _ => Self::Auto,
        }
    }

    pub fn as_str(self) -> &'static str {
        match self {
            Self::Auto => "auto",
            Self::Dxgi => "dxgi",
            Self::Gdi => "gdi",
        }
    }

    fn label(self) -> &'static str {
        match self {
            Self::Auto => "AUTO",
            Self::Dxgi => "DXGI",
            Self::Gdi => "GDI",
        }
    }
}

#[derive(Clone)]
pub struct DesktopCapturable {
    id: u8,
    name: String,
    screen: RECT,
    virtual_screen: RECT,
    source: WindowsCaptureSource,
}

#[derive(Clone)]
pub struct WindowCapturable {
    hwnd: usize,
    title: String,
    rect: RECT,
    virtual_screen: RECT,
}

impl WindowCapturable {
    fn hwnd(&self) -> HWND {
        self.hwnd as HWND
    }
}

impl Capturable for WindowCapturable {
    fn as_any(&self) -> &dyn Any {
        self
    }

    fn name(&self) -> String {
        format!("Window {}", self.title)
    }

    fn before_input(&mut self) -> Result<(), Box<dyn Error>> {
        unsafe {
            let hwnd = self.hwnd();
            if hwnd.is_null() {
                return Ok(());
            }
            if IsIconic(hwnd) != 0 {
                ShowWindow(hwnd, SW_RESTORE);
            }
            BringWindowToTop(hwnd);
            SetForegroundWindow(hwnd);
        }
        Ok(())
    }

    fn recorder(&self, capture_cursor: bool) -> Result<Box<dyn Recorder>, Box<dyn Error>> {
        Ok(Box::new(WindowRecorder::new(
            self.hwnd(),
            self.rect,
            capture_cursor,
        )?))
    }

    fn geometry(&self) -> Result<Geometry, Box<dyn Error>> {
        Ok(Geometry::VirtualScreen(
            self.rect.left - self.virtual_screen.left,
            self.rect.top - self.virtual_screen.top,
            (self.rect.right - self.rect.left) as u32,
            (self.rect.bottom - self.rect.top) as u32,
            self.rect.left,
            self.rect.top,
        ))
    }
}

impl DesktopCapturable {
    pub fn new(
        id: u8,
        name: String,
        screen: RECT,
        virtual_screen: RECT,
        source: WindowsCaptureSource,
    ) -> DesktopCapturable {
        DesktopCapturable {
            id,
            name,
            screen,
            virtual_screen,
            source,
        }
    }
}

impl Capturable for DesktopCapturable {
    fn as_any(&self) -> &dyn Any {
        self
    }

    fn name(&self) -> String {
        format!("Desktop {} ({})", self.name, self.source.label())
    }
    fn before_input(&mut self) -> Result<(), Box<dyn Error>> {
        Ok(())
    }
    fn recorder(&self, capture_cursor: bool) -> Result<Box<dyn Recorder>, Box<dyn Error>> {
        match self.source {
            WindowsCaptureSource::Gdi => {
                Ok(Box::new(GdiRecorder::new(self.screen, capture_cursor)?))
            }
            WindowsCaptureSource::Auto | WindowsCaptureSource::Dxgi => {
                if capture_cursor && self.source == WindowsCaptureSource::Auto {
                    return Ok(Box::new(GdiRecorder::new(self.screen, true)?));
                }
                match CaptrsRecorder::new(self.id, self.screen) {
                    Ok(recorder) => Ok(Box::new(recorder)),
                    Err(err) => {
                        tracing::warn!(
                            "DXGI screen capture failed for {}, falling back to GDI: {}",
                            self.name,
                            err
                        );
                        Ok(Box::new(GdiRecorder::new(self.screen, false)?))
                    }
                }
            }
        }
    }
    fn geometry(&self) -> Result<Geometry, Box<dyn Error>> {
        Ok(Geometry::VirtualScreen(
            self.screen.left - self.virtual_screen.left,
            self.screen.top - self.virtual_screen.top,
            (self.screen.right - self.screen.left) as u32,
            (self.screen.bottom - self.screen.top) as u32,
            self.screen.left,
            self.screen.top,
        ))
    }
}
#[derive(Debug)]
pub struct CaptrsError(String);

impl std::fmt::Display for CaptrsError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        let Self(s) = self;
        write!(f, "{}", s)
    }
}

impl Error for CaptrsError {}
pub struct CaptrsRecorder {
    capturer: Capturer,
    fallback: Option<GdiRecorder>,
    fallback_rect: RECT,
    consecutive_failures: u32,
}

impl CaptrsRecorder {
    pub fn new(id: u8, fallback_rect: RECT) -> Result<CaptrsRecorder, Box<dyn Error>> {
        let capturer = catch_unwind(AssertUnwindSafe(|| Capturer::new(id.into())))
            .map_err(|_| CaptrsError("DXGI initialization panicked".into()))??;
        Ok(CaptrsRecorder {
            capturer,
            fallback: None,
            fallback_rect,
            consecutive_failures: 0,
        })
    }

    fn fallback_capture(&mut self) -> Result<crate::video::PixelProvider, Box<dyn Error>> {
        if self.fallback.is_none() {
            tracing::warn!("Switching Windows screen capture from DXGI to GDI fallback.");
            self.fallback = Some(GdiRecorder::new(self.fallback_rect, false)?);
        }
        self.fallback.as_mut().unwrap().capture()
    }
}

pub struct GdiRecorder {
    rect: RECT,
    width: usize,
    height: usize,
    buffer: Vec<u8>,
    capture_cursor: bool,
}

pub struct WindowRecorder {
    hwnd: usize,
    rect: RECT,
    width: usize,
    height: usize,
    buffer: Vec<u8>,
    gdi_fallback: GdiRecorder,
    capture_cursor: bool,
}

impl GdiRecorder {
    pub fn new(rect: RECT, capture_cursor: bool) -> Result<Self, Box<dyn Error>> {
        let width = (rect.right - rect.left).max(0) as usize;
        let height = (rect.bottom - rect.top).max(0) as usize;
        if width == 0 || height == 0 {
            return Err(Box::new(CaptrsError(
                "Invalid GDI capture rectangle".into(),
            )));
        }
        Ok(Self {
            rect,
            width,
            height,
            buffer: vec![0; width * height * 4],
            capture_cursor,
        })
    }

    fn draw_cursor(&self, dc: winapi::shared::windef::HDC) {
        draw_cursor_on_dc(dc, self.rect);
    }
}

impl Recorder for GdiRecorder {
    fn backend_name(&self) -> &'static str {
        "GDI BitBlt"
    }

    fn capture(&mut self) -> Result<crate::video::PixelProvider, Box<dyn Error>> {
        unsafe {
            let _input_desktop = attach_thread_to_input_desktop();
            let screen_dc = GetDC(ptr::null_mut());
            if screen_dc.is_null() {
                return Err(Box::new(CaptrsError(format!(
                    "GDI GetDC failed: {}",
                    std::io::Error::last_os_error()
                ))));
            }
            let mem_dc = CreateCompatibleDC(screen_dc);
            if mem_dc.is_null() {
                ReleaseDC(ptr::null_mut(), screen_dc);
                return Err(Box::new(CaptrsError(format!(
                    "GDI CreateCompatibleDC failed: {}",
                    std::io::Error::last_os_error()
                ))));
            }
            let bitmap = CreateCompatibleBitmap(screen_dc, self.width as i32, self.height as i32);
            if bitmap.is_null() {
                DeleteDC(mem_dc);
                ReleaseDC(ptr::null_mut(), screen_dc);
                return Err(Box::new(CaptrsError(format!(
                    "GDI CreateCompatibleBitmap failed: {}",
                    std::io::Error::last_os_error()
                ))));
            }
            let old = SelectObject(mem_dc, bitmap.cast());
            let copied = BitBlt(
                mem_dc,
                0,
                0,
                self.width as i32,
                self.height as i32,
                screen_dc,
                self.rect.left,
                self.rect.top,
                SRCCOPY | CAPTUREBLT,
            );
            if copied == 0 {
                let err = GetLastError();
                SelectObject(mem_dc, old);
                DeleteObject(bitmap.cast());
                DeleteDC(mem_dc);
                ReleaseDC(ptr::null_mut(), screen_dc);
                return Err(Box::new(CaptrsError(format!(
                    "GDI BitBlt failed: {}",
                    std::io::Error::from_raw_os_error(err as i32)
                ))));
            }
            if self.capture_cursor {
                self.draw_cursor(mem_dc);
            }

            let mut info: BITMAPINFO = mem::zeroed();
            info.bmiHeader = BITMAPINFOHEADER {
                biSize: mem::size_of::<BITMAPINFOHEADER>() as u32,
                biWidth: self.width as i32,
                biHeight: -(self.height as i32),
                biPlanes: 1,
                biBitCount: 32,
                biCompression: BI_RGB,
                biSizeImage: (self.buffer.len()) as u32,
                biXPelsPerMeter: 0,
                biYPelsPerMeter: 0,
                biClrUsed: 0,
                biClrImportant: 0,
            };
            let lines = GetDIBits(
                mem_dc,
                bitmap,
                0,
                self.height as u32,
                self.buffer.as_mut_ptr().cast(),
                &mut info,
                DIB_RGB_COLORS,
            );
            SelectObject(mem_dc, old);
            DeleteObject(bitmap.cast());
            DeleteDC(mem_dc);
            ReleaseDC(ptr::null_mut(), screen_dc);

            if lines == 0 {
                return Err(Box::new(CaptrsError(format!(
                    "GDI GetDIBits failed: {}",
                    std::io::Error::last_os_error()
                ))));
            }
        }
        Ok(crate::video::PixelProvider::BGR0(
            self.width,
            self.height,
            &self.buffer,
        ))
    }
}

struct InputDesktopHandle(HDESK);

impl Drop for InputDesktopHandle {
    fn drop(&mut self) {
        unsafe {
            if !self.0.is_null() {
                CloseDesktop(self.0);
            }
        }
    }
}

fn attach_thread_to_input_desktop() -> Option<InputDesktopHandle> {
    unsafe {
        let desktop = OpenInputDesktop(
            0,
            0,
            DESKTOP_READOBJECTS
                | DESKTOP_WRITEOBJECTS
                | DESKTOP_CREATEWINDOW
                | DESKTOP_CREATEMENU
                | DESKTOP_HOOKCONTROL
                | DESKTOP_JOURNALRECORD
                | DESKTOP_JOURNALPLAYBACK
                | DESKTOP_ENUMERATE
                | DESKTOP_SWITCHDESKTOP,
        );
        if desktop.is_null() {
            tracing::warn!(
                "OpenInputDesktop failed before GDI capture: {}",
                std::io::Error::last_os_error()
            );
            return None;
        }
        if SetThreadDesktop(desktop) == 0 {
            tracing::warn!(
                "SetThreadDesktop(input desktop) failed before GDI capture: {}",
                std::io::Error::last_os_error()
            );
        }
        Some(InputDesktopHandle(desktop))
    }
}

impl WindowRecorder {
    pub fn new(hwnd: HWND, rect: RECT, capture_cursor: bool) -> Result<Self, Box<dyn Error>> {
        let width = (rect.right - rect.left).max(0) as usize;
        let height = (rect.bottom - rect.top).max(0) as usize;
        if width == 0 || height == 0 {
            return Err(Box::new(CaptrsError(
                "Invalid window capture rectangle".into(),
            )));
        }
        Ok(Self {
            hwnd: hwnd as usize,
            rect,
            width,
            height,
            buffer: vec![0; width * height * 4],
            gdi_fallback: GdiRecorder::new(rect, capture_cursor)?,
            capture_cursor,
        })
    }

    fn hwnd(&self) -> HWND {
        self.hwnd as HWND
    }
}

impl Recorder for WindowRecorder {
    fn backend_name(&self) -> &'static str {
        "Windows PrintWindow"
    }

    fn capture(&mut self) -> Result<crate::video::PixelProvider, Box<dyn Error>> {
        unsafe {
            let screen_dc = GetDC(ptr::null_mut());
            if screen_dc.is_null() {
                return self.gdi_fallback.capture();
            }
            let mem_dc = CreateCompatibleDC(screen_dc);
            if mem_dc.is_null() {
                ReleaseDC(ptr::null_mut(), screen_dc);
                return self.gdi_fallback.capture();
            }
            let bitmap = CreateCompatibleBitmap(screen_dc, self.width as i32, self.height as i32);
            if bitmap.is_null() {
                DeleteDC(mem_dc);
                ReleaseDC(ptr::null_mut(), screen_dc);
                return self.gdi_fallback.capture();
            }
            let old = SelectObject(mem_dc, bitmap.cast());
            let printed = PrintWindow(self.hwnd(), mem_dc, 0x00000002);
            if printed == 0 {
                SelectObject(mem_dc, old);
                DeleteObject(bitmap.cast());
                DeleteDC(mem_dc);
                ReleaseDC(ptr::null_mut(), screen_dc);
                return self.gdi_fallback.capture();
            }
            if self.capture_cursor {
                draw_cursor_on_dc(mem_dc, self.rect);
            }

            let mut info: BITMAPINFO = mem::zeroed();
            info.bmiHeader = BITMAPINFOHEADER {
                biSize: mem::size_of::<BITMAPINFOHEADER>() as u32,
                biWidth: self.width as i32,
                biHeight: -(self.height as i32),
                biPlanes: 1,
                biBitCount: 32,
                biCompression: BI_RGB,
                biSizeImage: (self.buffer.len()) as u32,
                biXPelsPerMeter: 0,
                biYPelsPerMeter: 0,
                biClrUsed: 0,
                biClrImportant: 0,
            };
            let lines = GetDIBits(
                mem_dc,
                bitmap,
                0,
                self.height as u32,
                self.buffer.as_mut_ptr().cast(),
                &mut info,
                DIB_RGB_COLORS,
            );
            SelectObject(mem_dc, old);
            DeleteObject(bitmap.cast());
            DeleteDC(mem_dc);
            ReleaseDC(ptr::null_mut(), screen_dc);

            if lines == 0 {
                return self.gdi_fallback.capture();
            }
        }
        Ok(crate::video::PixelProvider::BGR0(
            self.width,
            self.height,
            &self.buffer,
        ))
    }
}

fn draw_cursor_on_dc(dc: winapi::shared::windef::HDC, rect: RECT) {
    unsafe {
        let mut cursor_info: CURSORINFO = mem::zeroed();
        cursor_info.cbSize = mem::size_of::<CURSORINFO>() as DWORD;
        let has_cursor_info = GetCursorInfo(&mut cursor_info) != 0;
        let mut pos = cursor_info.ptScreenPos;
        if !has_cursor_info || cursor_info.flags != CURSOR_SHOWING {
            pos = POINT { x: 0, y: 0 };
            if GetCursorPos(&mut pos) == 0 {
                return;
            }
        }

        let source_cursor = if has_cursor_info && !cursor_info.hCursor.is_null() {
            cursor_info.hCursor
        } else {
            LoadCursorW(ptr::null_mut(), IDC_ARROW)
        };
        if source_cursor.is_null() {
            return;
        }

        let cursor = CopyIcon(source_cursor);
        if cursor.is_null() {
            return;
        }

        let mut icon_info: ICONINFO = mem::zeroed();
        let has_icon_info = GetIconInfo(cursor, &mut icon_info) != 0;
        let hotspot_x = if has_icon_info { icon_info.xHotspot } else { 0 };
        let hotspot_y = if has_icon_info { icon_info.yHotspot } else { 0 };
        if has_icon_info {
            if !icon_info.hbmMask.is_null() {
                DeleteObject(icon_info.hbmMask.cast());
            }
            if !icon_info.hbmColor.is_null() {
                DeleteObject(icon_info.hbmColor.cast());
            }
        }

        let x = pos.x - rect.left - hotspot_x as i32;
        let y = pos.y - rect.top - hotspot_y as i32;
        if x > rect.right - rect.left || y > rect.bottom - rect.top {
            DestroyIcon(cursor);
            return;
        }
        DrawIconEx(dc, x, y, cursor, 0, 0, 0, ptr::null_mut(), DI_NORMAL);
        DestroyIcon(cursor);
    }
}

impl Recorder for CaptrsRecorder {
    fn backend_name(&self) -> &'static str {
        if self.fallback.is_some() {
            "GDI BitBlt"
        } else {
            "DXGI Desktop Duplication"
        }
    }

    fn capture(&mut self) -> Result<crate::video::PixelProvider, Box<dyn Error>> {
        if self.fallback.is_some() {
            return self.fallback_capture();
        }

        let capture_result = catch_unwind(AssertUnwindSafe(|| self.capturer.capture_store_frame()))
            .map_err(|_| CaptrsError("DXGI capture panicked".into()));
        if let Err(err) = capture_result.and_then(|result| {
            result.map_err(|err| CaptrsError(format!("Captrs failed to capture frame: {err:?}")))
        }) {
            self.consecutive_failures = self.consecutive_failures.saturating_add(1);
            if self.consecutive_failures >= 3 {
                tracing::warn!(
                    "DXGI screen capture failed {} consecutive times, falling back to GDI: {}",
                    self.consecutive_failures,
                    err
                );
                return self.fallback_capture();
            }
            return Err(Box::new(err));
        }
        self.consecutive_failures = 0;
        let (w, h) = self.capturer.geometry();
        match self.capturer.get_stored_frame() {
            Some(frame) => Ok(crate::video::PixelProvider::BGR0(
                w as usize,
                h as usize,
                unsafe { std::mem::transmute(frame) },
            )),
            None => {
                self.consecutive_failures = self.consecutive_failures.saturating_add(1);
                if self.consecutive_failures >= 3 {
                    tracing::warn!(
                        "DXGI screen capture returned no frame {} consecutive times, falling back to GDI.",
                        self.consecutive_failures
                    );
                    return self.fallback_capture();
                }
                Err(Box::new(CaptrsError(
                    "Captrs did not return a captured frame".into(),
                )))
            }
        }
    }
}

struct EnumWindowData {
    virtual_screen: RECT,
    windows: Vec<WindowCapturable>,
}

pub fn get_window_capturables(virtual_screen: RECT) -> Vec<WindowCapturable> {
    let mut data = EnumWindowData {
        virtual_screen,
        windows: Vec::new(),
    };
    unsafe {
        EnumWindows(
            Some(enum_window_capturable),
            (&mut data as *mut EnumWindowData) as LPARAM,
        );
    }
    data.windows
}

unsafe extern "system" fn enum_window_capturable(hwnd: HWND, lparam: LPARAM) -> BOOL {
    let data = &mut *(lparam as *mut EnumWindowData);
    if hwnd.is_null() || IsWindowVisible(hwnd) == 0 || IsIconic(hwnd) != 0 {
        return TRUE;
    }
    let root = GetAncestor(hwnd, GA_ROOT);
    if !root.is_null() && root != hwnd {
        return TRUE;
    }
    let title_len = GetWindowTextLengthW(hwnd);
    if title_len <= 0 {
        return TRUE;
    }
    let mut title_buf = vec![0u16; title_len as usize + 1];
    let copied = GetWindowTextW(hwnd, title_buf.as_mut_ptr(), title_buf.len() as i32);
    if copied <= 0 {
        return TRUE;
    }
    let title = String::from_utf16_lossy(&title_buf[..copied as usize])
        .trim()
        .to_string();
    if title.is_empty() {
        return TRUE;
    }
    let mut rect: RECT = mem::zeroed();
    if GetWindowRect(hwnd, &mut rect) == 0 {
        return TRUE;
    }
    let width = rect.right - rect.left;
    let height = rect.bottom - rect.top;
    if width < 64 || height < 64 {
        return TRUE;
    }
    data.windows.push(WindowCapturable {
        hwnd: hwnd as usize,
        title,
        rect,
        virtual_screen: data.virtual_screen,
    });
    TRUE
}
