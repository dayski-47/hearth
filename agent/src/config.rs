use anyhow::{anyhow, Result};

#[derive(Clone, Debug)]
pub struct TlsPaths {
    pub ca: String,
    pub cert: String,
    pub key: String,
}

#[derive(Clone, Debug)]
pub struct Config {
    pub host_id: String,
    pub grpc_listen_addr: String,
    pub gateway_grpc_addr: String,
    pub advertise_addr: String,
    pub workspace_addr: String,
    pub podman_socket: Option<String>,
    pub host_mounts: Vec<String>,
    pub tls: TlsPaths,
}

fn req(key: &str) -> Result<String> {
    std::env::var(key).map_err(|_| anyhow!("{key} is required"))
}

/// Self-hoster-configured allowlist of host directories a workspace may
/// bind-mount instead of getting a fresh managed volume. Empty means the
/// feature is off - a fresh clone with no configuration behaves exactly as
/// it always has.
fn host_mounts() -> Vec<String> {
    std::env::var("HEARTH_HOST_MOUNTS")
        .ok()
        .map(|raw| {
            raw.split(',')
                .map(str::trim)
                .filter(|s| !s.is_empty())
                .map(str::to_string)
                .collect()
        })
        .unwrap_or_default()
}

pub fn load() -> Result<Config> {
    Ok(Config {
        host_id: std::env::var("HEARTH_HOST_ID").unwrap_or_else(|_| "local".into()),
        grpc_listen_addr: req("HEARTH_AGENT_GRPC_LISTEN_ADDR")?,
        gateway_grpc_addr: req("HEARTH_GATEWAY_GRPC_ADDR")?,
        advertise_addr: req("HEARTH_AGENT_ADDR")?,
        workspace_addr: req("HEARTH_WORKSPACE_ADDR")?,
        podman_socket: std::env::var("HEARTH_PODMAN_SOCKET")
            .ok()
            .filter(|s| !s.is_empty()),
        host_mounts: host_mounts(),
        tls: TlsPaths {
            ca: req("HEARTH_TLS_CA")?,
            cert: req("HEARTH_AGENT_TLS_CERT")?,
            key: req("HEARTH_AGENT_TLS_KEY")?,
        },
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn load_requires_gateway_addr() {
        // Serialize env access to avoid cross-test races.
        let _g = crate::test_env_lock();
        std::env::remove_var("HEARTH_GATEWAY_GRPC_ADDR");
        std::env::set_var("HEARTH_AGENT_GRPC_LISTEN_ADDR", "0.0.0.0:9091");
        std::env::set_var("HEARTH_AGENT_ADDR", "https://localhost:9091");
        std::env::set_var("HEARTH_WORKSPACE_ADDR", "https://localhost:9092");
        std::env::set_var("HEARTH_TLS_CA", "ca.pem");
        std::env::set_var("HEARTH_AGENT_TLS_CERT", "a.pem");
        std::env::set_var("HEARTH_AGENT_TLS_KEY", "a-key.pem");
        assert!(load().is_err());
    }

    #[test]
    fn host_mounts_defaults_empty() {
        let _g = crate::test_env_lock();
        std::env::remove_var("HEARTH_HOST_MOUNTS");
        assert!(host_mounts().is_empty());
    }

    #[test]
    fn host_mounts_splits_and_trims_commas() {
        let _g = crate::test_env_lock();
        std::env::set_var("HEARTH_HOST_MOUNTS", "/a, /b ,, /c");
        assert_eq!(host_mounts(), vec!["/a", "/b", "/c"]);
        std::env::remove_var("HEARTH_HOST_MOUNTS");
    }
}
