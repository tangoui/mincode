//go:build darwin && cgo

package associations

/*
#cgo LDFLAGS: -framework CoreServices -framework CoreFoundation
#include <CoreServices/CoreServices.h>
#include <stdlib.h>
static CFStringRef mcType(const char *ext) {
 CFStringRef s=CFStringCreateWithCString(NULL,ext,kCFStringEncodingUTF8);
 CFStringRef t=UTTypeCreatePreferredIdentifierForTag(kUTTagClassFilenameExtension,s,NULL);
 CFRelease(s); return t;
}
static char *mcDefault(const char *value,int file) {
 CFStringRef s= file?mcType(value):CFStringCreateWithCString(NULL,value,kCFStringEncodingUTF8);
 if(!s) return NULL;
 CFStringRef app=file?LSCopyDefaultRoleHandlerForContentType(s,kLSRolesAll):LSCopyDefaultHandlerForURLScheme(s);
 CFRelease(s); if(!app) return NULL;
 CFIndex size=CFStringGetMaximumSizeForEncoding(CFStringGetLength(app),kCFStringEncodingUTF8)+1;
 char *out=malloc(size); if(!CFStringGetCString(app,out,size,kCFStringEncodingUTF8)){free(out);out=NULL;}
 CFRelease(app); return out;
}
static int mcSet(const char *value,int file,const char *bundle) {
 CFStringRef s=file?mcType(value):CFStringCreateWithCString(NULL,value,kCFStringEncodingUTF8);
 CFStringRef app=CFStringCreateWithCString(NULL,bundle,kCFStringEncodingUTF8);
 if(!s||!app){if(s)CFRelease(s);if(app)CFRelease(app);return -1;}
 CFURLRef url=NULL; OSStatus exists=LSFindApplicationForInfo(kLSUnknownCreator,app,NULL,NULL,&url);
 if(url)CFRelease(url);
 OSStatus r=exists==noErr?(file?LSSetDefaultRoleHandlerForContentType(s,kLSRolesAll,app):LSSetDefaultHandlerForURLScheme(s,app)):exists;
 CFRelease(s);CFRelease(app);return (int)r;
}
*/
import "C"
import (
	"fmt"
	"unsafe"

	"mincode/app/plugins/api/externalopen"
)

func fileFlag(t externalopen.Type) C.int {
	if t.Kind == "file" {
		return 1
	}
	return 0
}
func query(t externalopen.Type) (Status, error) {
	s := C.CString(t.Value)
	defer C.free(unsafe.Pointer(s))
	app := C.mcDefault(s, fileFlag(t))
	name := ""
	if app != nil {
		name = C.GoString(app)
		C.free(unsafe.Pointer(app))
	}
	return Status{Supported: true, Default: name == BundleID, Application: name}, nil
}
func set(t externalopen.Type, previous string, restore bool) error {
	target := BundleID
	if restore {
		target = previous
	}
	if target == "" {
		return fmt.Errorf("previous application is unavailable")
	}
	s, b := C.CString(t.Value), C.CString(target)
	defer C.free(unsafe.Pointer(s))
	defer C.free(unsafe.Pointer(b))
	if code := C.mcSet(s, fileFlag(t), b); code != 0 {
		return fmt.Errorf("Launch Services declined the default application request (%d); choose it in macOS settings or Finder Get Info", int(code))
	}
	return nil
}
