package model

import "time"

type User struct {
	ID              string    `db:"id" json:"id"`
	GitHubID        int64     `db:"github_id" json:"github_id"`
	Username        string    `db:"username" json:"username"`
	Email           string    `db:"email" json:"email"`
	AvatarURL       string    `db:"avatar_url" json:"avatar_url"`
	Plan            string    `db:"plan" json:"plan"` // free | pro | team | business
	StripeCustomerID string   `db:"stripe_customer_id" json:"stripe_customer_id,omitempty"`
	StripeSubID     string    `db:"stripe_subscription_id" json:"stripe_subscription_id,omitempty"`
	CreatedAt       time.Time `db:"created_at" json:"created_at"`
	UpdatedAt       time.Time `db:"updated_at" json:"updated_at"`
}

type Project struct {
	ID          string    `db:"id" json:"id"`
	OwnerID     string    `db:"owner_id" json:"owner_id"`
	Name        string    `db:"name" json:"name"`
	Slug        string    `db:"slug" json:"slug"`
	Description string    `db:"description" json:"description"`
	CreatedAt   time.Time `db:"created_at" json:"created_at"`
	UpdatedAt   time.Time `db:"updated_at" json:"updated_at"`
}

type Environment struct {
	ID        string    `db:"id" json:"id"`
	ProjectID string    `db:"project_id" json:"project_id"`
	Name      string    `db:"name" json:"name"` // dev | staging | production
	CreatedAt time.Time `db:"created_at" json:"created_at"`
}

type Secret struct {
	ID            string    `db:"id" json:"id"`
	EnvironmentID string    `db:"environment_id" json:"environment_id"`
	EncryptedData []byte    `db:"encrypted_data" json:"-"`
	DataNonce     []byte    `db:"data_nonce" json:"-"`
	Version       int       `db:"version" json:"version"`
	PushedBy      string    `db:"pushed_by" json:"pushed_by"`
	CreatedAt     time.Time `db:"created_at" json:"created_at"`
}

type AuditLog struct {
	ID            string    `db:"id" json:"id"`
	UserID        string    `db:"user_id" json:"user_id"`
	ProjectID     string    `db:"project_id" json:"project_id"`
	EnvironmentID string    `db:"environment_id" json:"environment_id"`
	Action        string    `db:"action" json:"action"` // push | pull | invite | revoke
	Metadata      string    `db:"metadata" json:"metadata"`
	IPAddress     string    `db:"ip_address" json:"ip_address"`
	CreatedAt     time.Time `db:"created_at" json:"created_at"`
}

type TeamMember struct {
	ID        string    `db:"id" json:"id"`
	ProjectID string    `db:"project_id" json:"project_id"`
	UserID    string    `db:"user_id" json:"user_id"`
	Role      string    `db:"role" json:"role"` // owner | admin | member | viewer
	InvitedBy string    `db:"invited_by" json:"invited_by"`
	CreatedAt time.Time `db:"created_at" json:"created_at"`
}

type RefreshToken struct {
	ID        string    `db:"id" json:"id"`
	UserID    string    `db:"user_id" json:"user_id"`
	TokenHash string    `db:"token_hash" json:"-"`
	ExpiresAt time.Time `db:"expires_at" json:"expires_at"`
	Revoked   bool      `db:"revoked" json:"revoked"`
	CreatedAt time.Time `db:"created_at" json:"created_at"`
}

// Plan limits
type PlanLimits struct {
	MaxProjects    int
	MaxMembers     int
	HistoryDays    int
	HasAuditLogs   bool
	HasLeakDetect  bool
}

var Plans = map[string]PlanLimits{
	"free":     {MaxProjects: 1, MaxMembers: 3, HistoryDays: 7, HasAuditLogs: false, HasLeakDetect: false},
	"pro":      {MaxProjects: -1, MaxMembers: 5, HistoryDays: 30, HasAuditLogs: false, HasLeakDetect: true},
	"team":     {MaxProjects: -1, MaxMembers: 10, HistoryDays: 90, HasAuditLogs: false, HasLeakDetect: true},
	"business": {MaxProjects: -1, MaxMembers: -1, HistoryDays: 365, HasAuditLogs: true, HasLeakDetect: true},
}
