package dto

// Setup2FARequest is the request body for enabling 2FA.
type Setup2FARequest struct {
	Code string `json:"code" binding:"required"`
}

// Verify2FARequest is the request body for verifying 2FA.
type Verify2FARequest struct {
	Code      string `json:"code" binding:"required"`
	FlowToken string `json:"flow_token,omitempty"`
}

// Setup2FAResponse is the response data for POST /api/user/2fa/setup (service.TwoFASetup).
type Setup2FAResponse struct {
	Secret      string   `json:"secret"`
	QRCodeData  string   `json:"qr_code_data"`
	BackupCodes []string `json:"backup_codes"`
	FlowToken   string   `json:"flow_token"`
	ExpiresAt   int64    `json:"expires_at"`
}

// TwoFAStatusData is the data field for GET /api/user/2fa/status.
type TwoFAStatusData struct {
	Enabled              bool `json:"enabled"`
	Locked               bool `json:"locked"`
	BackupCodesRemaining int  `json:"backup_codes_remaining,omitempty"`
}

// UniversalVerifyRequest is the request body for POST /api/verify (service.VerificationInput).
type UniversalVerifyRequest struct {
	Method            string `json:"method"`
	Scope             string `json:"scope"`
	Context           any    `json:"context,omitempty"`
	Code              string `json:"code,omitempty"`
	Password          string `json:"password,omitempty"`
	PasswordEncrypted string `json:"password_encrypted,omitempty"`
	EncryptionKeyID   string `json:"encryption_key_id,omitempty"`
}

// SecurityProof is the step-up proof a completed verification returns (service.SecurityProof).
type SecurityProof struct {
	ProofToken string `json:"proof_token"`
	ExpiresAt  int64  `json:"expires_at"`
	Method     string `json:"method"`
	Scope      string `json:"scope"`
}

// VerificationMethodOption is one entry of VerificationRequirements.Methods.
type VerificationMethodOption struct {
	Method    string `json:"method"`
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
}

// VerificationOAuthProvider is a linked provider that can prove a step-up scope.
type VerificationOAuthProvider struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
}

// VerificationRequirements is the data field for GET /api/verify/methods (service.VerificationRequirements).
type VerificationRequirements struct {
	Scope                     string                      `json:"scope"`
	Methods                   []VerificationMethodOption  `json:"methods"`
	OAuthProviders            []VerificationOAuthProvider `json:"oauth_providers"`
	PasswordEncryptionEnabled bool                        `json:"password_encryption_enabled"`
}

// LoginSessionView is one browser session (service.LoginSessionView).
type LoginSessionView struct {
	SID          string `json:"sid"`
	Current      bool   `json:"current"`
	LoginMethod  string `json:"login_method"`
	IP           string `json:"ip"`
	UserAgent    string `json:"user_agent"`
	CreatedAt    int64  `json:"created_at"`
	LastActiveAt int64  `json:"last_active_at"`
	ExpiresAt    int64  `json:"expires_at"`
}

// AuthRotationData is the data field of routes that bump the auth version and
// re-issue the session token. Empty when the caller used an access token.
type AuthRotationData struct {
	AccessToken     string           `json:"access_token,omitempty"`
	TokenType       string           `json:"token_type,omitempty"`
	AccessExpiresAt int64            `json:"access_expires_at,omitempty"`
	Session         LoginSessionView `json:"session,omitempty"`
}

// BackupCodesData is the data field for POST /api/user/2fa/backup_codes.
type BackupCodesData struct {
	AccessToken     string           `json:"access_token,omitempty"`
	TokenType       string           `json:"token_type,omitempty"`
	AccessExpiresAt int64            `json:"access_expires_at,omitempty"`
	Session         LoginSessionView `json:"session,omitempty"`
	BackupCodes     []string         `json:"backup_codes"`
}

// PasskeyVerifyBeginRequest is the request body for POST /api/user/passkey/verify/begin.
type PasskeyVerifyBeginRequest struct {
	RPID    string `json:"rp_id,omitempty"`
	Scope   string `json:"scope"`
	Context any    `json:"context,omitempty"`
}

// PasskeyFinishRequest is the request body for the passkey register and verify finish routes.
type PasskeyFinishRequest struct {
	FlowToken  string `json:"flow_token"`
	Credential any    `json:"credential" description:"WebAuthn credential response, binary fields base64url"`
}
