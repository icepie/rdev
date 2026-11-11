#[cfg(feature = "cua-native")]
use anyhow::Context;
use anyhow::{bail, Result};
use serde::Deserialize;
use serde_json::{Map, Value};
#[cfg(feature = "cua-native")]
use tokio::sync::Mutex;
mod ffi;
#[cfg(feature = "cua-native")]
mod native;

const MAX_ARGUMENTS: usize = 256 * 1024;

#[derive(Debug, Deserialize)]
#[serde(tag = "action", rename_all = "snake_case", deny_unknown_fields)]
pub enum Request {
    Tools,
    Call {
        name: String,
        #[serde(default)]
        arguments: Map<String, Value>,
    },
}

#[cfg(not(feature = "cua-native"))]
pub fn supported() -> bool {
    false
}

#[cfg(all(
    feature = "cua-native",
    target_os = "linux",
    target_arch = "x86_64",
    not(target_env = "musl")
))]
pub fn supported() -> bool {
    let version = unsafe { libc::gnu_get_libc_version() };
    if version.is_null() {
        return false;
    }
    let Ok(version) = (unsafe { std::ffi::CStr::from_ptr(version) }).to_str() else {
        return false;
    };
    let mut parts = version
        .split('.')
        .filter_map(|part| part.parse::<u32>().ok());
    matches!((parts.next(), parts.next()), (Some(major), Some(minor)) if (major, minor) >= (2, 30))
}

#[cfg(all(
    feature = "cua-native",
    target_os = "linux",
    target_arch = "aarch64",
    not(target_env = "musl")
))]
pub fn supported() -> bool {
    let version = unsafe { libc::gnu_get_libc_version() };
    if version.is_null() {
        return false;
    }
    let Ok(version) = (unsafe { std::ffi::CStr::from_ptr(version) }).to_str() else {
        return false;
    };
    let mut parts = version
        .split('.')
        .filter_map(|part| part.parse::<u32>().ok());
    matches!((parts.next(), parts.next()), (Some(major), Some(minor)) if (major, minor) >= (2, 30))
}

#[cfg(all(
    feature = "cua-native",
    target_os = "macos",
    any(target_arch = "x86_64", target_arch = "aarch64")
))]
pub fn supported() -> bool {
    let key = b"kern.osproductversion\0";
    let mut bytes = [0u8; 32];
    let mut length = bytes.len();
    let status = unsafe {
        libc::sysctlbyname(
            key.as_ptr().cast(),
            bytes.as_mut_ptr().cast(),
            &mut length,
            std::ptr::null_mut(),
            0,
        )
    };
    if status != 0 || length == 0 || length > bytes.len() {
        return false;
    }
    std::str::from_utf8(&bytes[..length.saturating_sub(1)])
        .ok()
        .and_then(|version| version.split('.').next())
        .and_then(|major| major.parse::<u32>().ok())
        .is_some_and(|major| major >= 13)
}

#[cfg(all(
    feature = "cua-native",
    target_os = "windows",
    any(target_arch = "x86_64", target_arch = "aarch64")
))]
pub fn supported() -> bool {
    #[repr(C)]
    struct VersionInfo {
        size: u32,
        major: u32,
        minor: u32,
        build: u32,
        platform: u32,
        service_pack: [u16; 128],
    }
    #[link(name = "ntdll")]
    unsafe extern "system" {
        fn RtlGetVersion(info: *mut VersionInfo) -> i32;
    }
    let mut info = VersionInfo {
        size: std::mem::size_of::<VersionInfo>() as u32,
        major: 0,
        minor: 0,
        build: 0,
        platform: 0,
        service_pack: [0; 128],
    };
    unsafe { RtlGetVersion(&mut info) == 0 && info.major >= 10 }
}

#[cfg(not(any(
    not(feature = "cua-native"),
    all(
        feature = "cua-native",
        target_os = "linux",
        any(target_arch = "x86_64", target_arch = "aarch64"),
        not(target_env = "musl")
    ),
    all(
        feature = "cua-native",
        target_os = "macos",
        any(target_arch = "x86_64", target_arch = "aarch64")
    ),
    all(
        feature = "cua-native",
        target_os = "windows",
        any(target_arch = "x86_64", target_arch = "aarch64")
    )
)))]
pub fn supported() -> bool {
    false
}

pub struct Session {
    #[cfg(feature = "cua-native")]
    inner: Mutex<Option<native::SessionRuntime>>,
}

impl Session {
    pub fn new() -> Self {
        Self {
            #[cfg(feature = "cua-native")]
            inner: Mutex::new(None),
        }
    }

    pub async fn request(&mut self, request: Request) -> Result<Value> {
        if !supported() {
            bail!("native CUA is unsupported on this build or platform");
        }
        #[cfg(feature = "cua-native")]
        {
            if let Request::Call { arguments, .. } = &request {
                validate_arguments(arguments)?;
                if serde_json::to_vec(arguments)?.len() > MAX_ARGUMENTS {
                    bail!("native CUA arguments exceed size limit");
                }
            }
            let mut guard = self.inner.lock().await;
            let mut runtime = match guard.take() {
                Some(runtime) => runtime,
                None => native::SessionRuntime::new()?,
            };
            let result = match request {
                Request::Tools => runtime.tools().await,
                Request::Call { name, arguments } => runtime.call(&name, arguments).await,
            };
            // A failed tool call is not a desktop-session boundary. Keep the
            // SDK session and its cursor state attached to this CGo handle;
            // otherwise the next request silently gets a fresh session ID.
            *guard = Some(runtime);
            result
        }
        #[cfg(not(feature = "cua-native"))]
        {
            let _ = request;
            unreachable!()
        }
    }

    #[cfg(feature = "cua-native")]
    pub async fn shutdown(&mut self) -> Result<()> {
        let mut guard = self.inner.lock().await;
        drop(guard.take());
        Ok(())
    }
    #[cfg(not(feature = "cua-native"))]
    pub async fn shutdown(&mut self) -> Result<()> {
        Ok(())
    }
}

impl Default for Session {
    fn default() -> Self {
        Self::new()
    }
}

#[cfg(feature = "cua-native")]
impl Drop for Session {
    fn drop(&mut self) {
        if let Ok(mut guard) = self.inner.try_lock() {
            drop(guard.take());
        }
    }
}

#[cfg(feature = "cua-native")]
fn filter_tools(mut response: Value) -> Result<Value> {
    let tools = response
        .get_mut("tools")
        .and_then(Value::as_array_mut)
        .context("Cua SDK tool inventory omitted tools array")?;
    tools.retain(|tool| {
        tool.get("name")
            .and_then(Value::as_str)
            .is_some_and(|name| !name.is_empty())
    });
    for tool in tools {
        if let Some(schema) = tool.get_mut("inputSchema") {
            if let Some(properties) = schema.get_mut("properties").and_then(Value::as_object_mut) {
                for field in ["session", "session_id", "_session_id"] {
                    properties.remove(field);
                }
            }
            if let Some(required) = schema.get_mut("required").and_then(Value::as_array_mut) {
                required.retain(|field| {
                    !matches!(
                        field.as_str(),
                        Some("session" | "session_id" | "_session_id")
                    )
                });
            }
        }
    }
    Ok(response)
}

fn validate_arguments(arguments: &Map<String, Value>) -> Result<()> {
    fn inspect(value: &Value) -> Result<()> {
        if let Value::Object(object) = value {
            for (key, child) in object {
                let key = key.to_ascii_lowercase();
                if matches!(key.as_str(), "session" | "session_id" | "_session_id") {
                    bail!("session argument is reserved by bridge");
                }
                if matches!(
                    key.as_str(),
                    "output_path" | "output_file" | "file_path" | "filename" | "save_to"
                ) {
                    bail!("file output arguments are not allowed");
                }
                inspect(child)?;
            }
        } else if let Value::Array(array) = value {
            for child in array {
                inspect(child)?;
            }
        }
        Ok(())
    }
    for (key, value) in arguments {
        let normalized = key.to_ascii_lowercase();
        if matches!(
            normalized.as_str(),
            "session" | "session_id" | "_session_id"
        ) {
            bail!("session argument is reserved by bridge");
        }
        if matches!(
            normalized.as_str(),
            "output_path" | "output_file" | "file_path" | "filename" | "save_to"
        ) {
            bail!("file output arguments are not allowed");
        }
        inspect(value)?;
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn rejects_host_session_overrides_at_every_depth() {
        for value in [
            serde_json::json!({"session": "foreign"}),
            serde_json::json!({"target": {"Session_ID": "foreign"}}),
            serde_json::json!({"targets": [{"_session_id": "foreign"}]}),
        ] {
            assert!(validate_arguments(value.as_object().unwrap()).is_err());
        }
    }

    #[test]
    fn forbids_file_outputs_but_accepts_native_target_parameters() {
        assert!(validate_arguments(
            serde_json::json!({"output_path":"/tmp/capture.png"})
                .as_object()
                .unwrap()
        )
        .is_err());
        assert!(validate_arguments(
            serde_json::json!({"window_id":"123", "include_screenshot":false})
                .as_object()
                .unwrap()
        )
        .is_ok());
    }

    #[cfg(feature = "cua-native")]
    #[test]
    fn exposes_all_sdk_tools_and_hides_runtime_session_fields() {
        let tools = filter_tools(serde_json::json!({
            "tools": [
                {
                    "name": "list_apps",
                    "inputSchema": {
                        "properties": {"session": {}, "session_id": {}, "window_id": {}},
                        "required": ["session", "session_id", "window_id"]
                    }
                },
                {"name": "set_agent_cursor_enabled", "inputSchema": {"type": "object"}},
                {"name": ""}
            ]
        }))
        .unwrap();
        let tools = tools["tools"].as_array().unwrap();
        assert_eq!(tools.len(), 2);
        assert_eq!(tools[1]["name"], "set_agent_cursor_enabled");
        assert!(tools[0]["inputSchema"]["properties"]
            .get("session")
            .is_none());
        assert!(tools[0]["inputSchema"]["properties"]
            .get("session_id")
            .is_none());
        assert_eq!(
            tools[0]["inputSchema"]["required"],
            serde_json::json!(["window_id"])
        );
    }

    #[cfg(feature = "cua-native")]
    #[tokio::test]
    async fn failed_tool_request_does_not_replace_the_cgo_session() {
        if std::env::var_os("DISPLAY").is_none() {
            return;
        }
        let mut session = Session::new();
        let call = |name: &str, value: Value| Request::Call {
            name: name.to_owned(),
            arguments: value.as_object().cloned().unwrap_or_default(),
        };
        let enabled = session
            .request(call(
                "set_agent_cursor_enabled",
                serde_json::json!({"enabled": true}),
            ))
            .await
            .expect("enable cursor");
        session
            .request(call(
                "set_agent_cursor_motion",
                serde_json::json!({"glide_duration_ms": 321.0}),
            ))
            .await
            .expect("set motion");
        assert!(session
            .request(call("not_a_real_tool", serde_json::json!({})))
            .await
            .is_err());

        let state = session
            .request(call("get_agent_cursor_state", serde_json::json!({})))
            .await
            .expect("read cursor state after failed request");
        assert_eq!(state["structuredContent"]["enabled"], true, "{state}");
        assert_eq!(
            state["structuredContent"]["motion"]["glide_duration_ms"], 321.0,
            "{state}"
        );
        assert_eq!(
            state["structuredContent"]["session"], enabled["structuredContent"]["session"],
            "a CGo tool error must not mint a replacement cursor session"
        );
    }

    #[cfg(not(feature = "cua-native"))]
    #[test]
    fn feature_off_is_unsupported() {
        assert!(!supported());
    }
}
