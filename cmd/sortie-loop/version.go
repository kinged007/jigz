package main

// engineTag pins the engine release these loops run against. The
// workflows need the pi agent and github-pr tracker, which ship in the
// kinged007/sortie fork releases; re-pin here when a fork release adds
// what a workflow needs. The version used in asset and cache names is
// this tag without its leading "v" (see engineVersion).
const engineTag = "v1.27.0"

// version is this tool's own version, stamped at build time by GoReleaser
// (-X main.version). It is "dev" for a plain `go build`.
var version = "dev"
