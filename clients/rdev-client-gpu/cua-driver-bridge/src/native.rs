use super::filter_tools;
use anyhow::{Context, Result};
use cua_driver_sdk::{
    ConfiguredDriverOptions, CuaDriver, DriverHostOptions, RuntimeAuthorizationOptions,
    SessionPermissionMode, TrustedSessionOptions,
};
use serde_json::{Map, Value};
use std::sync::{Arc, LazyLock};
use std::time::Duration;

const SESSION_TTL_SECONDS: u64 = 24 * 60 * 60;
const SESSION_IDLE_TTL_SECONDS: u64 = 60 * 60;

static DRIVER: LazyLock<std::result::Result<Arc<CuaDriver>, String>> = LazyLock::new(|| {
    CuaDriver::try_create_configured_for_host(
        ConfiguredDriverOptions {
            claude_code_compatibility: false,
            authorization: RuntimeAuthorizationOptions {
                allowed_modes: vec![SessionPermissionMode::Standard],
                compatibility_mode: SessionPermissionMode::Standard,
                compatibility_capability_manifest_path: None,
                compatibility_bounded_manifest_path: None,
                unrestricted_acknowledged: false,
                max_session_ttl_seconds: SESSION_TTL_SECONDS,
                max_idle_ttl_seconds: SESSION_IDLE_TTL_SECONDS,
            },
        },
        DriverHostOptions {
            // This is the native client runtime, not a headless language SDK
            // embedding. The default cursor config enables the platform
            // renderer; callers can still hide it per session.
            cursor: Default::default(),
            host_owns_permission_ux: true,
            host_bundle_id: None,
            claude_code_compatibility: false,
            prepare_desktop_environment: true,
            register_host_tools: None,
            authorization_host: None,
            activity_observer: None,
        },
    )
    .map_err(|error| error.to_string())
});

fn driver() -> Result<Arc<CuaDriver>> {
    DRIVER
        .as_ref()
        .map(Arc::clone)
        .map_err(|error| anyhow::anyhow!("create embedded Cua Driver: {error}"))
}

#[cfg(target_os = "macos")]
pub(super) fn initialize_cursor_host() -> Result<()> {
    driver().map(|_| ())
}

pub(super) struct SessionRuntime {
    driver: Arc<CuaDriver>,
    session: Arc<cua_driver_sdk::CuaDriverSession>,
    tools: Option<Value>,
}

impl SessionRuntime {
    pub(super) fn new() -> Result<Self> {
        let driver = driver()?;
        let session = driver
            .create_trusted_session(TrustedSessionOptions {
                public_session: uuid::Uuid::new_v4().to_string(),
                mode: SessionPermissionMode::Standard,
                ttl_seconds: SESSION_TTL_SECONDS,
                idle_ttl_seconds: SESSION_IDLE_TTL_SECONDS,
                capability_manifest_path: None,
                bounded_manifest_path: None,
            })
            .context("create transport-bound Cua Driver session")?;
        Ok(Self {
            driver,
            session,
            tools: None,
        })
    }

    pub(super) async fn tools(&mut self) -> Result<Value> {
        if let Some(tools) = &self.tools {
            return Ok(tools.clone());
        }
        let response = tokio::time::timeout(Duration::from_secs(90), self.driver.list_tools_json())
            .await
            .context("list Cua tools timed out")??;
        let filtered = filter_tools(
            serde_json::from_str(&response)
                .context("parse the embedded Cua Driver tool inventory")?,
        )?;
        self.tools = Some(filtered.clone());
        Ok(filtered)
    }

    pub(super) async fn call(
        &mut self,
        name: &str,
        arguments: Map<String, Value>,
    ) -> Result<Value> {
        let tools = self.tools().await?;
        let available = tools
            .get("tools")
            .and_then(Value::as_array)
            .is_some_and(|tools| {
                tools
                    .iter()
                    .any(|tool| tool.get("name").and_then(Value::as_str) == Some(name))
            });
        if !available {
            anyhow::bail!("native CUA tool is unavailable: {name}");
        }
        let encoded = serde_json::to_string(&arguments)?;
        let result = tokio::time::timeout(
            Duration::from_secs(90),
            self.session.call_tool(name.to_owned(), encoded),
        )
        .await
        .with_context(|| format!("Cua Driver {name} call timed out"))??;
        let result: Value = serde_json::from_str(&result.raw_json)
            .with_context(|| format!("parse Cua Driver {name} result"))?;
        if result.get("isError").and_then(Value::as_bool) == Some(true) {
            return Ok(result);
        }
        if matches!(name, "set_agent_cursor_enabled" | "set_agent_cursor_motion") {
            self.wait_for_cursor_state(name, &result).await?;
        }
        Ok(result)
    }

    async fn wait_for_cursor_state(&self, name: &str, result: &Value) -> Result<()> {
        const READBACK_TIMEOUT: Duration = Duration::from_secs(5);
        let expected = result
            .get("structuredContent")
            .context("cursor setter omitted structuredContent")?;
        let session = expected
            .get("session")
            .and_then(Value::as_str)
            .context("cursor setter omitted session identity")?;
        let deadline = tokio::time::Instant::now() + READBACK_TIMEOUT;
        loop {
            let remaining = deadline.saturating_duration_since(tokio::time::Instant::now());
            if remaining.is_zero() {
                anyhow::bail!(
                    "Cua Driver {name} was accepted but renderer state did not update within {}s",
                    READBACK_TIMEOUT.as_secs()
                );
            }
            let state = tokio::time::timeout(
                remaining,
                self.session
                    .call_tool("get_agent_cursor_state".to_owned(), "{}".to_owned()),
            )
            .await
            .with_context(|| {
                format!(
                    "Cua Driver {name} renderer state did not update within {}s",
                    READBACK_TIMEOUT.as_secs()
                )
            })??;
            let state: Value = serde_json::from_str(&state.raw_json)
                .context("parse Cua Driver cursor-state result")?;
            let structured = &state["structuredContent"];
            let matches = structured["session"] == session
                && match name {
                    "set_agent_cursor_enabled" => structured["enabled"] == expected["enabled"],
                    "set_agent_cursor_motion" => structured["motion"] == expected["motion"],
                    _ => false,
                };
            if matches {
                return Ok(());
            }
            if tokio::time::Instant::now() >= deadline {
                anyhow::bail!(
                    "Cua Driver {name} was accepted but renderer state did not update within {}s: {state}",
                    READBACK_TIMEOUT.as_secs()
                );
            }
            tokio::time::sleep(Duration::from_millis(10)).await;
        }
    }
}

#[cfg(all(test, feature = "cua-native"))]
mod tests {
    use super::SessionRuntime;
    use serde_json::{json, Map, Value};
    use std::time::Duration;

    #[tokio::test]
    async fn cursor_enable_and_motion_state_are_read_back_from_renderer() {
        if std::env::var_os("DISPLAY").is_none() {
            // The test exercises the renderer's command queue, not the
            // headless SDK fallback. Run it under Xvfb in Linux CI/smoke runs.
            return;
        }
        let driver = super::driver().expect("create native driver");
        let default_state = driver
            .call_tool("get_agent_cursor_state".into(), "{}".into())
            .await
            .expect("read default cursor state");
        let default_state: Value =
            serde_json::from_str(&default_state.raw_json).expect("parse default state");
        assert_eq!(
            default_state["structuredContent"]["enabled"], true,
            "{default_state}"
        );
        let mut runtime = SessionRuntime::new().expect("create native driver session");
        let arguments = |value: Value| value.as_object().cloned().unwrap_or_default();

        let initial = runtime
            .call("get_agent_cursor_state", Map::new())
            .await
            .expect("read initial cursor state");
        assert_eq!(initial["structuredContent"]["enabled"], true, "{initial}");
        let disabled = runtime
            .call(
                "set_agent_cursor_enabled",
                arguments(json!({"enabled": false})),
            )
            .await
            .expect("disable cursor");
        assert_eq!(
            disabled["structuredContent"]["enabled"], false,
            "{disabled}"
        );
        let enabled = runtime
            .call(
                "set_agent_cursor_enabled",
                arguments(json!({"enabled": true})),
            )
            .await
            .expect("enable cursor");
        assert_eq!(enabled["structuredContent"]["enabled"], true, "{enabled}");
        let motion = runtime
            .call(
                "set_agent_cursor_motion",
                arguments(json!({"glide_duration_ms": 321.0})),
            )
            .await
            .expect("set cursor motion");
        assert_eq!(
            motion["structuredContent"]["motion"]["glide_duration_ms"], 321.0,
            "{motion}"
        );

        let deadline = tokio::time::Instant::now() + Duration::from_secs(5);
        loop {
            let state = runtime
                .call("get_agent_cursor_state", Map::new())
                .await
                .expect("read cursor state");
            if state["structuredContent"]["enabled"] == true
                && state["structuredContent"]["motion"]["glide_duration_ms"] == 321.0
            {
                break;
            }
            assert!(tokio::time::Instant::now() < deadline, "{state}");
            tokio::time::sleep(Duration::from_millis(10)).await;
        }
    }
}
