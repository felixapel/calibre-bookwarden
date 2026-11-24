package evidence

// Finding is a versioned observation derived from one sealed book package.
// Fields contain stable codes and qualifications, never book text.
type Finding struct {
	Code   string `json:"code"`
	Format string `json:"format,omitempty"`
	Field  string `json:"field,omitempty"`
	Reason string `json:"reason,omitempty"`
}
