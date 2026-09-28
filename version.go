package avoinspector

// Version is the SDK library version, sent on the wire as libVersion (SPEC.md §7.3.3). It MUST be
// a plain SemVer string with no suffix. Maintainers: bump this constant on every release.
const Version = "1.1.0"

// SpecVersion is the version of avohq/spec-first-inspector-server-sdk this SDK implements.
const SpecVersion = "3.0.1"

// LibPlatform identifies this SDK on the wire: it is the libPlatform body field and the
// X-Avo-Client request header (SPEC.md §7.2, §7.3.1). Constant for the life of the process.
const LibPlatform = "go"
