use hearth_proto::hearth::v1::{Workspace, WorkspaceState};

#[test]
fn constructs_core_types() {
    let ws = Workspace {
        workspace_id: "w1".into(),
        container_id: "c1".into(),
        state: WorkspaceState::Running as i32,
        host_id: "local".into(),
        message: String::new(),
    };
    assert_eq!(ws.state, WorkspaceState::Running as i32);
}
