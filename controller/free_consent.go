package controller

import (
	"regexp"
	"strings"

	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/notify"

	"github.com/go-fuego/fuego"
)

var guestIdPattern = regexp.MustCompile(`^[A-Za-z0-9-]{16,64}$`)

// freeConsentSubject resolves who the consent belongs to. The guest routes take
// the guest cookie only from the chat BFF, so a browser cannot write consents
// for another guest id.
func freeConsentSubject(c dto.FuegoCtx, guest bool) (int, string, bool) {
	if !guest {
		return dto.UserID(c), "", true
	}
	ginCtx := dto.GinCtx(c)
	guestId := strings.TrimSpace(ginCtx.GetHeader("X-Guest-Id"))
	if !middleware.ResolvedByBFF(ginCtx) || !guestIdPattern.MatchString(guestId) {
		return 0, "", false
	}
	return 0, guestId, true
}

func listFreeConsents(c dto.FuegoCtx, guest bool) (*dto.Response[dto.FreeConsentListData], error) {
	userId, guestId, ok := freeConsentSubject(c, guest)
	if !ok {
		return dto.Fail[dto.FreeConsentListData]("guest consent routes are limited to the chat service")
	}
	consents, err := model.ActiveFreeConsents(userId, guestId)
	if err != nil {
		return dto.Fail[dto.FreeConsentListData](err.Error())
	}
	items := make([]dto.FreeConsentItem, 0, len(consents))
	for _, consent := range consents {
		items = append(items, dto.FreeConsentItem{Model: consent.Model, Locale: consent.Locale, CreatedAt: consent.CreatedAt, Covers: consent.Covers()})
	}
	return dto.Ok(dto.FreeConsentListData{Consents: items})
}

func grantFreeConsent(c fuego.ContextWithBody[dto.FreeConsentGrantRequest], guest bool) (*dto.Response[dto.MessageResponse], error) {
	userId, guestId, ok := freeConsentSubject(c, guest)
	if !ok {
		return dto.Fail[dto.MessageResponse]("guest consent routes are limited to the chat service")
	}
	req, err := c.Body()
	if err != nil {
		return dto.Fail[dto.MessageResponse]("Invalid parameters")
	}
	if strings.TrimSpace(req.Text) == "" {
		return dto.Fail[dto.MessageResponse]("consent needs the text shown")
	}
	scope := model.RecordedConsentScope(req.Model)
	// Every :free model still takes a consent: the chat gates on is_free until
	// the frontend reads consent_required.
	if !scope.Required && !notify.IsFreeModel(scope.Model) {
		return dto.Fail[dto.MessageResponse]("this model needs no consent")
	}
	if err := model.GrantFreeConsent(userId, guestId, scope, req.Locale, req.Text); err != nil {
		return dto.Fail[dto.MessageResponse](err.Error())
	}
	return dto.Ok(dto.MessageResponse{Message: "ok"})
}

func revokeFreeConsent(c fuego.ContextWithBody[dto.FreeConsentRevokeRequest], guest bool) (*dto.Response[dto.MessageResponse], error) {
	userId, guestId, ok := freeConsentSubject(c, guest)
	if !ok {
		return dto.Fail[dto.MessageResponse]("guest consent routes are limited to the chat service")
	}
	req, err := c.Body()
	if err != nil {
		return dto.Fail[dto.MessageResponse]("Invalid parameters")
	}
	if err := model.RevokeFreeConsent(userId, guestId, req.Model); err != nil {
		return dto.Fail[dto.MessageResponse](err.Error())
	}
	return dto.Ok(dto.MessageResponse{Message: "ok"})
}

func GetFreeConsents(c fuego.ContextNoBody) (*dto.Response[dto.FreeConsentListData], error) {
	return listFreeConsents(c, false)
}

func GrantFreeConsent(c fuego.ContextWithBody[dto.FreeConsentGrantRequest]) (*dto.Response[dto.MessageResponse], error) {
	return grantFreeConsent(c, false)
}

func RevokeFreeConsent(c fuego.ContextWithBody[dto.FreeConsentRevokeRequest]) (*dto.Response[dto.MessageResponse], error) {
	return revokeFreeConsent(c, false)
}

func GetGuestFreeConsents(c fuego.ContextNoBody) (*dto.Response[dto.FreeConsentListData], error) {
	return listFreeConsents(c, true)
}

func GrantGuestFreeConsent(c fuego.ContextWithBody[dto.FreeConsentGrantRequest]) (*dto.Response[dto.MessageResponse], error) {
	return grantFreeConsent(c, true)
}

func RevokeGuestFreeConsent(c fuego.ContextWithBody[dto.FreeConsentRevokeRequest]) (*dto.Response[dto.MessageResponse], error) {
	return revokeFreeConsent(c, true)
}

func getOwnPurposeObjection(c dto.FuegoCtx, guest bool) (*dto.Response[dto.OwnPurposeObjectionData], error) {
	userId, guestId, ok := freeConsentSubject(c, guest)
	if !ok {
		return dto.Fail[dto.OwnPurposeObjectionData]("guest consent routes are limited to the chat service")
	}
	if guest {
		objects, err := model.GuestObjectsToOwnPurposes(guestId)
		if err != nil {
			return dto.Fail[dto.OwnPurposeObjectionData](err.Error())
		}
		return dto.Ok(dto.OwnPurposeObjectionData{Objection: objects})
	}
	user, err := model.GetUserById(userId, true)
	if err != nil {
		return dto.Fail[dto.OwnPurposeObjectionData](err.Error())
	}
	return dto.Ok(dto.OwnPurposeObjectionData{Objection: user.GetSetting().OwnPurposeObjection})
}

func setOwnPurposeObjection(c fuego.ContextWithBody[dto.OwnPurposeObjectionRequest], guest bool) (*dto.Response[dto.OwnPurposeObjectionData], error) {
	userId, guestId, ok := freeConsentSubject(c, guest)
	if !ok {
		return dto.Fail[dto.OwnPurposeObjectionData]("guest consent routes are limited to the chat service")
	}
	req, err := c.Body()
	if err != nil || req.Objection == nil {
		return dto.Fail[dto.OwnPurposeObjectionData]("Invalid parameters")
	}
	objects := *req.Objection
	if guest {
		if err := model.SetGuestOwnPurposeObjection(guestId, objects); err != nil {
			return dto.Fail[dto.OwnPurposeObjectionData](err.Error())
		}
		return dto.Ok(dto.OwnPurposeObjectionData{Objection: objects})
	}
	user, err := model.GetUserById(userId, true)
	if err != nil {
		return dto.Fail[dto.OwnPurposeObjectionData](err.Error())
	}
	setting := user.GetSetting()
	setting.OwnPurposeObjection = objects
	if err := model.UpdateUserSetting(user.Id, setting); err != nil {
		return dto.Fail[dto.OwnPurposeObjectionData]("Update failed")
	}
	return dto.Ok(dto.OwnPurposeObjectionData{Objection: objects})
}

func GetOwnPurposeObjection(c fuego.ContextNoBody) (*dto.Response[dto.OwnPurposeObjectionData], error) {
	return getOwnPurposeObjection(c, false)
}

func SetOwnPurposeObjection(c fuego.ContextWithBody[dto.OwnPurposeObjectionRequest]) (*dto.Response[dto.OwnPurposeObjectionData], error) {
	return setOwnPurposeObjection(c, false)
}

func GetGuestOwnPurposeObjection(c fuego.ContextNoBody) (*dto.Response[dto.OwnPurposeObjectionData], error) {
	return getOwnPurposeObjection(c, true)
}

func SetGuestOwnPurposeObjection(c fuego.ContextWithBody[dto.OwnPurposeObjectionRequest]) (*dto.Response[dto.OwnPurposeObjectionData], error) {
	return setOwnPurposeObjection(c, true)
}
