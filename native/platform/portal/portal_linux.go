//go:build linux

// Package portal requests an authorized KDE/Wayland screen and input session.
package portal

import (
 "context"
 "crypto/rand"
 "encoding/hex"
 "errors"
 "fmt"
 "os"
 "strings"
 "sync"
 "time"

 "github.com/godbus/dbus/v5"
)

const destination = "org.freedesktop.portal.Desktop"
const desktopPath = dbus.ObjectPath("/org/freedesktop/portal/desktop")
const remote = "org.freedesktop.portal.RemoteDesktop"
const screen = "org.freedesktop.portal.ScreenCast"
const request = "org.freedesktop.portal.Request"
const session = "org.freedesktop.portal.Session"

type Stream struct { Node uint32; Properties map[string]dbus.Variant }
type Capture struct {conn *dbus.Conn; Handle dbus.ObjectPath; FD *os.File; Stream Stream; Devices uint32; closed chan struct{};once sync.Once}
func token()(string,error){var b [16]byte;if _,err:=rand.Read(b[:]);err!=nil{return "",err};return "godeck"+hex.EncodeToString(b[:]),nil}
func requestCall(ctx context.Context,conn *dbus.Conn,method string,args ...any)(map[string]dbus.Variant,error){
 t,err:=token();if err!=nil{return nil,err};opts:=map[string]dbus.Variant{"handle_token":dbus.MakeVariant(t)};args=append(args,opts)
 // Subscribe before the method call: a response may arrive before Call returns.
 signal:=make(chan *dbus.Signal,16);conn.Signal(signal);defer conn.RemoveSignal(signal)
 if err=conn.AddMatchSignal(dbus.WithMatchInterface(request),dbus.WithMatchMember("Response"));err!=nil{return nil,err};defer conn.RemoveMatchSignal(dbus.WithMatchInterface(request),dbus.WithMatchMember("Response"))
 var path dbus.ObjectPath;call:=conn.Object(destination,desktopPath).CallWithContext(ctx,method,0,args...);if call.Err!=nil{return nil,call.Err};if err=call.Store(&path);err!=nil{return nil,err}
 for {select{case <-ctx.Done():closeCtx,closeCancel:=context.WithTimeout(context.Background(),time.Second);_ = conn.Object(destination,path).CallWithContext(closeCtx,request+".Close",0).Err;closeCancel();return nil,ctx.Err();case sig,ok:=<-signal:if !ok{return nil,errors.New("桌面门户连接已关闭")};if sig.Path!=path||sig.Name!=request+".Response"||len(sig.Body)!=2{continue};var response uint32;var result map[string]dbus.Variant;if err=dbus.Store(sig.Body,&response,&result);err!=nil{return nil,err};if response!=0{return nil,errors.New("桌面共享授权被取消或拒绝")};return result,nil}}
}
// Open requests an interactive screen share and optionally keyboard/pointer access.
func Open(ctx context.Context,allowControl bool)(*Capture,error){
 conn,err:=dbus.ConnectSessionBus();if err!=nil{return nil,errors.New("无法连接桌面会话 D-Bus；请在已登录的 KDE 桌面中启动")};c:=&Capture{conn:conn,closed:make(chan struct{})};success:=false;defer func(){if !success{c.Close()}}()
 sessionToken,err:=token();if err!=nil{return nil,err}
 // CreateSession has both a request handle token and a session handle token.
 createMethod:=screen+".CreateSession";if allowControl{createMethod=remote+".CreateSession"};result,err:=createSession(ctx,conn,createMethod,sessionToken);if err!=nil{return nil,err};v,ok:=result["session_handle"];if !ok{return nil,errors.New("门户没有返回会话")};if err=dbus.Store([]any{v.Value()},&c.Handle);err!=nil{return nil,err}
 if allowControl {if _,err=requestOptions(ctx,conn,remote+".SelectDevices",c.Handle,map[string]dbus.Variant{"types":dbus.MakeVariant(uint32(3))});err!=nil{return nil,fmt.Errorf("无法选择控制权限：%w",err)}}
 if _,err=requestOptions(ctx,conn,screen+".SelectSources",c.Handle,map[string]dbus.Variant{"types":dbus.MakeVariant(uint32(1)),"multiple":dbus.MakeVariant(false),"cursor_mode":dbus.MakeVariant(uint32(2))});err!=nil{return nil,fmt.Errorf("无法选择共享屏幕：%w",err)}
 startMethod:=screen+".Start";if allowControl{startMethod=remote+".Start"};start,err:=requestCall(ctx,conn,startMethod,c.Handle,"");if err!=nil{return nil,err};if v,ok=start["devices"];ok{_ = dbus.Store([]any{v.Value()},&c.Devices)}
 v,ok=start["streams"];if !ok{return nil,errors.New("门户未返回屏幕流")};var streams []Stream;if err=dbus.Store([]any{v.Value()},&streams);err!=nil||len(streams)!=1{return nil,errors.New("无法读取门户屏幕流")};c.Stream=streams[0]
 var fd dbus.UnixFD;if err=conn.Object(destination,desktopPath).CallWithContext(ctx,screen+".OpenPipeWireRemote",0,c.Handle,map[string]dbus.Variant{}).Store(&fd);err!=nil{return nil,errors.New("无法打开已授权的 PipeWire 流")};c.FD=os.NewFile(uintptr(fd),"portal-pipewire")
 if err=conn.AddMatchSignal(dbus.WithMatchObjectPath(c.Handle),dbus.WithMatchInterface(session),dbus.WithMatchMember("Closed"));err!=nil{return nil,err};sig:=make(chan *dbus.Signal,8);conn.Signal(sig);go func(){defer conn.RemoveSignal(sig);for{select{case <-c.closed:return;case s,ok:=<-sig:if !ok{return};if s.Path==c.Handle&&s.Name==session+".Closed"{c.Close();return}}}}();success=true;return c,nil
}
func createSession(ctx context.Context,conn *dbus.Conn,method,sessionToken string)(map[string]dbus.Variant,error){return requestOptions(ctx,conn,method,map[string]dbus.Variant{"session_handle_token":dbus.MakeVariant(sessionToken)})}
func requestOptions(ctx context.Context,conn *dbus.Conn,method string,args ...any)(map[string]dbus.Variant,error){
 extra:=args[len(args)-1].(map[string]dbus.Variant);args=args[:len(args)-1];t,err:=token();if err!=nil{return nil,err};extra["handle_token"]=dbus.MakeVariant(t);args=append(args,extra)
 signal:=make(chan *dbus.Signal,16);conn.Signal(signal);defer conn.RemoveSignal(signal);if err=conn.AddMatchSignal(dbus.WithMatchInterface(request),dbus.WithMatchMember("Response"));err!=nil{return nil,err};defer conn.RemoveMatchSignal(dbus.WithMatchInterface(request),dbus.WithMatchMember("Response"))
 var path dbus.ObjectPath;call:=conn.Object(destination,desktopPath).CallWithContext(ctx,method,0,args...);if call.Err!=nil{return nil,call.Err};if err=call.Store(&path);err!=nil{return nil,err}
 for {select{case <-ctx.Done():closeCtx,closeCancel:=context.WithTimeout(context.Background(),time.Second);_ = conn.Object(destination,path).CallWithContext(closeCtx,request+".Close",0).Err;closeCancel();return nil,ctx.Err();case sig,ok:=<-signal:if !ok{return nil,errors.New("门户连接已关闭")};if sig.Path!=path||sig.Name!=request+".Response"||len(sig.Body)!=2{continue};var code uint32;var result map[string]dbus.Variant;if err=dbus.Store(sig.Body,&code,&result);err!=nil{return nil,err};if code!=0{return nil,errors.New("桌面共享授权被取消或拒绝")};return result,nil}}
}
func (c *Capture) Closed()<-chan struct{}{return c.closed}
func (c *Capture) Close(){c.once.Do(func(){close(c.closed);if c.conn!=nil&&c.Handle!=""{ctx,cancel:=context.WithTimeout(context.Background(),time.Second);_ = c.conn.Object(destination,c.Handle).CallWithContext(ctx,session+".Close",0).Err;cancel()};if c.FD!=nil{c.FD.Close()};if c.conn!=nil{c.conn.Close()}})}
func (c *Capture) Notify(method string,args ...any)error{if c.Devices==0{return errors.New("未授予远程操作权限")};if strings.Contains(method,"Pointer")&&c.Devices&1==0{return errors.New("未授予鼠标权限")};if strings.Contains(method,"Keyboard")&&c.Devices&2==0{return errors.New("未授予键盘权限")};all:=[]any{c.Handle,map[string]dbus.Variant{}};all=append(all,args...);ctx,cancel:=context.WithTimeout(context.Background(),time.Second);defer cancel();return c.conn.Object(destination,desktopPath).CallWithContext(ctx,remote+"."+method,dbus.FlagNoAutoStart,all...).Err}
