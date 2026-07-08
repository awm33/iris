//! Iris engine core.
//!
//! Owns the performance-critical, DOM-free parts of the canvas and playback
//! engines: frame-graph evaluation, decode scheduling, compositing math,
//! color pipeline. Compiled to WASM (+WebGPU) for the browser and natively
//! (wgpu) for the server render service — one implementation, two targets,
//! which is what makes client preview ≡ server export structural rather than
//! test-enforced (TDD §2.3, §3.6).
//!
//! This crate compiles in CI (native + wasm32-unknown-unknown) from day 0,
//! even while nearly empty, to keep the seam honest. Hot paths migrate here
//! from the TS/WebGL2 compositor when profiling justifies (M5 checkpoint in
//! docs/design/04-implementation-plan.md).

/// Frame-graph schema version this core evaluates. The TS shell and the
/// server renderer both pin this; a mismatch is a build error, not a runtime
/// surprise.
pub const FRAME_GRAPH_VERSION: u32 = 0;

#[cfg(test)]
mod tests {
    #[test]
    fn compiles() {
        assert_eq!(super::FRAME_GRAPH_VERSION, 0);
    }
}
