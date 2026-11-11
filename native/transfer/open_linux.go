//go:build linux

package transfer
import("os";"syscall")
func openRegular(path string)(*os.File,error){fd,err:=syscall.Open(path,syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC,0);if err!=nil{return nil,err};f:=os.NewFile(uintptr(fd),path);info,err:=f.Stat();if err!=nil||!info.Mode().IsRegular(){f.Close();return nil,os.ErrInvalid};return f,nil}
