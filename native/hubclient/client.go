// Package hubclient provides a native, context-aware client for the GoDeck hub.
package hubclient

import (
 "bytes"
 "context"
 "crypto/tls"
 "crypto/x509"
 "encoding/json"
 "errors"
 "fmt"
 "io"
 "math/rand"
 "net"
 "net/http"
 "net/url"
 "strings"
 "time"

 "golang.org/x/net/websocket"
)

type Options struct { AllowHTTP bool; CAFilePEM []byte }
type Client struct { base string; tlsConfig *tls.Config; http *http.Client; Token string; DeviceToken string }
type Error struct { Code string `json:"code"`; Message string `json:"message"`; Status int `json:"-"` }
func(e *Error) Error() string { return e.Code+": "+e.Message }
func New(base string, o Options)(*Client,error) {
 u,e:=url.Parse(base);if e!=nil||u.Host==""||u.User!=nil||u.RawQuery!=""||u.Fragment!=""||(u.Scheme!="https"&&!(u.Scheme=="http"&&o.AllowHTTP)){return nil,errors.New("hub URL must be HTTPS, or explicitly allow HTTP")}
 { roots,e:=x509.SystemCertPool();if e!=nil||roots==nil{roots=x509.NewCertPool()};if len(o.CAFilePEM)>0&&!roots.AppendCertsFromPEM(o.CAFilePEM){return nil,errors.New("invalid CA certificate")};tlsCfg:=&tls.Config{MinVersion:tls.VersionTLS12,RootCAs:roots};h:=&http.Client{Transport:&http.Transport{TLSClientConfig:tlsCfg},Timeout:45*time.Second,CheckRedirect:func(*http.Request,[]*http.Request)error{return http.ErrUseLastResponse}};return &Client{base:strings.TrimRight(base,"/"),tlsConfig:tlsCfg,http:h},nil }
}
type Status struct { SetupRequired bool `json:"setup_required"`; Transports []string `json:"transports,omitempty"` }
func(c *Client) Status(ctx context.Context)(Status,error){var v Status;e:=c.call(ctx,"GET","/v1/status","",nil,&v,true);return v,e}
type Credentials struct { Username string `json:"username"`; Password string `json:"password"`; Code string `json:"code,omitempty"` }
type TokenResponse struct { Token string `json:"token"` }
type PairCode struct { Code string `json:"code"`; Expires time.Time `json:"expires"` }
type Device struct { ID string `json:"id"`; Name string `json:"name"`; Platform string `json:"platform"`; Accepting bool `json:"accepting"`; Capabilities []string `json:"capabilities"`; LastSeen time.Time `json:"last_seen"`; Online bool `json:"online"` }
type PairRequest struct { Code string `json:"code"`; Name string `json:"name"`; Platform string `json:"platform"` }
type PairResponse struct { DeviceID string `json:"device_id"`; DeviceToken string `json:"device_token"` }
type HeartbeatRequest struct { Name string `json:"name"`; Platform string `json:"platform"`; Accepting bool `json:"accepting"`; Capabilities []string `json:"capabilities"` }
type ICEServer struct { URLs []string `json:"urls"`; Username string `json:"username,omitempty"`; Credential string `json:"credential,omitempty"` }
type MediaInfo struct { Width int `json:"width"`; Height int `json:"height"`; Encoder string `json:"encoder"`; Hardware bool `json:"hardware"` }
type Session struct { FilesAllowed bool `json:"files_allowed"`; Control bool `json:"control"`; Media MediaInfo `json:"media"`; ID string `json:"id"`; DeviceID string `json:"device_id"`; State string `json:"state"`; Offer string `json:"offer,omitempty"`; Answer string `json:"answer,omitempty"`; Nonce string `json:"nonce"`; Sequence uint64 `json:"sequence"`; Expires time.Time `json:"expires"`; ICEServers []ICEServer `json:"ice_servers,omitempty"`; Transport string `json:"transport,omitempty"`; TransportReason string `json:"transport_reason,omitempty"` }
func(c *Client) call(ctx context.Context,method,path,token string,in,out any,retry bool)error {
 var body []byte;var err error;if in!=nil{body,err=json.Marshal(in);if err!=nil{return err}}
 for attempt:=0; ;attempt++ {req,e:=http.NewRequestWithContext(ctx,method,c.base+path,bytes.NewReader(body));if e!=nil{return e};if in!=nil{req.Header.Set("Content-Type","application/json")};if token!=""{req.Header.Set("Authorization","Bearer "+token)};resp,e:=c.http.Do(req);if e==nil {data,readErr:=io.ReadAll(io.LimitReader(resp.Body,1<<20));resp.Body.Close();if readErr!=nil{return readErr};if resp.StatusCode>=200&&resp.StatusCode<300 {if out!=nil&&len(data)>0{return json.Unmarshal(data,out)};return nil};var api Error;if json.Unmarshal(data,&api)!=nil||api.Code==""{api=Error{Code:"http_error",Message:http.StatusText(resp.StatusCode)}};api.Status=resp.StatusCode;if !retry||resp.StatusCode<500||attempt>=2{return &api};e=&api};if !retry||attempt>=2{return fmt.Errorf("hub unavailable: %w",e)};delay:=time.Duration(200*(1<<attempt)+rand.Intn(150))*time.Millisecond;timer:=time.NewTimer(delay);select{case <-ctx.Done():timer.Stop();return ctx.Err();case <-timer.C:}}
}
func(c *Client) Initialize(ctx context.Context,code,user,password string)error {var out TokenResponse;e:=c.call(ctx,"POST","/v1/setup","",Credentials{Username:user,Password:password,Code:code},&out,false);if e==nil{c.Token=out.Token};return e}
func(c *Client) Login(ctx context.Context,user,password string)error {var out TokenResponse;e:=c.call(ctx,"POST","/v1/login","",Credentials{Username:user,Password:password},&out,false);if e==nil{c.Token=out.Token};return e}
func(c *Client) ChangePassword(ctx context.Context,oldPassword,newPassword string)error{return c.call(ctx,"POST","/v1/password",c.Token,map[string]string{"old_password":oldPassword,"new_password":newPassword},nil,false)}
func(c *Client) Logout(ctx context.Context)error{e:=c.call(ctx,"POST","/v1/logout",c.Token,struct{}{},nil,false);if e==nil{c.Token=""};return e}
func(c *Client) CreatePairCode(ctx context.Context)(PairCode,error){var v PairCode;e:=c.call(ctx,"POST","/v1/pair-code",c.Token,struct{}{},&v,false);return v,e}
func(c *Client) Pair(ctx context.Context,req PairRequest)(PairResponse,error){var v PairResponse;e:=c.call(ctx,"POST","/v1/pair","",req,&v,false);if e==nil{c.DeviceToken=v.DeviceToken};return v,e}
func(c *Client) Heartbeat(ctx context.Context,req HeartbeatRequest)error{return c.call(ctx,"POST","/v1/heartbeat",c.DeviceToken,req,nil,true)}
func(c *Client) ListDevices(ctx context.Context)([]Device,error){var v []Device;e:=c.call(ctx,"GET","/v1/devices",c.Token,nil,&v,true);return v,e}
func(c *Client) RenameDevice(ctx context.Context,id,name string)error{return c.call(ctx,"POST","/v1/device/rename",c.Token,map[string]string{"id":id,"name":name},nil,false)}
func(c *Client) RevokeDevice(ctx context.Context,id string)error{return c.call(ctx,"POST","/v1/device/revoke",c.Token,map[string]string{"id":id},nil,false)}
func(c *Client) BaseURL() string { return c.base }
func(c *Client) CreateSession(ctx context.Context,id string)(Session,error){var v Session;e:=c.call(ctx,"POST","/v1/session/create",c.Token,map[string]string{"device_id":id},&v,false);return v,e}
func(c *Client) SubmitOffer(ctx context.Context,id,offer string)error{return c.call(ctx,"POST","/v1/session/offer",c.Token,map[string]string{"id":id,"offer":offer},nil,false)}
func(c *Client) SubmitOfferWithTransport(ctx context.Context,id,offer,transport,reason string)error{
	if transport=="" && reason=="" {
		return c.SubmitOffer(ctx,id,offer)
	}
	payload:=map[string]string{"id":id,"offer":offer,"transport":transport}
	if reason!="" {
		payload["transport_reason"]=reason
	}
	return c.call(ctx,"POST","/v1/session/offer",c.Token,payload,nil,false)
}
func(c *Client) GetSession(ctx context.Context,id string)(Session,error){return c.getSession(ctx,id,c.Token)}
func(c *Client) GetDeviceSession(ctx context.Context,id string)(Session,error){return c.getSession(ctx,id,c.DeviceToken)}
func(c *Client) getSession(ctx context.Context,id,token string)(Session,error){var v Session;e:=c.call(ctx,"POST","/v1/session/get",token,map[string]string{"id":id},&v,true);return v,e}
func(c *Client) WaitInvitations(ctx context.Context)(Session,error){var v Session;e:=c.call(ctx,"POST","/v1/session/wait",c.DeviceToken,struct{}{},&v,true);return v,e}
func(c *Client) AnswerSession(ctx context.Context,id,nonce,answer string,accept,control,filesAllowed bool,media MediaInfo)error{return c.call(ctx,"POST","/v1/session/answer",c.DeviceToken,map[string]any{"id":id,"nonce":nonce,"answer":answer,"accept":accept,"control":control,"files_allowed":filesAllowed,"media":media},nil,false)}
func(c *Client) EndSession(ctx context.Context,id string,device bool)error{token:=c.Token;if device{token=c.DeviceToken};return c.call(ctx,"POST","/v1/session/end",token,map[string]string{"id":id},nil,false)}

func(c *Client) dialWS(ctx context.Context, path string, token string) (*websocket.Conn, error) {
	u, err := url.Parse(c.base)
	if err != nil {
		return nil, err
	}
	wsScheme := "wss"
	if u.Scheme == "http" {
		wsScheme = "ws"
	}
	host := u.Host
	if _, _, splitErr := net.SplitHostPort(host); splitErr != nil {
		if wsScheme == "wss" {
			host = net.JoinHostPort(host, "443")
		} else {
			host = net.JoinHostPort(host, "80")
		}
	}
	wsURL := wsScheme + "://" + u.Host + path
	origin := c.base
	cfg, err := websocket.NewConfig(wsURL, origin)
	if err != nil {
		return nil, err
	}
	if token != "" {
		cfg.Header.Set("Authorization", "Bearer "+token)
	}
	cfg.TlsConfig = c.tlsConfig
	var raw net.Conn
	if wsScheme == "wss" {
		dialer := &tls.Dialer{
			NetDialer: &net.Dialer{Timeout: 8 * time.Second},
			Config:    c.tlsConfig,
		}
		raw, err = dialer.DialContext(ctx, "tcp", host)
	} else {
		dialer := &net.Dialer{Timeout: 8 * time.Second}
		raw, err = dialer.DialContext(ctx, "tcp", host)
	}
	if err != nil {
		return nil, err
	}
	ws, err := websocket.NewClient(cfg, raw)
	if err != nil {
		_ = raw.Close()
		return nil, err
	}
	return ws, nil
}

// ProbeWS checks WebSocket relay reachability and round-trip latency against the hub.
func(c *Client) ProbeWS(ctx context.Context) (bool, time.Duration) {
	ws, err := c.dialWS(ctx, "/v1/session/ws?probe=1", c.Token)
	if err != nil {
		return false, 0
	}
	defer ws.Close()
	t0 := time.Now()
	_ = ws.SetDeadline(time.Now().Add(1500 * time.Millisecond))
	var reply []byte
	if err := websocket.Message.Receive(ws, &reply); err != nil {
		return false, 0
	}
	return true, time.Since(t0)
}

// DialSessionWS opens a WebSocket relay stream on the hub for either viewer or host.
func(c *Client) DialSessionWS(ctx context.Context, sessionID string, device bool) (*websocket.Conn, error) {
	token := c.Token
	if device {
		token = c.DeviceToken
	}
	return c.dialWS(ctx, "/v1/session/ws?id="+url.QueryEscape(sessionID), token)
}





