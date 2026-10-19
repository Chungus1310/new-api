package dto

// FreeConsentItem is one active consent to a :free model.
type FreeConsentItem struct {
	Model     string `json:"model"`
	Locale    string `json:"locale"`
	CreatedAt int64  `json:"created_at"`
	// False once a provider the consent did not cover serves the model.
	Covers bool `json:"covers"`
}

// FreeConsentListData is the response data for GET /api/user/self/free-consents.
type FreeConsentListData struct {
	Consents []FreeConsentItem `json:"consents"`
}

// FreeConsentGrantRequest records a consent; Text is the dialog exactly as shown.
type FreeConsentGrantRequest struct {
	Model  string `json:"model" validate:"required,max=255"`
	Locale string `json:"locale" validate:"max=16"`
	Text   string `json:"text" validate:"required,max=20000"`
}

// FreeConsentRevokeRequest ends the consent for one model.
type FreeConsentRevokeRequest struct {
	Model string `json:"model" validate:"required,max=255"`
}

// OwnPurposeObjectionData is the caller's Art. 21 GDPR objection to providers
// that use inputs for their own purposes.
type OwnPurposeObjectionData struct {
	Objection bool `json:"objection"`
}

// OwnPurposeObjectionRequest sets or clears the objection.
type OwnPurposeObjectionRequest struct {
	Objection *bool `json:"objection"`
}
