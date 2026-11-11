//go:build linux

// Package hubserver provides the single-owner hub HTTP API. Media never passes through it.
package hubserver

import (
 "crypto/hmac"
 "crypto/sha1"
 "crypto/subtle"
 "encoding/base64"
 "encoding/json"
 "errors"
 "io"
 "mime"
 "net"
 "net/http"
 "strconv"
 "strings"
 "sync"
 "time"

 "golang.org/x/net/websocket"
 "godeck/internal/hubauth"
 "godeck/internal/hubstore"
 "godeck/native/hubclient"
)

type Config struct { SetupCode string; SetupUntil time.Time; STUNURLs []string; TURNURLs []string; TURNSecret string }
type pair struct { owner string; expires time.Time }
type session struct { hubclient.Session; owner,login string }
type wsRelay struct {
 mu sync.Mutex
 viewer *websocket.Conn
 host *websocket.Conn
 ready chan struct{}
 readyOnce sync.Once
 done chan struct{}
 once sync.Once
}
func newWSRelay()*wsRelay{return &wsRelay{ready:make(chan struct{}),done:make(chan struct{})}}
func(r *wsRelay) close(){
 r.once.Do(func(){
  close(r.done)
  r.mu.Lock()
  v,h:=r.viewer,r.host
  r.viewer,r.host=nil,nil
  r.mu.Unlock()
  if v!=nil{_ = v.Close()}
  if h!=nil{_ = h.Close()}
 })
}
type Server struct { store *hubstore.Store; auth *hubauth.Manager; cfg Config; mu sync.Mutex; pairs map[string]pair; sessions map[string]*session; relays map[string]*wsRelay; stop chan struct{}; waiters chan struct{}; once sync.Once }
func New(store *hubstore.Store,cfg Config)*Server{if cfg.SetupCode!=""{cfg.SetupCode=hubstore.Digest(cfg.SetupCode)};s:=&Server{store:store,auth:hubauth.New("",time.Time{}),cfg:cfg,pairs:map[string]pair{},sessions:map[string]*session{},relays:map[string]*wsRelay{},stop:make(chan struct{}),waiters:make(chan struct{},256)};go func(){ticker:=time.NewTicker(time.Minute);defer ticker.Stop();for{select{case <-s.stop:return;case now:=<-ticker.C:s.mu.Lock();s.cleanup(now);s.mu.Unlock()}}}();return s}
func(s *Server) Close(){s.once.Do(func(){close(s.stop);s.mu.Lock();for id,r:=range s.relays{r.close();delete(s.relays,id)};s.mu.Unlock()})}
func fail(w http.ResponseWriter,status int,code,msg string){w.WriteHeader(status);_ = json.NewEncoder(w).Encode(map[string]string{"code":code,"message":msg})}
func read(w http.ResponseWriter,r *http.Request,v any)bool {mediaType,_,err:=mime.ParseMediaType(r.Header.Get("Content-Type"));if err!=nil||mediaType!="application/json"{fail(w,415,"content_type","application/json required");return false};r.Body=http.MaxBytesReader(w,r.Body,300<<10);d:=json.NewDecoder(r.Body);d.DisallowUnknownFields();if d.Decode(v)!=nil||d.Decode(&struct{}{})!=io.EOF{fail(w,400,"invalid_request","invalid JSON request");return false};return true}
func token(r *http.Request)string {v:=r.Header.Get("Authorization");if !strings.HasPrefix(v,"Bearer "){return ""};return strings.TrimPrefix(v,"Bearer ")}
func(s *Server) owner(r *http.Request)(string,bool){v,ok:=s.auth.Lookup(token(r),time.Now());return v.OwnerID,ok}
func(s *Server) device(r *http.Request)(hubstore.Device,bool){v:=token(r);if v==""{return hubstore.Device{},false};snap,e:=s.store.Read();if e!=nil{return hubstore.Device{},false};digest:=hubstore.Digest(v);for _,d:=range snap.Devices{if subtle.ConstantTimeCompare([]byte(d.TokenHash),[]byte(digest))==1{return d,true}};return hubstore.Device{},false}
func(s *Server) limit(r *http.Request)bool {ip,_,e:=net.SplitHostPort(r.RemoteAddr);if e!=nil{ip=r.RemoteAddr};return s.auth.AllowAttempt(r.URL.Path+":"+ip,time.Now())}
func(s *Server) closeRelayLocked(id string){if r,ok:=s.relays[id];ok{r.close();delete(s.relays,id)}}
func(s *Server) cleanup(now time.Time){for k,v:=range s.pairs{if !now.Before(v.expires){delete(s.pairs,k)}};for k,v:=range s.sessions{if (v.State=="preparing"||v.State=="pending"||v.State=="ready")&&!s.auth.HasDigest(v.login,now){v.State="ended";v.Sequence++;v.Expires=now.Add(time.Minute);s.closeRelayLocked(k)}else if !now.Before(v.Expires){if v.State=="preparing"||v.State=="pending"||v.State=="ready"{v.State="expired";v.Sequence++;v.Expires=now.Add(time.Minute);s.closeRelayLocked(k)}else{s.closeRelayLocked(k);delete(s.sessions,k)}}}}
func(s *Server) ice()[]hubclient.ICEServer {var out []hubclient.ICEServer;if len(s.cfg.STUNURLs)>0{out=append(out,hubclient.ICEServer{URLs:s.cfg.STUNURLs})};if len(s.cfg.TURNURLs)>0&&s.cfg.TURNSecret!=""{username:=strconv.FormatInt(time.Now().Add(30*time.Minute).Unix(),10)+":godeck";mac:=hmac.New(sha1.New,[]byte(s.cfg.TURNSecret));mac.Write([]byte(username));out=append(out,hubclient.ICEServer{URLs:s.cfg.TURNURLs,Username:username,Credential:base64.StdEncoding.EncodeToString(mac.Sum(nil))})};return out}
func validText(v string,max int)bool{return len(v)>0&&len(v)<=max&&!strings.ContainsAny(v,"\x00\r\n")}
func(s *Server) ServeHTTP(w http.ResponseWriter,r *http.Request){
 if r.URL.Path=="/v1/session/ws"&&r.Method=="GET"{s.serveWS(w,r);return}
 w.Header().Set("Content-Type","application/json; charset=utf-8");w.Header().Set("Cache-Control","no-store");if r.Method!="POST"&&!(r.Method=="GET"&&(r.URL.Path=="/v1/devices"||r.URL.Path=="/v1/status")){fail(w,405,"method","method not allowed");return}
 if r.URL.Path=="/v1/session/wait"{s.wait(w,r);return}
 s.mu.Lock();defer s.mu.Unlock();s.cleanup(time.Now());now:=time.Now()
 switch r.URL.Path {
 case "/v1/status":
  snap,e:=s.store.Read();if e!=nil{fail(w,500,"storage_error","storage read failed");return};json.NewEncoder(w).Encode(hubclient.Status{SetupRequired:snap.Owner==nil,Transports:[]string{"udp","tcp","websocket"}})
 case "/v1/setup":
  if !s.limit(r){fail(w,429,"rate_limited","try later");return};var x hubclient.Credentials;if !read(w,r,&x){return};snap,e:=s.store.Read();if e!=nil||snap.Owner!=nil||!now.Before(s.cfg.SetupUntil)||s.cfg.SetupCode==""||subtle.ConstantTimeCompare([]byte(hubstore.Digest(x.Code)),[]byte(s.cfg.SetupCode))!=1{fail(w,403,"setup_unavailable","setup unavailable");return};a,e:=hubstore.NewAccount(x.Username,x.Password);if e!=nil{fail(w,400,"invalid_credentials",e.Error());return};if e=s.store.Update(func(v *hubstore.Snapshot)error{if v.Owner!=nil{return errors.New("already initialized")};v.Owner=a;return nil});e!=nil{fail(w,500,"storage_error","storage write failed");return};s.cfg.SetupCode="";t,e:=s.auth.Create(a.ID,now);if e!=nil{fail(w,500,"session_error","cannot create session");return};json.NewEncoder(w).Encode(hubclient.TokenResponse{Token:t})
 case "/v1/login":
  if !s.limit(r){fail(w,429,"rate_limited","try later");return};var x hubclient.Credentials;if !read(w,r,&x){return};snap,e:=s.store.Read();if e!=nil||snap.Owner==nil||snap.Owner.Username!=x.Username||!hubstore.VerifyPassword(snap.Owner,x.Password){fail(w,401,"invalid_credentials","invalid credentials");return};t,e:=s.auth.Create(snap.Owner.ID,now);if e!=nil{fail(w,503,"session_limit","session limit reached");return};json.NewEncoder(w).Encode(hubclient.TokenResponse{Token:t})
 case "/v1/logout":
  owner,ok:=s.owner(r);if !ok{fail(w,401,"unauthorized","authentication required");return};for k,v:=range s.sessions{if v.owner==owner&&v.login==hubstore.Digest(token(r)){v.State="ended";v.Sequence++;v.Expires=now.Add(time.Minute);s.closeRelayLocked(k)}};s.auth.Revoke(token(r));json.NewEncoder(w).Encode(struct{}{})
 case "/v1/password":
  owner,ok:=s.owner(r);if !ok{fail(w,401,"unauthorized","authentication required");return};var x struct{Old string `json:"old_password"`;New string `json:"new_password"`};if !read(w,r,&x){return};snap,e:=s.store.Read();if e!=nil||!hubstore.VerifyPassword(snap.Owner,x.Old){fail(w,403,"invalid_credentials","invalid credentials");return};a,e:=hubstore.NewAccount(snap.Owner.Username,x.New);if e!=nil{fail(w,400,"invalid_credentials",e.Error());return};a.ID=owner;if e=s.store.Update(func(v *hubstore.Snapshot)error{v.Owner=a;return nil});e!=nil{fail(w,500,"storage_error","storage write failed");return};s.auth.RevokeOther(owner,token(r));for k,v:=range s.sessions{if v.owner==owner&&v.login!=hubstore.Digest(token(r)){v.State="ended";v.Sequence++;v.Expires=now.Add(time.Minute);s.closeRelayLocked(k)}};json.NewEncoder(w).Encode(struct{}{})
 case "/v1/pair-code":
  owner,ok:=s.owner(r);if !ok{fail(w,401,"unauthorized","authentication required");return};var x struct{};if !read(w,r,&x){return};if len(s.pairs)>=128{fail(w,429,"limit","too many pairing codes");return};code,e:=hubstore.RandomToken();if e!=nil{fail(w,500,"random_error","random generation failed");return};expiry:=now.Add(5*time.Minute);s.pairs[hubstore.Digest(code)]=pair{owner,expiry};json.NewEncoder(w).Encode(hubclient.PairCode{Code:code,Expires:expiry})
 case "/v1/pair":
  if !s.limit(r){fail(w,429,"rate_limited","try later");return};var x hubclient.PairRequest;if !read(w,r,&x){return};p,ok:=s.pairs[hubstore.Digest(x.Code)];if !ok||!validText(x.Name,128)||!validText(x.Platform,64){fail(w,403,"pair_failed","pairing failed");return};id,e:=hubstore.RandomToken();if e!=nil{fail(w,500,"random_error","random generation failed");return};secret,e:=hubstore.RandomToken();if e!=nil{fail(w,500,"random_error","random generation failed");return};e=s.store.Update(func(v *hubstore.Snapshot)error{if len(v.Devices)>=1024{return errors.New("device limit")};v.Devices[id]=hubstore.Device{ID:id,OwnerID:p.owner,Name:x.Name,Platform:x.Platform,TokenHash:hubstore.Digest(secret)};return nil});if e!=nil{fail(w,500,"storage_error","storage write failed");return};delete(s.pairs,hubstore.Digest(x.Code));json.NewEncoder(w).Encode(hubclient.PairResponse{DeviceID:id,DeviceToken:secret})
 case "/v1/heartbeat":
  d,ok:=s.device(r);if !ok{fail(w,401,"unauthorized","authentication required");return};var x hubclient.HeartbeatRequest;if !read(w,r,&x){return};if !validText(x.Name,128)||!validText(x.Platform,64)||len(x.Capabilities)>32{fail(w,400,"invalid_request","invalid device metadata");return};for _,v:=range x.Capabilities{if !validText(v,64){fail(w,400,"invalid_request","invalid capabilities");return}};e:=s.store.Update(func(v *hubstore.Snapshot)error{d.Platform=x.Platform;d.Accepting=x.Accepting;d.Capabilities=x.Capabilities;d.LastSeen=now;v.Devices[d.ID]=d;return nil});if e!=nil{fail(w,500,"storage_error","storage write failed");return};if !x.Accepting{for k,v:=range s.sessions{if v.DeviceID==d.ID{v.State="ended";v.Sequence++;v.Expires=now.Add(time.Minute);s.closeRelayLocked(k)}}};json.NewEncoder(w).Encode(struct{}{})
 case "/v1/devices":
  owner,ok:=s.owner(r);if !ok{fail(w,401,"unauthorized","authentication required");return};snap,e:=s.store.Read();if e!=nil{fail(w,500,"storage_error","storage read failed");return};out:=[]hubclient.Device{};for _,d:=range snap.Devices{if d.OwnerID==owner{out=append(out,hubclient.Device{ID:d.ID,Name:d.Name,Platform:d.Platform,Accepting:d.Accepting,Capabilities:d.Capabilities,LastSeen:d.LastSeen,Online:now.Sub(d.LastSeen)<45*time.Second})}};json.NewEncoder(w).Encode(out)
 case "/v1/device/rename","/v1/device/revoke":
  owner,ok:=s.owner(r);if !ok{fail(w,401,"unauthorized","authentication required");return};var x struct{ID string `json:"id"`;Name string `json:"name"`};if !read(w,r,&x){return};snap,_:=s.store.Read();d,ok:=snap.Devices[x.ID];if !ok||d.OwnerID!=owner{fail(w,404,"not_found","device not found");return};if r.URL.Path=="/v1/device/rename"&&!validText(x.Name,128){fail(w,400,"invalid_request","invalid name");return};e:=s.store.Update(func(v *hubstore.Snapshot)error{if r.URL.Path=="/v1/device/revoke"{delete(v.Devices,x.ID)}else{d.Name=x.Name;v.Devices[x.ID]=d};return nil});if e!=nil{fail(w,500,"storage_error","storage write failed");return};if r.URL.Path=="/v1/device/revoke"{for k,v:=range s.sessions{if v.DeviceID==x.ID{v.State="ended";v.Sequence++;v.Expires=now.Add(time.Minute);s.closeRelayLocked(k)}}};json.NewEncoder(w).Encode(struct{}{})
 case "/v1/session/create":
  owner,ok:=s.owner(r);if !ok{fail(w,401,"unauthorized","authentication required");return};var x struct{DeviceID string `json:"device_id"`};if !read(w,r,&x){return};snap,_:=s.store.Read();d,ok:=snap.Devices[x.DeviceID];if !ok||d.OwnerID!=owner{fail(w,404,"not_found","device not found");return};if !d.Accepting||now.Sub(d.LastSeen)>=45*time.Second{fail(w,409,"device_unavailable","device unavailable");return};for _,v:=range s.sessions{if v.DeviceID==d.ID&&(v.State=="preparing"||v.State=="pending"||v.State=="ready"){fail(w,409,"device_busy","device busy");return}};if len(s.sessions)>=1024{fail(w,429,"limit","session limit reached");return};id,e:=hubstore.RandomToken();if e!=nil{fail(w,500,"random_error","random generation failed");return};nonce,e:=hubstore.RandomToken();if e!=nil{fail(w,500,"random_error","random generation failed");return};v:=&session{Session:hubclient.Session{ID:id,DeviceID:d.ID,State:"preparing",Nonce:nonce,Sequence:1,Expires:now.Add(30*time.Second),ICEServers:s.ice()},owner:owner,login:hubstore.Digest(token(r))};s.sessions[id]=v;json.NewEncoder(w).Encode(v.Session)
 case "/v1/session/offer":
  owner,ok:=s.owner(r);if !ok{fail(w,401,"unauthorized","authentication required");return};var x struct{ID string `json:"id"`;Offer string `json:"offer"`;Transport string `json:"transport,omitempty"`;TransportReason string `json:"transport_reason,omitempty"`};if !read(w,r,&x){return};v,ok:=s.sessions[x.ID];if !ok||v.owner!=owner||v.login!=hubstore.Digest(token(r))||v.State!="preparing"{fail(w,404,"not_found","session not found");return};if len(x.Offer)==0||len(x.Offer)>256<<10{fail(w,400,"invalid_request","invalid offer");return};tr:=strings.ToLower(strings.TrimSpace(x.Transport));if tr==""{tr="udp"};if tr!="udp"&&tr!="tcp"&&tr!="websocket"&&tr!="auto"{fail(w,400,"invalid_request","invalid transport");return};v.Offer=x.Offer;v.Transport=tr;v.TransportReason=x.TransportReason;v.State="pending";v.Expires=now.Add(2*time.Minute);v.Sequence++;json.NewEncoder(w).Encode(struct{}{})
 case "/v1/session/get":
  var x struct{ID string `json:"id"`};if !read(w,r,&x){return};v,ok:=s.sessions[x.ID];if !ok{fail(w,404,"not_found","session not found");return};owner,ownerOK:=s.owner(r);d,deviceOK:=s.device(r);if !(ownerOK&&v.owner==owner&&v.login==hubstore.Digest(token(r)))&&!(deviceOK&&v.DeviceID==d.ID){fail(w,404,"not_found","session not found");return};json.NewEncoder(w).Encode(v.Session)
 case "/v1/session/answer":
  d,ok:=s.device(r);if !ok{fail(w,401,"unauthorized","authentication required");return};var x struct{ID string `json:"id"`;Nonce string `json:"nonce"`;Answer string `json:"answer"`;Accept bool `json:"accept"`;Control bool `json:"control"`;FilesAllowed bool `json:"files_allowed"`;Media hubclient.MediaInfo `json:"media"`};if !read(w,r,&x){return};v,ok:=s.sessions[x.ID];if !ok||v.DeviceID!=d.ID||v.State!="pending"||subtle.ConstantTimeCompare([]byte(v.Nonce),[]byte(x.Nonce))!=1{fail(w,404,"not_found","session not found");return};if x.Accept&&(len(x.Answer)==0||len(x.Answer)>256<<10){fail(w,400,"invalid_request","invalid answer");return};if x.Accept{v.State="ready";v.Answer=x.Answer;v.Control=x.Control;v.FilesAllowed=x.FilesAllowed;v.Media=x.Media;v.Expires=now.Add(12*time.Hour)}else{v.State="rejected";v.Expires=now.Add(time.Minute);s.closeRelayLocked(x.ID)};v.Sequence++;json.NewEncoder(w).Encode(struct{}{})
 case "/v1/session/end":
  var x struct{ID string `json:"id"`};if !read(w,r,&x){return};v,ok:=s.sessions[x.ID];if !ok{fail(w,404,"not_found","session not found");return};owner,ownerOK:=s.owner(r);d,deviceOK:=s.device(r);if !(ownerOK&&v.owner==owner&&v.login==hubstore.Digest(token(r)))&&!(deviceOK&&v.DeviceID==d.ID){fail(w,404,"not_found","session not found");return};v.State="ended";v.Sequence++;v.Expires=now.Add(time.Minute);s.closeRelayLocked(x.ID);json.NewEncoder(w).Encode(struct{}{})
 default:fail(w,404,"not_found","endpoint not found")
 }
}
func(s *Server) serveWS(w http.ResponseWriter,r *http.Request){
 owner,ownerOK:=s.owner(r);d,deviceOK:=s.device(r)
 if !ownerOK&&!deviceOK{http.Error(w,"unauthorized",401);return}
 if r.URL.Query().Get("probe")=="1"{
  srv:=websocket.Server{Handshake:func(c *websocket.Config,req *http.Request)error{return nil},Handler:func(ws *websocket.Conn){_ = websocket.Message.Send(ws,[]byte("GDKPONG"))}}
  srv.ServeHTTP(w,r);return
 }
 id:=r.URL.Query().Get("id")
 s.mu.Lock()
 v,ok:=s.sessions[id]
 isViewer:=ok&&ownerOK&&v.owner==owner&&v.login==hubstore.Digest(token(r))
 isHost:=ok&&!isViewer&&deviceOK&&v.DeviceID==d.ID
 active:=ok&&(v.State=="preparing"||v.State=="pending"||v.State=="ready")
 if !active||(!isViewer&&!isHost){s.mu.Unlock();http.Error(w,"session not found",404);return}
 relay,exists:=s.relays[id]
 if !exists{relay=newWSRelay();s.relays[id]=relay}
 s.mu.Unlock()
 srv:=websocket.Server{Handshake:func(c *websocket.Config,req *http.Request)error{return nil},Handler:func(ws *websocket.Conn){
  ws.MaxPayloadBytes=64<<10
  _ = ws.SetDeadline(time.Time{})
  relay.mu.Lock()
  if isViewer{relay.viewer=ws}else{relay.host=ws}
  both:=relay.viewer!=nil&&relay.host!=nil
  relay.mu.Unlock()
  if both{relay.readyOnce.Do(func(){close(relay.ready)})}
  waitTimer:=time.NewTimer(30*time.Second)
  defer waitTimer.Stop()
  select{
  case <-relay.ready:
  case <-relay.done:return
  case <-s.stop:relay.close();return
  case <-waitTimer.C:relay.close();return
  }
  relay.mu.Lock()
  peer:=relay.host
  if isHost{peer=relay.viewer}
  relay.mu.Unlock()
  if peer==nil{relay.close();return}
  for{
   var pkt []byte
   if err:=websocket.Message.Receive(ws,&pkt);err!=nil{break}
   if len(pkt)==0{continue}
   if err:=websocket.Message.Send(peer,pkt);err!=nil{break}
  }
  relay.close()
  s.mu.Lock()
  if s.relays[id]==relay{delete(s.relays,id)}
  s.mu.Unlock()
 }}
 srv.ServeHTTP(w,r)
}
func(s *Server) wait(w http.ResponseWriter,r *http.Request){select{case s.waiters<-struct{}{}:defer func(){<-s.waiters}();default:fail(w,429,"limit","too many waiters");return};var x struct{};if !read(w,r,&x){return};deadline:=time.NewTimer(25*time.Second);defer deadline.Stop();ticker:=time.NewTicker(time.Second);defer ticker.Stop();for{s.mu.Lock();s.cleanup(time.Now());d,ok:=s.device(r);if !ok{s.mu.Unlock();fail(w,401,"unauthorized","authentication required");return};for _,v:=range s.sessions{if v.DeviceID==d.ID&&v.State=="pending"{copy:=v.Session;s.mu.Unlock();json.NewEncoder(w).Encode(copy);return}};s.mu.Unlock();select{case <-r.Context().Done():return;case <-s.stop:fail(w,503,"shutting_down","hub shutting down");return;case <-deadline.C:fail(w,408,"timeout","no invitation");return;case <-ticker.C:}}
}









