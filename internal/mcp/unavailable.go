package mcp

// Unavailable is a CredentialProvider's answer that the caller can't use the
// upstream yet (no account connected, say). Atryum doesn't contact the
// upstream: a tools/list is answered with Tools, an initialize carries
// Instructions, and anything else fails with Message.
type Unavailable struct {
	Message      string
	Tools        []Tool
	Instructions string
}

func (u *Unavailable) Error() string { return u.Message }
