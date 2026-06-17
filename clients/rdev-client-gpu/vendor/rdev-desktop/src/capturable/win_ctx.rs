use std::mem::zeroed;
use std::{mem, ptr};
use tracing::warn;
use winapi::shared::dxgi::{
    CreateDXGIFactory1, IDXGIAdapter1, IDXGIFactory1, IDXGIOutput, IID_IDXGIFactory1,
    DXGI_OUTPUT_DESC,
};
use winapi::shared::minwindef::{BOOL, LPARAM, TRUE};

use winapi::shared::windef::*;
use winapi::shared::winerror::*;
use winapi::um::winuser::*;
use wio::com::ComPtr;

// from https://github.com/bryal/dxgcap-rs/blob/009b746d1c19c4c10921dd469eaee483db6aa002/src/lib.r
fn hr_failed(hr: HRESULT) -> bool {
    hr < 0
}

fn create_dxgi_factory_1() -> Result<ComPtr<IDXGIFactory1>, HRESULT> {
    unsafe {
        let mut factory = ptr::null_mut();
        let hr = CreateDXGIFactory1(&IID_IDXGIFactory1, &mut factory);
        if hr_failed(hr) {
            Err(hr)
        } else {
            Ok(ComPtr::from_raw(factory as *mut IDXGIFactory1))
        }
    }
}

fn get_adapter_outputs(adapter: &IDXGIAdapter1) -> Vec<ComPtr<IDXGIOutput>> {
    let mut outputs = Vec::new();
    for i in 0.. {
        unsafe {
            let mut output = ptr::null_mut();
            if hr_failed(adapter.EnumOutputs(i, &mut output)) {
                break;
            } else {
                let mut out_desc = zeroed();
                (*output).GetDesc(&mut out_desc);
                if out_desc.AttachedToDesktop != 0 {
                    outputs.push(ComPtr::from_raw(output))
                } else {
                    break;
                }
            }
        }
    }
    outputs
}

#[derive(Clone)]
pub struct WinCtx {
    outputs: Vec<DXGI_OUTPUT_DESC>,
    union_rect: RECT,
}

#[derive(Clone)]
pub struct WinOutput {
    pub capture_id: u8,
    pub desc: DXGI_OUTPUT_DESC,
}

impl WinCtx {
    pub fn new() -> WinCtx {
        let mut desktops: Vec<DXGI_OUTPUT_DESC> = Vec::new();
        let mut union: RECT = unsafe { mem::zeroed() };
        unsafe {
            match create_dxgi_factory_1() {
                Ok(factory) => {
                    let mut adapter = ptr::null_mut();
                    if factory.EnumAdapters1(0, &mut adapter) != DXGI_ERROR_NOT_FOUND && !adapter.is_null() {
                        let adp = ComPtr::from_raw(adapter);
                        let outputs = get_adapter_outputs(&adp);
                        for o in outputs {
                            let mut desc: DXGI_OUTPUT_DESC = mem::zeroed();
                            o.GetDesc(ptr::addr_of_mut!(desc));
                            add_output(&mut desktops, &mut union, desc);
                        }
                    }
                }
                Err(hr) => warn!("Failed to create DXGIFactory1, hr={hr:x}; falling back to GDI monitor enumeration"),
            }
        }
        if desktops.is_empty() {
            warn!("DXGI monitor enumeration returned no desktop outputs; falling back to GDI monitor enumeration");
            desktops = enum_display_monitor_outputs(&mut union);
        }
        WinCtx {
            outputs: desktops,
            union_rect: union,
        }
    }
    pub fn get_capture_outputs(&self) -> Vec<WinOutput> {
        let mut outputs = self.outputs.clone();
        outputs.sort_by_key(|desc| if is_primary(desc.Monitor) { 0 } else { 1 });
        outputs
            .into_iter()
            .enumerate()
            .map(|(index, desc)| WinOutput {
                capture_id: index as u8,
                desc,
            })
            .collect()
    }
    pub fn get_union_rect(&self) -> &RECT {
        &self.union_rect
    }
}

fn is_primary(monitor: HMONITOR) -> bool {
    unsafe {
        let mut info: MONITORINFO = mem::zeroed();
        info.cbSize = mem::size_of::<MONITORINFO>() as u32;
        GetMonitorInfoW(monitor, &mut info) != 0 && (info.dwFlags & MONITORINFOF_PRIMARY) != 0
    }
}

fn add_output(outputs: &mut Vec<DXGI_OUTPUT_DESC>, union: &mut RECT, desc: DXGI_OUTPUT_DESC) {
    unsafe {
        if outputs.is_empty() {
            *union = desc.DesktopCoordinates;
        } else {
            UnionRect(
                ptr::addr_of_mut!(*union),
                ptr::addr_of!(*union),
                ptr::addr_of!(desc.DesktopCoordinates),
            );
        }
    }
    outputs.push(desc);
}

struct MonitorEnumData {
    outputs: Vec<DXGI_OUTPUT_DESC>,
    union_rect: *mut RECT,
}

fn enum_display_monitor_outputs(union: &mut RECT) -> Vec<DXGI_OUTPUT_DESC> {
    let mut data = MonitorEnumData {
        outputs: Vec::new(),
        union_rect: union as *mut RECT,
    };
    unsafe {
        EnumDisplayMonitors(
            ptr::null_mut(),
            ptr::null(),
            Some(enum_display_monitor),
            (&mut data as *mut MonitorEnumData) as LPARAM,
        );
    }
    data.outputs
}

unsafe extern "system" fn enum_display_monitor(
    monitor: HMONITOR,
    _hdc: HDC,
    _rect: LPRECT,
    lparam: LPARAM,
) -> BOOL {
    let data = &mut *(lparam as *mut MonitorEnumData);
    let mut info: MONITORINFOEXW = mem::zeroed();
    info.cbSize = mem::size_of::<MONITORINFOEXW>() as u32;
    if GetMonitorInfoW(monitor, &mut info as *mut MONITORINFOEXW as *mut MONITORINFO) == 0 {
        return TRUE;
    }
    let mut desc: DXGI_OUTPUT_DESC = mem::zeroed();
    desc.DeviceName = info.szDevice;
    desc.DesktopCoordinates = info.rcMonitor;
    desc.AttachedToDesktop = TRUE;
    desc.Monitor = monitor;
    add_output(data.outputs.as_mut(), &mut *data.union_rect, desc);
    TRUE
}
