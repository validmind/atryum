package invocation

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
)

// Asserted turns an application's on_behalf_of block into the asserted
// identity the decider sees. by is the authenticated credential's agent (the
// application), never a value from the request body. Call Validate first.
func (o *OnBehalfOf) Asserted(by string) *AssertedIdentity {
	if o == nil {
		return nil
	}
	a := &AssertedIdentity{By: by, Kind: AssertedKindApp, AppAgent: o.AppAgent, Token: o.Token}
	if u := o.EndUser; u != nil {
		a.UserID = u.ID
		a.UserDisplay = u.Display
		a.Email = u.Email
		a.Roles = append([]string(nil), u.Roles...)
		if u.Tenant != nil {
			a.EUTenant, a.HasEUTenant = *u.Tenant, true
		}
		a.UserKey = EndUserKey(by, a.EUTenant, a.HasEUTenant, a.UserID)
	}
	return a
}

// EndUserKey identifies one end user of one application: sha256 over the
// asserting agent, the end user's tenant (or its absence) and their id, each
// length-prefixed so no two inputs share a key. Atryum stores it instead of
// using the raw id to scope idempotency keys and client sessions, and it is
// how an embedder finds an end user's rows to redact them.
func EndUserKey(by, euTenant string, hasEUTenant bool, userID string) string {
	tenant := "-" // no tenant sent
	if hasEUTenant {
		tenant = "t:" + euTenant
	}
	h := sha256.New()
	for _, part := range []string{by, tenant, userID} {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(part)))
		h.Write(n[:])
		h.Write([]byte(part))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// endUserKey is a's UserKey, or "" when the call carries no end user.
func endUserKey(a *AssertedIdentity) string {
	if a == nil || a.Kind != AssertedKindApp || a.UserID == "" {
		return ""
	}
	return a.UserKey
}

// endUserSessionKey scopes an application's client session id to one end
// user: an application serves many end users with one credential, and two of
// them may well send the same conversation id. Without an end user the id is
// used as sent.
func endUserSessionKey(a *AssertedIdentity, clientSessionID string) string {
	if k := endUserKey(a); k != "" && clientSessionID != "" {
		return "eu:" + k[:24] + ":" + clientSessionID
	}
	return clientSessionID
}
