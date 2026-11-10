use super::filter_tools;
use anyhow::{Context, Result};
use cua_driver_sdk::{
    ConfiguredDriverOptions, CuaDriver, RuntimeAuthorizationOptions, SessionPermissionMode,
    TrustedSessionOptions,
};
use serde_json::{Map, Value};
use std::sync::{Arc, LazyLock};
use std::time::Duration;

const SESSION_TTL_SECONDS: u64 = 24 * 60 * 60;
const SESSION_IDLE_TTL_SECONDS: u64 = 60 * 60;

static DRIVER: LazyLock<std::result::Result<Arc<CuaDriver>, String>> = LazyLock::new(|| {
    CuaDriver::create_configured(ConfiguredDriverOptions {
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
    })
    .map_err(|error| error.to_string())
});

fn driver() -> Result<Arc<CuaDriver>> {
    DRIVER
        .as_ref()
        .map(Arc::clone)
        .map_err(|error| anyhow::anyhow!("create embedded Cua Driver: {error}"))
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
        serde_json::from_str(&result.raw_json)
            .with_context(|| format!("parse Cua Driver {name} result"))
    }
}
