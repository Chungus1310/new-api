package dto

// UserGroupInfo describes a single usable group for the current user.
type UserGroupInfo struct {
	Ratio any    `json:"ratio"`
	Desc  string `json:"desc"`
	// Online reports whether any channel behind the group is enabled right now.
	// A group can be usable and priced yet have every channel auto-disabled, and
	// pinning to one of those fails every request until a channel recovers.
	Online bool `json:"online"`
	// Data-protection profile of the group's providers, see GroupDataPolicy.
	// Prompts and Transfer are absent when no channel in the group records one.
	Provider           string `json:"provider,omitempty"`
	SeatCountry        string `json:"seat_country,omitempty"`
	Prompts            string `json:"prompts,omitempty"`
	Transfer           string `json:"transfer,omitempty"`
	UnsafeThirdCountry bool   `json:"unsafe_third_country"`
}

// GroupDataPolicy is the data-protection profile of the providers behind a
// group. When several channels serve a group it is the most conservative of
// them, and a channel without a recorded policy counts as unknown.
type GroupDataPolicy struct {
	// Operator of the upstream; empty when unknown.
	Provider string `json:"provider,omitempty"`
	// Empty when unknown.
	SeatCountry string `json:"seat_country,omitempty"`
	// none, retains, trains or unknown.
	Prompts string `json:"prompts" validate:"required"`
	// adequacy, dpf, scc, none or unknown.
	Transfer string `json:"transfer" validate:"required"`
	// True when any channel sends data to a third country without an adequacy
	// decision and without a transfer basis.
	UnsafeThirdCountry bool `json:"unsafe_third_country"`
}
