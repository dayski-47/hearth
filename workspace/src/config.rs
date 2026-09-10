use anyhow::{anyhow, Result};

#[derive(Clone, Debug)]
pub struct TlsPaths {
    pub ca: String,
    pub cert: String,
    pub key: String,
}

#[derive(Clone, Debug)]
pub struct Config {
    pub grpc_listen_addr: String,
    pub podman_socket: Option<String>,
    pub tls: TlsPaths,
    /// How long a shell is held open after its client drops before it is
    /// reaped. A reconnect inside this window re-attaches to the same shell.
    pub terminal_grace: std::time::Duration,
}

fn req(key: &str) -> Result<String> {
    std::env::var(key).map_err(|_| anyhow!("{key} is required"))
}

pub fn load() -> Result<Config> {
    Ok(Config {
        grpc_listen_addr: req("HEARTH_WORKSPACE_GRPC_LISTEN_ADDR")?,
        podman_socket: std::env::var("HEARTH_PODMAN_SOCKET")
            .ok()
            .filter(|s| !s.is_empty()),
        tls: TlsPaths {
            ca: req("HEARTH_TLS_CA")?,
            cert: req("HEARTH_WORKSPACE_TLS_CERT")?,
            key: req("HEARTH_WORKSPACE_TLS_KEY")?,
        },
        terminal_grace: std::time::Duration::from_secs(
            std::env::var("HEARTH_TERMINAL_GRACE_SECONDS")
                .ok()
                .and_then(|s| s.parse().ok())
                .unwrap_or(60),
        ),
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn load_requires_the_listen_addr() {
        // Serialize env access to avoid cross-test races.
        let _g = crate::test_env_lock();
        std::env::remove_var("HEARTH_WORKSPACE_GRPC_LISTEN_ADDR");
        std::env::set_var("HEARTH_TLS_CA", "ca.pem");
        std::env::set_var("HEARTH_WORKSPACE_TLS_CERT", "w.pem");
        std::env::set_var("HEARTH_WORKSPACE_TLS_KEY", "w-key.pem");
        assert!(load().is_err());
    }

    #[test]
    fn terminal_grace_defaults_to_sixty_seconds() {
        let _g = crate::test_env_lock();
        std::env::set_var("HEARTH_WORKSPACE_GRPC_LISTEN_ADDR", "127.0.0.1:0");
        std::env::set_var("HEARTH_TLS_CA", "ca.pem");
        std::env::set_var("HEARTH_WORKSPACE_TLS_CERT", "w.pem");
        std::env::set_var("HEARTH_WORKSPACE_TLS_KEY", "w-key.pem");

        std::env::remove_var("HEARTH_TERMINAL_GRACE_SECONDS");
        assert_eq!(
            load().unwrap().terminal_grace,
            std::time::Duration::from_secs(60)
        );

        std::env::set_var("HEARTH_TERMINAL_GRACE_SECONDS", "5");
        assert_eq!(
            load().unwrap().terminal_grace,
            std::time::Duration::from_secs(5)
        );
        std::env::remove_var("HEARTH_TERMINAL_GRACE_SECONDS");
    }
}
