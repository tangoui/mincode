//go:build darwin && cgo

package filewatch

/*
#cgo LDFLAGS: -framework CoreServices
#include <CoreServices/CoreServices.h>
#include <dispatch/dispatch.h>
#include <stdint.h>
#include <stdlib.h>

extern void fileWatchEvents(uintptr_t token, size_t count, char **paths, FSEventStreamEventFlags *flags);

static void fileWatchCallback(ConstFSEventStreamRef stream, void *info, size_t count, void *paths,
	const FSEventStreamEventFlags flags[], const FSEventStreamEventId ids[]) {
	fileWatchEvents((uintptr_t)info, count, (char **)paths, (FSEventStreamEventFlags *)flags);
}

static void fileWatchNoop(void *context) {}

typedef struct {
	FSEventStreamRef stream;
	dispatch_queue_t queue;
} fileWatchHandle;

static int fileWatchStart(uintptr_t token, const char *root, fileWatchHandle *handle) {
	CFStringRef path = CFStringCreateWithCString(NULL, root, kCFStringEncodingUTF8);
	if (path == NULL) return 0;
	CFArrayRef paths = CFArrayCreate(NULL, (const void **)&path, 1, &kCFTypeArrayCallBacks);
	CFRelease(path);
	if (paths == NULL) return 0;
	FSEventStreamContext context = {0, (void *)token, NULL, NULL, NULL};
	FSEventStreamRef stream = FSEventStreamCreate(NULL, fileWatchCallback, &context, paths,
		kFSEventStreamEventIdSinceNow, 0.1, kFSEventStreamCreateFlagNoDefer | kFSEventStreamCreateFlagWatchRoot);
	CFRelease(paths);
	if (stream == NULL) return 0;
	dispatch_queue_t queue = dispatch_queue_create("com.mincode.filewatch", DISPATCH_QUEUE_SERIAL);
	FSEventStreamSetDispatchQueue(stream, queue);
	if (!FSEventStreamStart(stream)) {
		FSEventStreamInvalidate(stream);
		FSEventStreamRelease(stream);
		dispatch_release(queue);
		return 0;
	}
	handle->stream = stream;
	handle->queue = queue;
	return 1;
}

static void fileWatchStop(fileWatchHandle *handle) {
	FSEventStreamStop(handle->stream);
	FSEventStreamInvalidate(handle->stream);
	FSEventStreamRelease(handle->stream);
	// Drain a callback already queued before releasing its queue.
	dispatch_sync_f(handle->queue, NULL, fileWatchNoop);
	dispatch_release(handle->queue);
}
*/
import "C"

import (
	"errors"
	"sync"
	"unsafe"
)

// FSEvents reports directories whose entries changed; these flags ask for a
// subtree rescan because individual events were coalesced or dropped.
const fsEventsRescanFlags = C.kFSEventStreamEventFlagMustScanSubDirs |
	C.kFSEventStreamEventFlagUserDropped | C.kFSEventStreamEventFlagKernelDropped |
	C.kFSEventStreamEventFlagRootChanged

var fsEventTargets = struct {
	sync.Mutex
	next    uintptr
	targets map[uintptr]fsEventTarget
}{targets: map[uintptr]fsEventTarget{}}

type fsEventTarget struct {
	root    string
	changed watchChanged
}

type fsEventsSource struct {
	token  uintptr
	handle C.fileWatchHandle
	once   sync.Once
}

// NativeSource reports whether this platform has a native change source:
// FSEvents reports changed directories, so only those are rescanned.
const NativeSource = true

func startNativeWatch(root string, changed watchChanged) (watchSource, error) {
	fsEventTargets.Lock()
	fsEventTargets.next++
	token := fsEventTargets.next
	fsEventTargets.targets[token] = fsEventTarget{root: root, changed: changed}
	fsEventTargets.Unlock()

	source := &fsEventsSource{token: token}
	cRoot := C.CString(root)
	defer C.free(unsafe.Pointer(cRoot))
	if C.fileWatchStart(C.uintptr_t(token), cRoot, &source.handle) == 0 {
		fsEventTargets.Lock()
		delete(fsEventTargets.targets, token)
		fsEventTargets.Unlock()
		return nil, errors.New("FSEvents stream could not be started")
	}
	return source, nil
}

func (source *fsEventsSource) close() {
	source.once.Do(func() {
		C.fileWatchStop(&source.handle)
		fsEventTargets.Lock()
		delete(fsEventTargets.targets, source.token)
		fsEventTargets.Unlock()
	})
}

//export fileWatchEvents
func fileWatchEvents(token C.uintptr_t, count C.size_t, paths **C.char, flags *C.FSEventStreamEventFlags) {
	fsEventTargets.Lock()
	target, ok := fsEventTargets.targets[uintptr(token)]
	fsEventTargets.Unlock()
	if !ok || count == 0 {
		return
	}
	pathList := unsafe.Slice(paths, int(count))
	flagList := unsafe.Slice(flags, int(count))
	for index := range pathList {
		path := C.GoString(pathList[index])
		if flagList[index]&C.kFSEventStreamEventFlagRootChanged != 0 {
			path = target.root
		}
		reason := ""
		if flagList[index]&fsEventsRescanFlags != 0 {
			reason = "overflow"
		}
		if flagList[index]&(C.kFSEventStreamEventFlagRootChanged|C.kFSEventStreamEventFlagUnmount) != 0 {
			reason = "sourceLost"
		}
		target.changed(trimFSEventPath(path), flagList[index]&fsEventsRescanFlags != 0, reason)
	}
}

// trimFSEventPath drops the trailing slash FSEvents appends to directories.
func trimFSEventPath(path string) string {
	for len(path) > 1 && path[len(path)-1] == '/' {
		path = path[:len(path)-1]
	}
	return path
}
