use anyhow::{anyhow, Result};
use tokio::net::TcpStream;
use tokio_tungstenite::{
    connect_async,
    tungstenite::{http::header::LOCATION, Error as WsError},
    MaybeTlsStream, WebSocketStream,
};
use url::Url;

pub type WsStream = WebSocketStream<MaybeTlsStream<TcpStream>>;

pub async fn connect_async_follow_redirects(
    url: &str,
    max_redirects: usize,
) -> Result<(WsStream, tokio_tungstenite::tungstenite::handshake::client::Response)> {
    let mut current = url.to_string();
    for _ in 0..=max_redirects {
        match connect_async(current.as_str()).await {
            Ok(pair) => return Ok(pair),
            Err(WsError::Http(resp)) if resp.status().is_redirection() => {
                let location = resp
                    .headers()
                    .get(LOCATION)
                    .ok_or_else(|| anyhow!("websocket redirect without location"))?
                    .to_str()
                    .map_err(|_| anyhow!("websocket redirect location is not valid utf-8"))?;
                current = resolve_redirect(&current, location)?;
                tracing::info!("websocket redirect to {current}");
            }
            Err(err) => return Err(err.into()),
        }
    }
    Err(anyhow!("too many websocket redirects"))
}

fn resolve_redirect(current: &str, location: &str) -> Result<String> {
    let base = Url::parse(current)?;
    let mut next = base.join(location)?;
    match next.scheme() {
        "http" => next.set_scheme("ws").map_err(|_| anyhow!("invalid redirect scheme"))?,
        "https" => next.set_scheme("wss").map_err(|_| anyhow!("invalid redirect scheme"))?,
        "ws" | "wss" => {}
        other => return Err(anyhow!("unsupported websocket redirect scheme: {other}")),
    }
    Ok(next.to_string())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn resolves_redirects_to_ws_schemes() {
        assert_eq!(
            resolve_redirect("ws://a.example/ws", "https://b.example/r").unwrap(),
            "wss://b.example/r"
        );
        assert_eq!(
            resolve_redirect("wss://a.example/base/ws", "../ws2").unwrap(),
            "wss://a.example/ws2"
        );
    }
}
