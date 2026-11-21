package plugin

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"

	"mincode/app/plugins/api"
)

// Process transports. "jsonl" (default) writes one JSON-RPC message per line.
// "framed" prefixes every message with kind:u8 | length:u32 big-endian so the
// same stdio pipes can also carry raw binary payloads between the plugin UI and
// the plugin process without base64:
//
//	kind 0: UTF-8 JSON-RPC message
//	kind 1: channelLength:u16 big-endian | channel UTF-8 | bytes
const (
	pluginTransportJSONL  = "jsonl"
	pluginTransportFramed = "framed"

	pluginFrameJSON   byte = 0
	pluginFrameBinary byte = 1

	pluginFrameHeaderBytes = 5
	// pluginBinaryMaxBytes bounds one binary frame; larger transfers must be
	// chunked by the plugin, which keeps UI bridge messages bounded too.
	pluginBinaryMaxBytes = 8 * 1024 * 1024
	pluginChannelMaxLen  = 128
)

var pluginChannelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$`)

func validPluginChannel(channel string) bool {
	return len(channel) <= pluginChannelMaxLen && pluginChannelPattern.MatchString(channel)
}

func (runtimeManifest ProcessRuntimeManifest) transport() string {
	if runtimeManifest.Transport == "" {
		return pluginTransportJSONL
	}
	return runtimeManifest.Transport
}

// checkInitializeIdentity rejects a process that reports another plugin or an
// unsupported protocol in its plugin.initialize result, which catches an entry
// that points at the wrong executable. SDKs older than the identity check omit
// both fields, so a result without them is accepted and reported as
// unidentified.
func checkInitializeIdentity(result json.RawMessage, manifest Manifest) (bool, error) {
	var handshake struct {
		RuntimeProtocolVersion *int `json:"runtimeProtocolVersion"`
		Plugin                 *struct {
			ID      string `json:"id"`
			Version string `json:"version"`
		} `json:"plugin"`
	}
	if json.Unmarshal(result, &handshake) != nil {
		return false, nil
	}
	if handshake.RuntimeProtocolVersion != nil && *handshake.RuntimeProtocolVersion != api.RuntimeProtocolVersion {
		return false, fmt.Errorf("plugin selected runtime protocol %d, host supports %d", *handshake.RuntimeProtocolVersion, api.RuntimeProtocolVersion)
	}
	if handshake.Plugin == nil {
		return false, nil
	}
	if handshake.Plugin.ID != manifest.ID {
		return false, fmt.Errorf("plugin process reports id %q, manifest declares %q", handshake.Plugin.ID, manifest.ID)
	}
	if handshake.Plugin.Version != "" && handshake.Plugin.Version != manifest.Version {
		return false, fmt.Errorf("plugin process reports version %q, manifest declares %q", handshake.Plugin.Version, manifest.Version)
	}
	return true, nil
}

// pluginFrame is one decoded message of the framed transport.
type pluginFrame struct {
	kind    byte
	json    []byte
	channel string
	data    []byte
}

func encodeJSONFrame(message []byte) []byte {
	frame := make([]byte, pluginFrameHeaderBytes+len(message))
	frame[0] = pluginFrameJSON
	binary.BigEndian.PutUint32(frame[1:5], uint32(len(message)))
	copy(frame[pluginFrameHeaderBytes:], message)
	return frame
}

func encodeBinaryFrame(channel string, data []byte) ([]byte, error) {
	if !validPluginChannel(channel) {
		return nil, errors.New("invalid binary channel name")
	}
	if len(data) > pluginBinaryMaxBytes {
		return nil, fmt.Errorf("binary payload exceeds %d bytes; chunk the transfer", pluginBinaryMaxBytes)
	}
	payloadLength := 2 + len(channel) + len(data)
	frame := make([]byte, pluginFrameHeaderBytes+payloadLength)
	frame[0] = pluginFrameBinary
	binary.BigEndian.PutUint32(frame[1:5], uint32(payloadLength))
	binary.BigEndian.PutUint16(frame[5:7], uint16(len(channel)))
	copy(frame[7:], channel)
	copy(frame[7+len(channel):], data)
	return frame, nil
}

// readPluginFrame reads one frame, enforcing the size limits before allocating
// the payload. JSON frames must be shorter than jsonLimit, the same stdout cap
// a JSONL line has (1 MiB unless a negotiated feature raised it).
func readPluginFrame(reader *bufio.Reader, jsonLimit int) (pluginFrame, error) {
	var header [pluginFrameHeaderBytes]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return pluginFrame{}, err
	}
	kind := header[0]
	length := binary.BigEndian.Uint32(header[1:5])
	switch kind {
	case pluginFrameJSON:
		if int64(length) >= int64(jsonLimit) {
			return pluginFrame{}, fmt.Errorf("plugin JSON frame exceeds %d bytes", jsonLimit)
		}
	case pluginFrameBinary:
		if length < 2 || length > uint32(2+pluginChannelMaxLen+pluginBinaryMaxBytes) {
			return pluginFrame{}, errors.New("plugin binary frame has an invalid length")
		}
	default:
		return pluginFrame{}, fmt.Errorf("unknown plugin frame kind %d", kind)
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return pluginFrame{}, err
	}
	if kind == pluginFrameJSON {
		return pluginFrame{kind: kind, json: payload}, nil
	}
	channelLength := int(binary.BigEndian.Uint16(payload[:2]))
	if 2+channelLength > len(payload) {
		return pluginFrame{}, errors.New("plugin binary frame channel exceeds the frame")
	}
	channel := string(payload[2 : 2+channelLength])
	if !validPluginChannel(channel) {
		return pluginFrame{}, errors.New("plugin binary frame has an invalid channel name")
	}
	data := payload[2+channelLength:]
	if len(data) > pluginBinaryMaxBytes {
		return pluginFrame{}, fmt.Errorf("plugin binary frame exceeds %d bytes", pluginBinaryMaxBytes)
	}
	return pluginFrame{kind: kind, channel: channel, data: data}, nil
}
