pub mod hearth {
    pub mod v1 {
        // tonic 0.12 generates streaming RPC signatures whose `Result` error variant
        // (`tonic::Status`) trips clippy's `result_large_err` lint. The generated code
        // is not ours to change, so silence the lint for the included module.
        #![allow(clippy::result_large_err)]

        tonic::include_proto!("hearth.v1");
    }
}
