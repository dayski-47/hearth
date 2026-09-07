//! Names and small helpers shared by the Hearth services.

/// The Podman container name for a workspace. One definition, used by both
/// `hearth-agent` (which creates it) and `hearth-workspace` (which execs into
/// it).
pub fn workspace_container_name(id: &str) -> String {
    format!("hearth-ws-{id}")
}

#[cfg(test)]
mod tests {
    #[test]
    fn container_name_is_prefixed() {
        assert_eq!(
            super::workspace_container_name("abc-123"),
            "hearth-ws-abc-123"
        );
    }
}
