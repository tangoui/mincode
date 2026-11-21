//go:build production

package core

// Production builds never default to loopback: the member cloud shares the public API origin.
const defaultMinCodeCloudBaseURL = DefaultMinCodeAPIBaseURL
