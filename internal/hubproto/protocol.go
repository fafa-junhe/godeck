// Package hubproto contains the bounded wire vocabulary shared by hub and agent.
// Transport handlers must independently authenticate and authorize every operation.
package hubproto

import "time"

const (
 MaxFileBytes int64 = 100 << 20
 MaxScreenBytes int64 = 32 << 20
 MaxControlBytes int64 = 4096
 MaxJobsPerDevice = 16
 MaxConcurrentJobsPerDevice = 4
 MaxSpoolBytes int64 = 256 << 20
 SessionLease = 45*time.Second
 DeviceOnlineTTL = 45*time.Second
 JobLifetime = 90*time.Second
)

type Operation string
const (
 Status Operation = "status"
 Screen Operation = "screen"
 Input Operation = "input"
 Files Operation = "files"
 FileGet Operation = "file_get"
 FilePut Operation = "file_put"
)
func (op Operation) Valid() bool {switch op {case Status,Screen,Input,Files,FileGet,FilePut:return true};return false}
func (op Operation) RequestLimit()int64 {switch op {case Input:return MaxControlBytes;case FilePut:return MaxFileBytes;default:return 0}}
func (op Operation) ResponseLimit()int64 {switch op {case Screen:return MaxScreenBytes;case FileGet:return MaxFileBytes;case Files:return 4<<20;case Status:return 64<<10;default:return 64<<10}}

type Job struct { ID string `json:"id"`; SessionID string `json:"session_id"`; Operation Operation `json:"operation"`; Name string `json:"name,omitempty"`; Deadline time.Time `json:"deadline"`; InputSequence uint64 `json:"input_sequence,omitempty"` }
type Enrollment struct { Code string `json:"code"`; Name string `json:"name"`; Platform string `json:"platform"` }
type EnrollmentResponse struct { DeviceID string `json:"device_id"`; DeviceToken string `json:"device_token"` }
type Result struct { JobID string `json:"job_id"`; HTTPStatus int `json:"http_status"`; ContentType string `json:"content_type,omitempty"`; Size int64 `json:"size"` }
