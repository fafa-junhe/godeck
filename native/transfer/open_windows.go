//go:build windows

package transfer
import("os";"syscall")
func openRegular(path string)(*os.File,error){p,err:=syscall.UTF16PtrFromString(path);if err!=nil{return nil,err};const openReparse=0x00200000;h,err:=syscall.CreateFile(p,syscall.GENERIC_READ,syscall.FILE_SHARE_READ,nil,syscall.OPEN_EXISTING,syscall.FILE_ATTRIBUTE_NORMAL|openReparse,0);if err!=nil{return nil,err};var info syscall.ByHandleFileInformation;if err=syscall.GetFileInformationByHandle(h,&info);err!=nil||info.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT!=0||info.FileAttributes&syscall.FILE_ATTRIBUTE_DIRECTORY!=0{syscall.CloseHandle(h);return nil,os.ErrInvalid};return os.NewFile(uintptr(h),path),nil}
