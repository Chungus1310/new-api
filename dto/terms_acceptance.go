package dto

// TermsAcceptanceData is the newest terms version the caller accepted; empty
// when none was recorded.
type TermsAcceptanceData struct {
	Version    string `json:"version"`
	AcceptedAt int64  `json:"accepted_at"`
}

// TermsAcceptRequest records an acceptance; Text is the dialog exactly as shown.
type TermsAcceptRequest struct {
	Version string `json:"version" validate:"required,max=16"`
	Locale  string `json:"locale" validate:"max=16"`
	Text    string `json:"text" validate:"required,max=20000"`
}
