//go:build linux

// Package hubauth supplies in-memory, expiring login sessions and attempt limits.
// It does not expose endpoints and must be used with origin checks and TLS.
package hubauth

import (
 "crypto/subtle"
 "errors"
 "net/http"
 "strings"
 "sync"
 "time"

 "godeck/internal/hubstore"
)

const sessionLife = 12*time.Hour
const maxSessions = 256
const maxBuckets = 4096

type Session struct { OwnerID string; Expires time.Time }
type bucket struct { count int; expires time.Time }
type Manager struct {mu sync.Mutex; sessions map[string]Session; attempts map[string]bucket; bootstrapHash string; bootstrapUntil time.Time}
func New(bootstrap string,expiry time.Time)*Manager{return &Manager{sessions:map[string]Session{},attempts:map[string]bucket{},bootstrapHash:hubstore.Digest(bootstrap),bootstrapUntil:expiry}}
func (m *Manager) AllowAttempt(key string,now time.Time)bool {m.mu.Lock();defer m.mu.Unlock();for k,b:=range m.attempts {if now.After(b.expires){delete(m.attempts,k)}};b,ok:=m.attempts[key];if !ok {if len(m.attempts)>=maxBuckets{return false};b=bucket{expires:now.Add(time.Minute)}};b.count++;m.attempts[key]=b;return b.count<=8}
func (m *Manager) ConsumeBootstrap(candidate string,now time.Time)bool {m.mu.Lock();defer m.mu.Unlock();if m.bootstrapHash==""||!now.Before(m.bootstrapUntil){return false};digest:=hubstore.Digest(candidate);if subtle.ConstantTimeCompare([]byte(digest),[]byte(m.bootstrapHash))!=1{return false};m.bootstrapHash="";return true}
func (m *Manager) Create(ownerID string,now time.Time)(string,error){m.mu.Lock();defer m.mu.Unlock();for k,s:=range m.sessions{if !now.Before(s.Expires){delete(m.sessions,k)}};if len(m.sessions)>=maxSessions{return "",errors.New("会话数量达到上限")};token,err:=hubstore.RandomToken();if err!=nil{return "",err};m.sessions[hubstore.Digest(token)]=Session{OwnerID:ownerID,Expires:now.Add(sessionLife)};return token,nil}
func (m *Manager) Lookup(token string,now time.Time)(Session,bool){m.mu.Lock();defer m.mu.Unlock();s,ok:=m.sessions[hubstore.Digest(token)];if !ok||!now.Before(s.Expires){delete(m.sessions,hubstore.Digest(token));return Session{},false};return s,true}
func (m *Manager) Revoke(token string){m.mu.Lock();defer m.mu.Unlock();delete(m.sessions,hubstore.Digest(token))}
func (m *Manager) RevokeOther(ownerID,keepToken string){m.mu.Lock();defer m.mu.Unlock();keep:=hubstore.Digest(keepToken);for k,s:=range m.sessions{if s.OwnerID==ownerID&&k!=keep{delete(m.sessions,k)}}}
// SameOrigin must be combined with a trusted, explicitly configured public origin;
// request Host and forwarding headers are not trusted as configuration.
func SameOrigin(r *http.Request,expected string)bool {origin:=r.Header.Get("Origin");return origin!=""&&subtle.ConstantTimeCompare([]byte(strings.TrimSuffix(origin,"/")),[]byte(strings.TrimSuffix(expected,"/")))==1}
func Cookie(token string,secure bool)*http.Cookie{return &http.Cookie{Name:"godeck_session",Value:token,Path:"/",HttpOnly:true,Secure:secure,SameSite:http.SameSiteStrictMode,MaxAge:int(sessionLife.Seconds())}}

// HasDigest checks a stored session identifier without retaining bearer secrets.
func (m *Manager) HasDigest(digest string,now time.Time)bool {m.mu.Lock();defer m.mu.Unlock();s,ok:=m.sessions[digest];if !ok||!now.Before(s.Expires){delete(m.sessions,digest);return false};return true}

