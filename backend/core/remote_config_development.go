//go:build !production

package core

// Development builds use the same public cloud as packaged desktop builds unless overridden.
const defaultMinCodeCloudBaseURL = DefaultMinCodeAPIBaseURL
