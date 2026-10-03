# Personal configuration-preservation checkpoint

This branch derives from xiaobaigroup/ClashBox at
`e036a6a3097192ff74987ce7306eea2a03d6a3ac`. It preserves imported configuration
and referenced resources, requires an explicit nonempty application allowlist,
and waits for native VPN startup/protection acknowledgement.

The Windows build profile is unsigned and ARM64-only. Host tests and local
builds have passed; device routing, protection, and long-running stability
have not been verified. This is not the original upstream 1.7.4 binary.

The generated `proxy_core/libs/arm64-v8a/libflclash.so` is intentionally not
tracked on this branch. Build it from the matching wrapper, author core,
gVisor and OHOS Go sources, or restore the exact checkpoint asset and verify
its SHA-256 before packaging. Old binaries in upstream history are not a
substitute for this branch's matching build.

Upstream submodule entries are historical; do not recursively initialize them
as the tested dependency combination. The personal multi-repository workspace
manifest and build instructions are maintained in the private coordination
repository. Upstream origins are preserved; upstream licensing still applies.
