//go:build windows && amd64

package client

import (
 "encoding/json"
 "errors"
 "os"
 "path/filepath"
 "syscall"
 "unsafe"
)
type secretBlob struct { Size uint32; Data *byte }
var crypt32=syscall.NewLazyDLL("crypt32.dll")
var protectData=crypt32.NewProc("CryptProtectData")
var unprotectData=crypt32.NewProc("CryptUnprotectData")
var localFree=syscall.NewLazyDLL("kernel32.dll").NewProc("LocalFree")
type savedDevice struct { URL string `json:"url"`; User string `json:"user"`; ID string `json:"id"`; Token string `json:"token"` }
func deviceFile()(string,error){d,e:=os.UserConfigDir();if e!=nil{return "",e};return filepath.Join(d,"godeck","hub-device.dat"),nil}
func protectBytes(data []byte,encrypt bool)([]byte,error){if len(data)==0{return nil,errors.New("空设备凭据")};in:=secretBlob{Size:uint32(len(data)),Data:&data[0]};var out secretBlob;var result uintptr;var err error;if encrypt{result,_,err=protectData.Call(uintptr(unsafe.Pointer(&in)),0,0,0,0,1,uintptr(unsafe.Pointer(&out)))}else{result,_,err=unprotectData.Call(uintptr(unsafe.Pointer(&in)),0,0,0,0,1,uintptr(unsafe.Pointer(&out)))};if result==0{return nil,err};defer localFree.Call(uintptr(unsafe.Pointer(out.Data)));if out.Size>1<<20{return nil,errors.New("设备凭据过大")};return append([]byte(nil),unsafe.Slice(out.Data,int(out.Size))...),nil}
func saveDevice(d savedDevice)error{b,e:=json.Marshal(d);if e!=nil{return e};b,e=protectBytes(b,true);if e!=nil{return e};p,e:=deviceFile();if e!=nil{return e};if e=os.MkdirAll(filepath.Dir(p),0700);e!=nil{return e};return os.WriteFile(p,b,0600)}
func loadDevice(url,user string)(savedDevice,error){p,e:=deviceFile();if e!=nil{return savedDevice{},e};b,e:=os.ReadFile(p);if e!=nil{return savedDevice{},e};b,e=protectBytes(b,false);if e!=nil{return savedDevice{},e};var d savedDevice;if e=json.Unmarshal(b,&d);e!=nil{return savedDevice{},e};if d.URL!=url||d.User!=user||d.Token==""{return savedDevice{},errors.New("没有该账号的设备绑定")};return d,nil}
