// Package mcpauthpolicy identifies the host-owned MCP OAuth token record.
//
// The record (access/refresh tokens and dynamically registered client
// credentials for remote MCP connectors) is read and written only by
// backend/mcpauth, encrypted with the machine key. The generic app_kv store
// commands must refuse the key so a renderer can neither read the ciphertext
// nor replace it, and store_load_all must not export it to config sync.
package mcpauthpolicy

const StorageKey = "mcp_oauth_v1"
