// Package models defines the GORM entities persisted for the panel.
package models

import (
	"time"

	"gorm.io/gorm"
)

type AdminUser struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	Username     string    `gorm:"uniqueIndex;size:64" json:"username"`
	PasswordHash string    `json:"-"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type AccountStatus string

const (
	AccountUnverified AccountStatus = "unverified"
	AccountActive     AccountStatus = "active"
	AccountError      AccountStatus = "error"
)

// InstagramAccount is one brand Instagram account, added by logging in
// with its username and password. The password, the session and the
// optional per-account proxy URL (its own IP, which may embed proxy
// credentials) are stored AES-GCM encrypted; every request made for this
// account goes out through its own proxy, or the server's IP if none is set.
type InstagramAccount struct {
	ID          uint       `gorm:"primaryKey" json:"id"`
	Label       string     `gorm:"size:128" json:"label"`
	PasswordEnc string     `gorm:"type:text" json:"-"` // AES-GCM encrypted, used to log in again when Instagram ends the session
	SessionEnc  string     `gorm:"type:text" json:"-"` // AES-GCM encrypted session (device ids + cookies)
	LoggedInAt  *time.Time `json:"logged_in_at"`

	ProxyURLEnc    string     `gorm:"type:text" json:"-"`           // AES-GCM encrypted, base64
	ProxyDisplay   string     `gorm:"size:255" json:"proxy"`        // proxy URL with the password masked
	ProxyLastIP    string     `gorm:"size:64" json:"proxy_last_ip"` // outbound IP seen on the last proxy test
	ProxyCheckedAt *time.Time `json:"proxy_checked_at"`

	IGUserID          string        `gorm:"size:64" json:"ig_user_id"`
	Username          string        `gorm:"size:64" json:"username"`
	Name              string        `gorm:"size:128" json:"name"`
	AccountType       string        `gorm:"size:32" json:"account_type"`
	ProfilePictureURL string        `gorm:"type:text" json:"profile_picture_url"`
	FollowersCount    int           `json:"followers_count"`
	FollowsCount      int           `json:"follows_count"`
	MediaCount        int           `json:"media_count"`
	Status            AccountStatus `gorm:"size:16;default:unverified" json:"status"`
	LastError         string        `gorm:"type:text" json:"last_error"`
	LastSyncedAt      *time.Time    `json:"last_synced_at"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type JobType string

const (
	JobModerateComments JobType = "moderate_comments"
	JobPublishMedia     JobType = "publish_media"
	JobExportComments   JobType = "export_comments"
	JobBulkPublish      JobType = "bulk_publish" // one post to several accounts, one after another
)

type JobStatus string

const (
	JobPending   JobStatus = "pending"
	JobRunning   JobStatus = "running"
	JobPaused    JobStatus = "paused"
	JobCompleted JobStatus = "completed"
	JobFailed    JobStatus = "failed"
	JobCancelled JobStatus = "cancelled"
)

// Job is a queued background operation for one account, executed by the
// worker pool with a configurable number of concurrent workers and a delay
// between individual Graph API calls to respect rate limits.
type Job struct {
	ID          uint       `gorm:"primaryKey" json:"id"`
	AccountID   uint       `gorm:"index" json:"account_id"`
	MediaID     string     `gorm:"size:64" json:"media_id"`
	Type        JobType    `gorm:"size:32" json:"type"`
	Status      JobStatus  `gorm:"size:16;default:pending" json:"status"`
	ParamsJSON  string     `gorm:"type:text" json:"params_json"`
	WorkerCount int        `json:"worker_count"`
	DelayMs     int        `json:"delay_ms"`
	Total       int        `json:"total"`
	Processed   int        `json:"processed"`
	Succeeded   int        `json:"succeeded"`
	Failed      int        `json:"failed"`
	ResultPath  string     `gorm:"size:255" json:"result_path"`
	CreatedAt   time.Time  `json:"created_at"`
	StartedAt   *time.Time `json:"started_at"`
	FinishedAt  *time.Time `json:"finished_at"`
}

type JobLogLevel string

const (
	LogInfo  JobLogLevel = "info"
	LogWarn  JobLogLevel = "warn"
	LogError JobLogLevel = "error"
)

type JobLog struct {
	ID        uint        `gorm:"primaryKey" json:"id"`
	JobID     uint        `gorm:"index" json:"job_id"`
	Level     JobLogLevel `gorm:"size:8" json:"level"`
	Message   string      `gorm:"type:text" json:"message"`
	TargetID  string      `gorm:"size:64" json:"target_id"`
	CreatedAt time.Time   `json:"created_at"`
}

func AllModels() []interface{} {
	return []interface{}{
		&AdminUser{},
		&InstagramAccount{},
		&Job{},
		&JobLog{},
	}
}

func Migrate(db *gorm.DB) error {
	return db.AutoMigrate(AllModels()...)
}
