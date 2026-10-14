package dto

import (
	"fmt"
	"regexp"
	"slices"
)

var countryCodePattern = regexp.MustCompile(`^[A-Z]{2}$`)

// ChannelDataPolicy records what the upstream behind a channel does with the
// data it relays. Every field is optional; an empty value means unknown.
type ChannelDataPolicy struct {
	// SeatCountry is the ISO 3166-1 alpha-2 code of the provider's seat.
	SeatCountry string `json:"seat_country,omitempty"`
	// Prompts is one of DataPolicyPrompts*.
	Prompts string `json:"prompts,omitempty"`
	// Transfer is the legal basis for moving data to the seat, one of DataPolicyTransfer*.
	Transfer string `json:"transfer,omitempty"`
}

const (
	DataPolicyPromptsNone    = "none"
	DataPolicyPromptsRetains = "retains"
	DataPolicyPromptsTrains  = "trains"
	DataPolicyUnknown        = "unknown"

	DataPolicyTransferAdequacy = "adequacy"
	DataPolicyTransferDPF      = "dpf"
	DataPolicyTransferSCC      = "scc"
	DataPolicyTransferNone     = "none"
)

var dataPolicyPrompts = []string{DataPolicyPromptsNone, DataPolicyPromptsRetains, DataPolicyPromptsTrains, DataPolicyUnknown}

var dataPolicyTransfers = []string{DataPolicyTransferAdequacy, DataPolicyTransferDPF, DataPolicyTransferSCC, DataPolicyTransferNone, DataPolicyUnknown}

var eeaCountries = []string{
	"AT", "BE", "BG", "HR", "CY", "CZ", "DK", "EE", "FI", "FR", "DE", "GR", "HU", "IE",
	"IT", "LV", "LT", "LU", "MT", "NL", "PL", "PT", "RO", "SK", "SI", "ES", "SE",
	"IS", "LI", "NO",
}

// The US is absent on purpose: only DPF-certified recipients are covered,
// which Transfer=dpf expresses.
var euAdequacyCountries = []string{"AD", "AR", "CA", "FO", "GG", "IL", "IM", "JP", "JE", "NZ", "KR", "CH", "GB", "UY"}

// IsEEACountry reports whether the ISO alpha-2 code is an EEA member.
func IsEEACountry(code string) bool {
	return slices.Contains(eeaCountries, code)
}

// IsEUAdequacyCountry reports whether the country holds an EU adequacy decision.
func IsEUAdequacyCountry(code string) bool {
	return slices.Contains(euAdequacyCountries, code)
}

// HasSafeTransferBasis reports whether the transfer value is an Art. 45/46 GDPR basis.
func HasSafeTransferBasis(transfer string) bool {
	return transfer == DataPolicyTransferAdequacy || transfer == DataPolicyTransferDPF || transfer == DataPolicyTransferSCC
}

// UnsafeThirdCountry reports a transfer to a known seat outside the EEA and
// the adequacy list with no transfer basis. An unknown seat is not reported.
func (p *ChannelDataPolicy) UnsafeThirdCountry() bool {
	if p == nil || p.SeatCountry == "" || HasSafeTransferBasis(p.Transfer) {
		return false
	}
	return !IsEEACountry(p.SeatCountry) && !IsEUAdequacyCountry(p.SeatCountry)
}

// Validate checks the stored values against the allowed vocabularies.
func (p *ChannelDataPolicy) Validate() error {
	if p == nil {
		return nil
	}
	if p.SeatCountry != "" && !countryCodePattern.MatchString(p.SeatCountry) {
		return fmt.Errorf("invalid data_policy.seat_country: %q, expected an uppercase ISO 3166-1 alpha-2 code", p.SeatCountry)
	}
	if p.Prompts != "" && !slices.Contains(dataPolicyPrompts, p.Prompts) {
		return fmt.Errorf("invalid data_policy.prompts: %q", p.Prompts)
	}
	if p.Transfer != "" && !slices.Contains(dataPolicyTransfers, p.Transfer) {
		return fmt.Errorf("invalid data_policy.transfer: %q", p.Transfer)
	}
	return nil
}
