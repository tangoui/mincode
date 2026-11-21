package plugin

import (
	"encoding/json"
	"strings"

	"mincode/app/plugins/api"
)

// Staged storage writes are served through the host-method seam so they are
// listed by host.api.describe (SDK feature detection) and gated by the
// storage capability like the other host.storage.file.* methods.
const (
	storageWriteBeginMethod  = "host.storage.file.write.begin"
	storageWriteAppendMethod = "host.storage.file.write.append"
	storageWriteCommitMethod = "host.storage.file.write.commit"
	storageWriteAbortMethod  = "host.storage.file.write.abort"
)

func init() {
	registerHostMethod(storageWriteBeginMethod, nil, api.CapabilityStorage, hostStorageWriteBegin)
	registerHostMethod(storageWriteAppendMethod, nil, api.CapabilityStorage, hostStorageWriteAppend)
	registerHostMethod(storageWriteCommitMethod, nil, api.CapabilityStorage, hostStorageWriteCommit)
	registerHostMethod(storageWriteAbortMethod, nil, api.CapabilityStorage, hostStorageWriteAbort)
	// Stop, crash, disable, reload, uninstall and generation changes end the
	// runtime that owns the sessions; clear data and import stop it first.
	registerRecordLifecycleHook(func(_ *pluginManager, record *pluginRecord) {
		abortPluginStorageSessions(record.id)
	})
}

func storageRPCError(err error) *rpcError {
	return &rpcError{Code: -32002, Message: err.Error()}
}

func validStorageSessionID(id string) bool {
	return len(id) == 32 && strings.Trim(id, "0123456789abcdef") == ""
}

func hostStorageWriteBegin(runtimeHost *processRuntime, params json.RawMessage) (any, *rpcError) {
	var request struct {
		Path string `json:"path"`
	}
	if err := decodeHostUIParams(params, &request); err != nil {
		return nil, err
	}
	result, err := pluginStorageBegin(runtimeHost, runtimeHost.pluginID, request.Path)
	if err != nil {
		return nil, storageRPCError(err)
	}
	return result, nil
}

func hostStorageWriteAppend(runtimeHost *processRuntime, params json.RawMessage) (any, *rpcError) {
	var request struct {
		Session string `json:"session"`
		Offset  int64  `json:"offset"`
		Data    string `json:"data"`
	}
	if err := decodeHostUIParams(params, &request); err != nil {
		return nil, err
	}
	if !validStorageSessionID(request.Session) {
		return nil, hostUIParamsError("invalid storage write session")
	}
	data, err := decodeStorageData(request.Data)
	if err == nil {
		err = pluginStorageAppend(runtimeHost, runtimeHost.pluginID, request.Session, request.Offset, data)
	}
	if err != nil {
		return nil, storageRPCError(err)
	}
	return map[string]int64{"size": request.Offset + int64(len(data))}, nil
}

func hostStorageWriteCommit(runtimeHost *processRuntime, params json.RawMessage) (any, *rpcError) {
	var request struct {
		Session string `json:"session"`
		Size    *int64 `json:"size"`
	}
	if err := decodeHostUIParams(params, &request); err != nil {
		return nil, err
	}
	if !validStorageSessionID(request.Session) {
		return nil, hostUIParamsError("invalid storage write session")
	}
	expected := int64(-1)
	if request.Size != nil {
		if *request.Size < 0 {
			return nil, hostUIParamsError("invalid storage write size")
		}
		expected = *request.Size
	}
	if err := pluginStorageCommit(runtimeHost, runtimeHost.pluginID, request.Session, expected); err != nil {
		return nil, storageRPCError(err)
	}
	return true, nil
}

func hostStorageWriteAbort(runtimeHost *processRuntime, params json.RawMessage) (any, *rpcError) {
	var request struct {
		Session string `json:"session"`
	}
	if err := decodeHostUIParams(params, &request); err != nil {
		return nil, err
	}
	if !validStorageSessionID(request.Session) {
		return nil, hostUIParamsError("invalid storage write session")
	}
	if err := pluginStorageAbort(runtimeHost, runtimeHost.pluginID, request.Session); err != nil {
		return nil, storageRPCError(err)
	}
	return true, nil
}
