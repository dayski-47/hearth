fn main() -> Result<(), Box<dyn std::error::Error>> {
    let proto_root = "../../proto";
    tonic_build::configure()
        .build_server(true)
        .build_client(true)
        .compile_protos(
            &[
                "hearth/v1/common.proto",
                "hearth/v1/agent.proto",
                "hearth/v1/gateway.proto",
                "hearth/v1/workspace.proto",
            ],
            &[proto_root],
        )?;
    println!("cargo:rerun-if-changed={proto_root}");
    Ok(())
}
