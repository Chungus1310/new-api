package controller

import (
	"strings"

	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"

	"github.com/go-fuego/fuego"
)

// GetTermsAcceptance answers the newest terms version the caller accepted. The
// web app compares it with the version it publishes; API keys never see this.
func GetTermsAcceptance(c fuego.ContextNoBody) (*dto.Response[dto.TermsAcceptanceData], error) {
	latest, err := model.LatestTermsAcceptance(dto.UserID(c))
	if err != nil {
		return dto.Fail[dto.TermsAcceptanceData](err.Error())
	}
	if latest == nil {
		return dto.Ok(dto.TermsAcceptanceData{})
	}
	return dto.Ok(dto.TermsAcceptanceData{Version: latest.Version, AcceptedAt: latest.CreatedAt})
}

// AcceptTerms records the caller's acceptance of the version the dialog showed.
func AcceptTerms(c fuego.ContextWithBody[dto.TermsAcceptRequest]) (*dto.Response[dto.TermsAcceptanceData], error) {
	req, err := c.Body()
	if err != nil {
		return dto.Fail[dto.TermsAcceptanceData]("Invalid parameters")
	}
	shown := model.TermsShown{Version: strings.TrimSpace(req.Version), Locale: strings.TrimSpace(req.Locale), Text: strings.TrimSpace(req.Text)}
	if !shown.Valid() {
		return dto.Fail[dto.TermsAcceptanceData]("terms acceptance needs a version and the text shown")
	}
	userId := dto.UserID(c)
	if err := model.RecordTermsAcceptance(userId, model.TermsSourceDialog, shown); err != nil {
		return dto.Fail[dto.TermsAcceptanceData](err.Error())
	}
	latest, err := model.LatestTermsAcceptance(userId)
	if err != nil || latest == nil {
		return dto.Ok(dto.TermsAcceptanceData{Version: shown.Version})
	}
	return dto.Ok(dto.TermsAcceptanceData{Version: latest.Version, AcceptedAt: latest.CreatedAt})
}
