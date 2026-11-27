package externalmcp

import (
	"crypto/rand"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

const privateFullAccess = 0x1f01ff

func privateDescriptor() (*windows.SECURITY_DESCRIPTOR, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	return windows.SecurityDescriptorFromString("O:" + user.User.Sid.String() + "D:P(A;OICI;FA;;;" + user.User.Sid.String() + ")(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)")
}

// 逐级持有不共享删除权限的目录句柄，防止验证后祖先目录被替换为 junction。
func lockPrivateParents(path string) (func(), error) {
	if !filepath.IsAbs(path) || filepath.VolumeName(path) == "" {
		return nil, errors.New("MCP data path must be absolute")
	}
	var paths []string
	for current := filepath.Clean(path); ; current = filepath.Dir(current) {
		paths = append(paths, current)
		if filepath.Dir(current) == current {
			break
		}
	}
	var handles []windows.Handle
	release := func() {
		for i := len(handles) - 1; i >= 0; i-- {
			windows.CloseHandle(handles[i])
		}
	}
	for i := len(paths) - 1; i >= 0; i-- {
		handle, err := openPrivatePath(paths[i], windows.FILE_READ_ATTRIBUTES|windows.FILE_LIST_DIRECTORY)
		if err != nil {
			release()
			return nil, err
		}
		handles = append(handles, handle)
		var info windows.ByHandleFileInformation
		if err := windows.GetFileInformationByHandle(handle, &info); err != nil || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
			release()
			return nil, errors.New("MCP data path must not contain reparse points")
		}
	}
	return release, nil
}

func openPrivatePath(path string, access uint32) (windows.Handle, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return windows.InvalidHandle, err
	}
	return windows.CreateFile(name, access, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
}

func makePrivateDir(path string) error {
	sd, err := privateDescriptor()
	if err != nil {
		return err
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	sa := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	err = windows.CreateDirectory(name, &sa)
	runtime.KeepAlive(sd)
	if err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		return err
	}
	// 只为本次新建目录指定 ACL，绝不重写已有目录的权限。
	return verifyPrivatePath(path, true)
}

func verifyPrivatePath(path string, directory bool) error {
	handle, err := openPrivatePath(path, windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)
	return verifyPrivateHandle(handle, directory)
}

func verifyPrivateHandle(handle windows.Handle, directory bool) error {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || (info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0) != directory || (!directory && info.NumberOfLinks != 1) {
		return errors.New("unsafe MCP private file type")
	}
	sd, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	return verifyPrivateDescriptor(sd)
}

func verifyPrivateDescriptor(sd *windows.SECURITY_DESCRIPTOR) error {
	if sd == nil || !sd.IsValid() {
		return errors.New("missing MCP security descriptor")
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil || !owner.Equals(user.User.Sid) {
		return errors.New("MCP private path must be owned by the current user")
	}
	control, _, err := sd.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		return errors.New("MCP private path must have a protected DACL")
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil {
		return errors.New("missing MCP private DACL")
	}
	var userAccess windows.ACCESS_MASK
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		err := windows.GetAce(dacl, i, &ace)
		if err != nil {
			return err
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceFlags & ^uint8(windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT) != 0 || ace.Header.AceSize < uint16(unsafe.Sizeof(windows.ACCESS_ALLOWED_ACE{})) {
			return errors.New("unsupported MCP private ACE")
		}
		sid, err := privateACESID(ace)
		if err != nil {
			return err
		}
		if sid.Equals(user.User.Sid) {
			userAccess |= ace.Mask
		} else if !sid.IsWellKnown(windows.WinLocalSystemSid) && !sid.IsWellKnown(windows.WinBuiltinAdministratorsSid) {
			return errors.New("MCP private path grants access to another principal")
		}
	}
	if userAccess&privateFullAccess != privateFullAccess {
		return errors.New("MCP private path lacks current-user full access")
	}
	return nil
}

func privateACESID(ace *windows.ACCESS_ALLOWED_ACE) (*windows.SID, error) {
	const sidHeaderSize = 8
	const maxSubAuthorities = 15
	sidOffset := unsafe.Offsetof(ace.SidStart)
	if uintptr(ace.Header.AceSize) < sidOffset+sidHeaderSize {
		return nil, errors.New("truncated MCP private ACE SID header")
	}
	header := unsafe.Slice((*byte)(unsafe.Pointer(&ace.SidStart)), sidHeaderSize)
	count := uintptr(header[1])
	if count > maxSubAuthorities || sidHeaderSize+4*count > uintptr(ace.Header.AceSize)-sidOffset {
		return nil, errors.New("truncated MCP private ACE SID")
	}
	sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
	if !sid.IsValid() {
		return nil, errors.New("invalid MCP private ACE SID")
	}
	return sid, nil
}

func createPrivateTemp(directory string) (*os.File, error) {
	sd, err := privateDescriptor()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(directory, ".record-"+rand.Text())
	wide, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	sa := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	handle, err := windows.CreateFile(wide, windows.GENERIC_READ|windows.GENERIC_WRITE|windows.READ_CONTROL|windows.WRITE_DAC, 0, &sa, windows.CREATE_NEW, windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	runtime.KeepAlive(sd)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(handle), path), nil
}

func protectPrivateFile(file *os.File) error {
	sd, err := privateDescriptor()
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	if err := windows.SetSecurityInfo(windows.Handle(file.Fd()), windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		return err
	}
	runtime.KeepAlive(sd)
	return verifyPrivateHandle(windows.Handle(file.Fd()), false)
}

func readPrivateRecord(path string) ([]byte, error) {
	handle, err := openPrivatePath(path, windows.GENERIC_READ|windows.READ_CONTROL)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), path)
	defer file.Close()
	if err := verifyPrivateHandle(handle, false); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(file, 1025))
	if len(data) > 1024 {
		return nil, errors.New("unsafe MCP rendezvous record")
	}
	return data, err
}
