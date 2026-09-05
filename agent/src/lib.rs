pub mod config;
pub mod engine;
pub mod gateway_client;
pub mod grpc;
pub mod registration;
pub mod tls;

#[cfg(test)]
pub(crate) fn test_env_lock() -> std::sync::MutexGuard<'static, ()> {
    static LOCK: std::sync::Mutex<()> = std::sync::Mutex::new(());
    LOCK.lock().unwrap_or_else(|e| e.into_inner())
}
