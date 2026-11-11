//go:build linux

// Package hubstore implements the single-process Linux hub snapshot store.
package hubstore

import (
 "crypto/rand"
 "crypto/sha256"
 "crypto/subtle"
 "encoding/base64"
 "encoding/json"
 "errors"
 "fmt"
 "io"
 "os"
 "path/filepath"
 "sync"
 "syscall"
 "time"

 "golang.org/x/crypto/argon2"
)

const snapshotVersion=1
const maxSnapshotBytes=8<<20

type Account struct { ID string `json:"id"`; Username string `json:"username"`; Salt string `json:"salt"`; PasswordHash string `json:"password_hash"` }
type Device struct { ID string `json:"id"`; OwnerID string `json:"owner_id"`; Name string `json:"name"`; Platform string `json:"platform"`; TokenHash string `json:"token_hash"`; LastSeen time.Time `json:"last_seen"`; Accepting bool `json:"accepting"`; Capabilities []string `json:"capabilities,omitempty"` }
type Snapshot struct { Version int `json:"version"`; Owner *Account `json:"owner,omitempty"`; Devices map[string]Device `json:"devices"` }
type Store struct { mu sync.Mutex; dir string; lock *os.File; state Snapshot }

func Open(dir string)(*Store,error) {
 if err:=os.MkdirAll(dir,0700);err!=nil{return nil,err};info,err:=os.Lstat(dir);if err!=nil{return nil,err};if !info.IsDir()||info.Mode()&os.ModeSymlink!=0||info.Mode().Perm()&0077!=0{return nil,errors.New("数据目录必须是仅当前用户可访问的真实目录")}
 lock,err:=os.OpenFile(filepath.Join(dir,"hub.lock"),os.O_CREATE|os.O_RDWR,0600);if err!=nil{return nil,err};if err=syscall.Flock(int(lock.Fd()),syscall.LOCK_EX|syscall.LOCK_NB);err!=nil{lock.Close();return nil,errors.New("数据目录已被另一个 Hub 使用")}
 s:=&Store{dir:dir,lock:lock,state:Snapshot{Version:snapshotVersion,Devices:map[string]Device{}}};file,err:=os.Open(filepath.Join(dir,"snapshot.json"));if os.IsNotExist(err){return s,nil};if err!=nil{s.Close();return nil,err};data,err:=io.ReadAll(io.LimitReader(file,maxSnapshotBytes+1));file.Close();if err!=nil{s.Close();return nil,err};if len(data)>maxSnapshotBytes{s.Close();return nil,errors.New("Hub 数据文件过大")};if err=json.Unmarshal(data,&s.state);err!=nil||s.state.Version!=snapshotVersion||s.state.Devices==nil{s.Close();return nil,errors.New("Hub 数据文件损坏或版本不受支持；不会自动重置")};if s.state.Owner!=nil&&(s.state.Owner.ID==""||s.state.Owner.Username==""||s.state.Owner.Salt==""||s.state.Owner.PasswordHash==""){s.Close();return nil,errors.New("Hub 账户数据损坏")};for id,d:=range s.state.Devices{if s.state.Owner==nil||id==""||id!=d.ID||d.OwnerID!=s.state.Owner.ID||d.TokenHash==""{s.Close();return nil,errors.New("Hub 设备数据损坏")}};return s,nil
}
func (s *Store) Close()error {s.mu.Lock();defer s.mu.Unlock();if s.lock==nil{return nil};_ = syscall.Flock(int(s.lock.Fd()),syscall.LOCK_UN);err:=s.lock.Close();s.lock=nil;return err}
func (s *Store) Read()(Snapshot,error) {s.mu.Lock();defer s.mu.Unlock();return clone(s.state)}
func clone(v Snapshot)(Snapshot,error){var out Snapshot;b,err:=json.Marshal(v);if err==nil{err=json.Unmarshal(b,&out)};return out,err}
// Update serializes changes and publishes them in memory only after the snapshot is durable.
func (s *Store) Update(fn func(*Snapshot)error)error {
 s.mu.Lock();defer s.mu.Unlock();next,err:=clone(s.state);if err!=nil{return err};if err=fn(&next);err!=nil{return err};b,err:=json.Marshal(next);if err!=nil{return err};if len(b)>maxSnapshotBytes{return errors.New("Hub 数据超过存储上限")}
 f,err:=os.CreateTemp(s.dir,".snapshot-*");if err!=nil{return err};name:=f.Name();defer os.Remove(name);if err=f.Chmod(0600);err!=nil{f.Close();return err};if _,err=f.Write(b);err!=nil{f.Close();return err};if err=f.Sync();err!=nil{f.Close();return err};if err=f.Close();err!=nil{return err};if err=os.Rename(name,filepath.Join(s.dir,"snapshot.json"));err!=nil{return err};s.state=next;d,err:=os.Open(s.dir);if err!=nil{return fmt.Errorf("数据已写入但目录同步失败：%w",err)};err=d.Sync();d.Close();if err!=nil{return fmt.Errorf("数据已写入但目录同步失败：%w",err)};return nil
}
func RandomToken()(string,error){b:=make([]byte,32);if _,err:=io.ReadFull(rand.Reader,b);err!=nil{return "",err};return base64.RawURLEncoding.EncodeToString(b),nil}
func Digest(token string)string {sum:=sha256.Sum256([]byte(token));return base64.RawURLEncoding.EncodeToString(sum[:])}
func NewAccount(username,password string)(*Account,error) {
 if len(username)<3||len(username)>64||len(password)<12||len(password)>1024{return nil,errors.New("用户名须为 3–64 字节，密码须为 12–1024 字节")};id,err:=RandomToken();if err!=nil{return nil,err};salt:=make([]byte,16);if _,err=io.ReadFull(rand.Reader,salt);err!=nil{return nil,err};key:=argon2.IDKey([]byte(password),salt,3,64*1024,2,32);return &Account{ID:id,Username:username,Salt:base64.RawURLEncoding.EncodeToString(salt),PasswordHash:base64.RawURLEncoding.EncodeToString(key)},nil
}
func VerifyPassword(a *Account,password string)bool {if a==nil||len(password)>1024{return false};salt,e1:=base64.RawURLEncoding.DecodeString(a.Salt);want,e2:=base64.RawURLEncoding.DecodeString(a.PasswordHash);if e1!=nil||e2!=nil||len(salt)!=16||len(want)!=32{return false};got:=argon2.IDKey([]byte(password),salt,3,64*1024,2,32);return subtle.ConstantTimeCompare(got,want)==1}


