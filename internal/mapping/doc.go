// Package mapping loads and validates the plane-forge-bridge configuration.
//
// The YAML file is the source of truth for layout. A small set of PFB_*
// environment variables override connection settings and the *names* of the
// env vars that hold secrets, so containers can be configured without
// rewriting the file. Actual token values never appear in YAML; the file
// only names which environment variable holds each secret, and the loader
// resolves them at startup.
//
// The package exposes Load (path-based) and LoadFromReader (testable
// variant). Both return a fully-resolved configuration with secrets pulled
// from the environment and all required fields validated.
package mapping
