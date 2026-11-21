package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// JSON-RPC 2.0 conformance of the plugin process pipe beyond plain request /
// response lines: batches (§6) and the -32700 / -32600 replies (§5.1).
const (
	jsonrpcParseError     = -32700
	jsonrpcInvalidRequest = -32600
)

var jsonNull = json.RawMessage("null")

func protocolError(id json.RawMessage, code int) rpcEnvelope {
	message := "Invalid Request"
	if code == jsonrpcParseError {
		message = "Parse error"
	}
	return rpcEnvelope{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: message}}
}

// validRequestID: a request id must be a string, a number or null.
func validRequestID(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return false
	}
	switch raw[0] {
	case '"':
		var s string
		return json.Unmarshal(raw, &s) == nil
	case 'n':
		return string(raw) == "null"
	case '{', '[', 't', 'f':
		return false
	}
	var number json.Number
	return json.Unmarshal(raw, &number) == nil
}

// replyIDOf picks the id to echo for a message that did not decode into an
// envelope (e.g. a non-string method): its id when valid, otherwise null.
func replyIDOf(raw []byte) json.RawMessage {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) == nil && validRequestID(fields["id"]) {
		return fields["id"]
	}
	return jsonNull
}

func structuredParams(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	return len(raw) == 0 || raw[0] == '{' || raw[0] == '['
}

// invalidRPCMessage checks a decoded message. A non-empty problem means it must
// not be processed; reply is the -32600 answer for request-like messages (a
// malformed response is never answered).
//
// A message without a method is handled as a (possibly malformed) response and
// never answered: on this two-way pipe an error echoing its id could be taken
// by the plugin as the reply to one of its own pending host calls.
func invalidRPCMessage(message rpcEnvelope) (reply *rpcEnvelope, problem string) {
	if message.Method == "" {
		if message.JSONRPC != "2.0" {
			return nil, "ignored JSON-RPC response with unsupported version"
		}
		return nil, ""
	}
	replyID := jsonNull
	if len(message.ID) > 0 && validRequestID(message.ID) {
		replyID = message.ID
	}
	invalid := func(problem string) (*rpcEnvelope, string) {
		reply := protocolError(replyID, jsonrpcInvalidRequest)
		return &reply, problem
	}
	switch {
	case message.JSONRPC != "2.0":
		return invalid("invalid JSON-RPC request: unsupported version")
	case len(message.ID) > 0 && !validRequestID(message.ID):
		return invalid("invalid JSON-RPC request: id must be a string, number or null")
	case !structuredParams(message.Params):
		return invalid("invalid JSON-RPC request: params must be an object or array")
	}
	return nil, ""
}

// handleBatch serves a JSON-RPC batch from the plugin: responses go to their
// pending calls, requests run concurrently, and their replies are sent back as
// one array once all have answered. Notifications and responses get no entry;
// a batch with nothing to answer gets no reply at all.
func (runtimeHost *processRuntime) handleBatch(generation uint64, line []byte, busyDropped *bool) {
	var items []json.RawMessage
	if err := json.Unmarshal(line, &items); err != nil {
		runtimeHost.logMessage("error", "invalid JSON-RPC batch: "+truncateLog(string(line)))
		runtimeHost.replyNonBlocking(generation, protocolError(jsonNull, jsonrpcParseError))
		return
	}
	if len(items) == 0 {
		runtimeHost.logMessage("error", "invalid JSON-RPC batch: empty")
		runtimeHost.replyNonBlocking(generation, protocolError(jsonNull, jsonrpcInvalidRequest))
		return
	}
	replies := make([]*rpcEnvelope, len(items))
	var waiting sync.WaitGroup
	for index, item := range items {
		var message rpcEnvelope
		if err := json.Unmarshal(item, &message); err != nil {
			reply := protocolError(replyIDOf(item), jsonrpcInvalidRequest)
			replies[index] = &reply
			continue
		}
		if reply, problem := invalidRPCMessage(message); problem != "" {
			runtimeHost.logMessage("error", problem)
			replies[index] = reply
			continue
		}
		if message.Method == "" {
			runtimeHost.deliverResponse(generation, message, false)
			continue
		}
		if message.Method == pluginTaskChangedNotification && len(message.ID) == 0 {
			runtimeHost.taskChanged(generation, message.Params)
			continue
		}
		if message.Method == hostUIPostMessageMethod && len(message.ID) == 0 {
			// Inline like a single message, so view messages keep their order.
			runtimeHost.uiMessageNotification(generation, message.Params)
			continue
		}
		select {
		case runtimeHost.hostRequests <- struct{}{}:
		default:
			if len(message.ID) > 0 {
				reply := busyReply(message.ID)
				replies[index] = &reply
			} else if !*busyDropped {
				*busyDropped = true
				runtimeHost.logMessage("error", "plugin busy; batched notification dropped")
			}
			continue
		}
		hasID := len(message.ID) > 0
		if hasID {
			waiting.Add(1)
		}
		index, message := index, message
		go func() {
			defer func() { <-runtimeHost.hostRequests }()
			var once sync.Once
			answered := runtimeHost.serveHostRequest(generation, message, func(result any, rpcErr *rpcError) {
				if !hasID {
					return
				}
				once.Do(func() {
					reply := hostReply(message.ID, result, rpcErr)
					replies[index] = &reply
					waiting.Done()
				})
			})
			if !answered && hasID {
				once.Do(waiting.Done) // stale generation: nothing will be sent
			}
		}()
	}
	go func() {
		waiting.Wait()
		runtimeHost.sendBatchReply(generation, replies)
	}()
}

// sendBatchReply writes the collected replies as one JSON array within the
// message limit; results too large for it are replaced by -32005 errors.
//
// When even those errors do not fit (a batch of many requests echoing long
// ids), the replies are split over several arrays rather than dropped: each
// entry still reaches its pending call by id, which both SDK runtimes resolve
// per entry, instead of every call in the batch timing out.
func (runtimeHost *processRuntime) sendBatchReply(generation uint64, replies []*rpcEnvelope) {
	limit := runtimeHost.replyLimit()
	encodeItems := func(shrink bool) ([][]byte, int) {
		items := make([][]byte, 0, len(replies))
		total := 1 // '['
		for _, reply := range replies {
			if reply == nil {
				continue
			}
			item := *reply
			encoded, err := json.Marshal(item)
			if err != nil || len(encoded)+2 >= limit || (shrink && item.Error == nil) {
				encoded, _ = json.Marshal(rpcEnvelope{JSONRPC: "2.0", ID: item.ID, Error: &rpcError{Code: -32005, Message: "plugin rpc response exceeds 1 MiB"}})
			}
			items = append(items, encoded)
			total += len(encoded) + 1 // ',' or ']'
		}
		return items, total
	}
	items, total := encodeItems(false)
	if len(items) == 0 {
		return
	}
	if total >= limit {
		items, total = encodeItems(true)
	}
	var arrays [][]byte
	if total < limit {
		arrays = [][]byte{joinBatch(items)}
	} else {
		arrays = splitBatch(items, limit)
		runtimeHost.logMessage("error", fmt.Sprintf("plugin rpc batch response exceeds message limit; split into %d arrays", len(arrays)))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, encoded := range arrays {
		if err := runtimeHost.enqueueWrite(ctx, generation, runtimeHost.encodeMessage(encoded)); err != nil {
			runtimeHost.logMessage("error", fmt.Sprintf("plugin rpc batch response: %v", err))
			return
		}
	}
}

func joinBatch(items [][]byte) []byte {
	line := append([]byte{'['}, bytes.Join(items, []byte{','})...)
	return append(line, ']')
}

// splitBatch packs encoded entries, in order, into arrays each strictly below
// limit bytes. An entry that cannot fit an array on its own (an id close to
// the message limit) is dropped: nothing within the limit can carry it.
func splitBatch(items [][]byte, limit int) [][]byte {
	var arrays [][]byte
	var current [][]byte
	size := 1
	for _, item := range items {
		if len(item)+2 >= limit {
			continue
		}
		if len(current) > 0 && size+len(item)+1 >= limit {
			arrays = append(arrays, joinBatch(current))
			current, size = nil, 1
		}
		current = append(current, item)
		size += len(item) + 1
	}
	if len(current) > 0 {
		arrays = append(arrays, joinBatch(current))
	}
	return arrays
}
