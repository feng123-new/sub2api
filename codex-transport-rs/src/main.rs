//! Private Unix-socket HTTP relay. Only this process establishes the upstream
//! TLS/ALPN/HTTP connection for opted-in Codex requests. Credentials never
//! appear in application logs or process arguments.

use axum::body::Body;
use futures_util::TryStreamExt;
use http_body_util::BodyExt;
use hyper::body::Incoming;
use hyper::header::{HeaderMap, HeaderName};
use hyper::service::service_fn;
use hyper::{Method, Request, Response, StatusCode, Version};
use hyper_util::rt::TokioIo;
use reqwest::{Client, Proxy, Url};
use std::collections::HashMap;
use std::convert::Infallible;
use std::fs;
use std::os::unix::fs::PermissionsExt;
use std::sync::Arc;
use std::time::{Duration, Instant};
use tokio::net::UnixListener;
use tokio::sync::Mutex;

const SOCKET_DEFAULT: &str = "/run/codex-rust/transport.sock";
const MAX_CLIENTS: usize = 256;
const CLIENT_IDLE_TTL: Duration = Duration::from_secs(15 * 60);

struct CachedClient {
    client: Client,
    last_used: Instant,
}

#[derive(Clone, Copy, Default, Debug, Eq, PartialEq)]
enum TlsBackend {
    #[default]
    Native,
    Rustls,
}

impl TlsBackend {
    fn from_env() -> std::io::Result<Self> {
        match std::env::var("CODEX_RUST_TLS_BACKEND") {
            Ok(value) if value == "native" => Ok(Self::Native),
            Ok(value) if value == "rustls" => Ok(Self::Rustls),
            Err(std::env::VarError::NotPresent) => Ok(Self::Native),
            _ => Err(std::io::Error::new(
                std::io::ErrorKind::InvalidInput,
                "CODEX_RUST_TLS_BACKEND must be native or rustls",
            )),
        }
    }
}

#[derive(Default)]
struct AppState {
    clients: Mutex<HashMap<(u64, String), CachedClient>>,
    tls_backend: TlsBackend,
}

impl AppState {
    async fn client_for(&self, account_id: u64, proxy_url: &str) -> Result<Client, &'static str> {
        let key = (account_id, proxy_url.to_owned());
        let mut clients = self.clients.lock().await;
        let now = Instant::now();
        clients.retain(|_, cached| now.duration_since(cached.last_used) < CLIENT_IDLE_TTL);
        if let Some(cached) = clients.get_mut(&key) {
            cached.last_used = now;
            return Ok(cached.client.clone());
        }

        if clients.len() >= MAX_CLIENTS {
            if let Some(oldest) = clients
                .iter()
                .min_by_key(|(_, cached)| cached.last_used)
                .map(|(key, _)| key.clone())
            {
                clients.remove(&oldest);
            }
        }

        // Official Codex uses reqwest's platform TLS by default and has a
        // narrow rustls fallback. Preserve an explicit selection for probes;
        // do not replay streamed requests after handshake failures.
        let builder = match self.tls_backend {
            TlsBackend::Native => Client::builder().use_native_tls(),
            TlsBackend::Rustls => Client::builder().use_rustls_tls(),
        };
        let mut builder = builder
            .redirect(reqwest::redirect::Policy::none())
            .connect_timeout(Duration::from_secs(10))
            .pool_idle_timeout(Duration::from_secs(90))
            .pool_max_idle_per_host(16);
        if !proxy_url.is_empty() {
            if !allowed_proxy(proxy_url) {
                return Err("invalid_proxy");
            }
            builder = builder.proxy(Proxy::all(proxy_url).map_err(|_| "invalid_proxy")?);
        } else {
            // Respect the Go account's explicit direct route; never inherit an
            // unrelated HTTP_PROXY / HTTPS_PROXY from this container.
            builder = builder.no_proxy();
        }
        let client = builder.build().map_err(|_| "client_build_failed")?;
        clients.insert(
            key,
            CachedClient {
                client: client.clone(),
                last_used: now,
            },
        );
        Ok(client)
    }
}

fn allowed_target(raw: &str) -> Option<Url> {
    let url = Url::parse(raw).ok()?;
    if url.scheme() != "https"
        || url.host_str() != Some("chatgpt.com")
        || !matches!(url.port(), None | Some(443))
        || !url.path().starts_with("/backend-api/codex/")
        || !url.username().is_empty()
        || url.password().is_some()
        || url.fragment().is_some()
    {
        return None;
    }
    Some(url)
}

fn allowed_proxy(raw: &str) -> bool {
    let Ok(url) = Url::parse(raw) else {
        return false;
    };
    matches!(url.scheme(), "http" | "https" | "socks5" | "socks5h")
        && url.host_str().is_some()
        && url.fragment().is_none()
}

fn control_header<'a>(headers: &'a HeaderMap, key: &str) -> Option<&'a str> {
    headers.get(key)?.to_str().ok()
}

fn skip_header(name: &HeaderName) -> bool {
    let lower = name.as_str();
    lower.starts_with("x-codex-rust-")
        || matches!(
            lower,
            "host"
                | "connection"
                | "proxy-connection"
                | "keep-alive"
                | "transfer-encoding"
                | "upgrade"
                | "te"
                | "trailer"
                | "proxy-authorization"
                | "content-length"
        )
}

fn connection_header_names(headers: &HeaderMap) -> Vec<HeaderName> {
    headers
        .get_all("connection")
        .iter()
        .filter_map(|value| value.to_str().ok())
        .flat_map(|value| value.split(','))
        .filter_map(|value| HeaderName::from_bytes(value.trim().as_bytes()).ok())
        .collect()
}

fn simple_response(status: StatusCode, body: &'static str) -> Response<Body> {
    Response::builder()
        .status(status)
        .body(Body::from(body))
        .expect("valid static response")
}

fn internal_error(code: &'static str) -> Response<Body> {
    Response::builder()
        .status(StatusCode::BAD_GATEWAY)
        .header("x-codex-rust-error", code)
        .body(Body::empty())
        .expect("valid error response")
}

// Keep failure classification coarse and safe to send back over IPC. An error
// string could include proxy credentials or a URL and must never enter logs.
fn upstream_error_code(err: &reqwest::Error) -> &'static str {
    if err.is_timeout() {
        "timeout"
    } else if err.is_connect() {
        "connect_failed"
    } else if err.is_body() {
        "body_failed"
    } else if err.is_request() {
        "request_failed"
    } else {
        "send_failed"
    }
}

async fn handle(
    req: Request<Incoming>,
    state: Arc<AppState>,
) -> Result<Response<Body>, Infallible> {
    let result = match (req.method(), req.uri().path()) {
        (&Method::GET, "/health") => simple_response(StatusCode::OK, "ok"),
        (&Method::POST, "/forward") => forward(req, state).await,
        _ => simple_response(StatusCode::NOT_FOUND, "not found"),
    };
    Ok(result)
}

async fn forward(req: Request<Incoming>, state: Arc<AppState>) -> Response<Body> {
    let headers = req.headers();
    let request_connection_headers = connection_header_names(headers);
    let Some(target) = control_header(headers, "x-codex-rust-target").and_then(allowed_target)
    else {
        return internal_error("invalid_target");
    };
    let Some(method) = control_header(headers, "x-codex-rust-method")
        .and_then(|value| Method::from_bytes(value.as_bytes()).ok())
        .filter(|value| *value == Method::GET || *value == Method::POST)
    else {
        return internal_error("invalid_method");
    };
    let Some(account_id) = control_header(headers, "x-codex-rust-account")
        .and_then(|value| value.parse::<u64>().ok())
        .filter(|id| *id > 0)
    else {
        return internal_error("invalid_account");
    };
    let proxy_url = control_header(headers, "x-codex-rust-proxy").unwrap_or("");
    let client = match state.client_for(account_id, proxy_url).await {
        Ok(client) => client,
        Err(code) => return internal_error(code),
    };

    let is_get = method == Method::GET;
    let mut upstream = client.request(method, target);
    for (name, value) in headers.iter() {
        if !skip_header(name) && !request_connection_headers.contains(name) {
            upstream = upstream.header(name, value);
        }
    }
    // Go can know an exact JSON body length even though the IPC hop is
    // streaming. Preserve it upstream, as the official Codex JSON sender does.
    if !is_get {
        if let Some(body_length) = control_header(headers, "x-codex-rust-body-length")
            .and_then(|value| value.parse::<u64>().ok())
        {
            upstream = upstream.header(hyper::header::CONTENT_LENGTH, body_length);
        }
    }
    // GET requests have no upstream body. This also avoids sending a spurious
    // chunked transfer/body on HTTP/1.1 model-catalog requests.
    // POST uploads remain streamed without buffering in either process.
    let upstream_result = if is_get {
        upstream.send().await
    } else {
        let incoming_stream = req
            .into_body()
            .into_data_stream()
            .map_err(std::io::Error::other);
        upstream
            .body(reqwest::Body::wrap_stream(incoming_stream))
            .send()
            .await
    };
    let upstream_response = match upstream_result {
        Ok(response) => response,
        Err(err) => return internal_error(upstream_error_code(&err)),
    };

    let status = upstream_response.status();
    let upstream_version = match upstream_response.version() {
        Version::HTTP_2 => "h2",
        _ => "http1",
    };
    let mut response = Response::builder()
        .status(status)
        .header("x-codex-rust-upstream-proto", upstream_version);
    let response_connection_headers = connection_header_names(upstream_response.headers());
    for (name, value) in upstream_response.headers().iter() {
        if !skip_header(name) && !response_connection_headers.contains(name) {
            response = response.header(name, value);
        }
    }
    // Response chunk boundaries and backpressure follow the actual upstream
    // body stream, including SSE, until the Go reader closes the socket.
    let body = Body::from_stream(upstream_response.bytes_stream());
    response
        .body(body)
        .unwrap_or_else(|_| internal_error("invalid_response"))
}

#[tokio::main]
async fn main() -> std::io::Result<()> {
    let socket = std::env::var("CODEX_RUST_SOCKET").unwrap_or_else(|_| SOCKET_DEFAULT.to_owned());
    let socket_path = std::path::Path::new(&socket);
    if !socket_path.is_absolute() {
        return Err(std::io::Error::new(
            std::io::ErrorKind::InvalidInput,
            "socket must be absolute",
        ));
    }
    if socket_path.exists() {
        fs::remove_file(socket_path)?;
    }
    let listener = UnixListener::bind(socket_path)?;
    fs::set_permissions(socket_path, fs::Permissions::from_mode(0o600))?;
    let state = Arc::new(AppState {
        tls_backend: TlsBackend::from_env()?,
        ..Default::default()
    });

    let mut sigterm = tokio::signal::unix::signal(tokio::signal::unix::SignalKind::terminate())?;
    loop {
        tokio::select! {
            result = listener.accept() => {
                let (stream, _) = result?;
                let state = state.clone();
                tokio::spawn(async move {
                    let service = service_fn(move |request| handle(request, state.clone()));
                    let _ = hyper::server::conn::http1::Builder::new()
                        .serve_connection(TokioIo::new(stream), service)
                        .await;
                });
            }
            _ = sigterm.recv() => break,
            _ = tokio::signal::ctrl_c() => break,
        }
    }
    let _ = fs::remove_file(socket_path);
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn only_fixed_codex_https_destination_allowed() {
        assert!(allowed_target("https://chatgpt.com/backend-api/codex/responses").is_some());
        assert!(allowed_target("https://chatgpt.com/backend-api/codex/models?x=1").is_some());
        assert!(allowed_target("http://chatgpt.com/backend-api/codex/responses").is_none());
        assert!(
            allowed_target("https://chatgpt.com.evil.test/backend-api/codex/responses").is_none()
        );
        assert!(allowed_target("https://chatgpt.com/backend-api/other").is_none());
        assert!(allowed_target("https://user@chatgpt.com/backend-api/codex/responses").is_none());
    }

    #[test]
    fn proxy_schemes_and_internal_headers_are_restricted() {
        assert!(allowed_proxy("socks5h://localhost:1080"));
        assert!(allowed_proxy("https://example.org:8443"));
        assert!(!allowed_proxy("file:///etc/passwd"));
        assert!(skip_header(&HeaderName::from_static("x-codex-rust-proxy")));
        assert!(skip_header(&HeaderName::from_static("connection")));
        assert!(!skip_header(&HeaderName::from_static("authorization")));
        let mut headers = HeaderMap::new();
        headers.insert("connection", "keep-alive, x-secret-hop".parse().unwrap());
        let tokens = connection_header_names(&headers);
        assert!(tokens.contains(&HeaderName::from_static("x-secret-hop")));
    }
}
