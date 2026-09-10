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
        terminal_grace: std::time::Duration::from_secs(terminal_grace_seconds()),
    })
}

/// Parse `HEARTH_TERMINAL_GRACE_SECONDS`, warning and falling back to 60 on an
/// unparseable value, and clamping to `1..=3600` so a typo cannot disable the
/// reconnect grace or leak shells for an hour or more.
fn terminal_grace_seconds() -> u64 {
    match std::env::var("HEARTH_TERMINAL_GRACE_SECONDS") {
        Err(_) => 60,
        Ok(raw) => match raw.parse::<u64>() {
            Ok(n) => n.clamp(1, 3600),
            Err(_) => {
                tracing::warn!(
                    value = %raw,
                    "HEARTH_TERMINAL_GRACE_SECONDS is not a number; using 60"
                );
                60
            }
        },
    }
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

        // An out-of-range value is clamped, not taken literally.
        std::env::set_var("HEARTH_TERMINAL_GRACE_SECONDS", "99999");
        assert_eq!(
            load().unwrap().terminal_grace,
            std::time::Duration::from_secs(3600)
        );

        // A non-numeric value warns and falls back to the default.
        std::env::set_var("HEARTH_TERMINAL_GRACE_SECONDS", "banana");
        assert_eq!(
            load().unwrap().terminal_grace,
            std::time::Duration::from_secs(60)
        );
        std::env::remove_var("HEARTH_TERMINAL_GRACE_SECONDS");
    }
}
