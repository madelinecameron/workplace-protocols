// Package protocol defines how independently owned systems correlate the
// records they produce.
//
// A Correlation is carried as CloudEvents extension attributes: correlationid
// and causationid from the CloudEvents correlation extension, plus sessionid.
// Values must be namespaced references ("mail:thread/42") so ids minted by
// different systems cannot collide.
package protocol

// Version is the major version of the correlation contract and the name of
// its schema directory.
const Version = "2"

// Correlation makes one causal chain joinable across independently owned
// systems. CorrelationID stays fixed for the whole chain; CausationID names
// the immediate input that produced the current record. SessionID optionally
// identifies the agent run that handled the hop.
//
// It is correlation, not identity: choosing a CorrelationID grants no
// authority.
type Correlation struct {
	CorrelationID string `json:"correlationid"`
	CausationID   string `json:"causationid,omitempty"`
	SessionID     string `json:"sessionid,omitempty"`
}

// Attributes returns c as CloudEvents extension attributes, omitting empty
// ones, ready to set on an event.
func (c Correlation) Attributes() map[string]string {
	attrs := map[string]string{"correlationid": c.CorrelationID}
	if c.CausationID != "" {
		attrs["causationid"] = c.CausationID
	}
	if c.SessionID != "" {
		attrs["sessionid"] = c.SessionID
	}
	return attrs
}

// FromAttributes reads a Correlation back out of CloudEvents extension
// attributes and validates it.
func FromAttributes(attrs map[string]string) (Correlation, error) {
	c := Correlation{
		CorrelationID: attrs["correlationid"],
		CausationID:   attrs["causationid"],
		SessionID:     attrs["sessionid"],
	}
	return c, c.Validate()
}
