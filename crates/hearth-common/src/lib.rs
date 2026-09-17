//! Names and small helpers shared by the Hearth services.

/// The Podman container name for a workspace. One definition, used by both
/// `hearth-agent` (which creates it) and `hearth-workspace` (which execs into
/// it).
pub fn workspace_container_name(id: &str) -> String {
    format!("hearth-ws-{id}")
}

/// The UID `hearth-agent`/`hearth-workspace` are actually running as - the
/// self-hoster's own host user, since both run as `systemd --user` services.
/// `--userns=keep-id` pins this exact number into a workspace container's
/// namespace, but a container still runs as whatever the image's `USER`
/// says unless told otherwise; the workspace-base image hardcodes UID 1000.
/// A bind-mounted host directory is owned by the real host user, which is
/// this UID, not 1000 - the two only happen to match if the self-hoster's
/// own account is UID 1000. Container creation and exec both need this to
/// run as the host user instead, or a bind mount is read-only in practice.
pub fn host_uid() -> u32 {
    rustix::process::getuid().as_raw()
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
