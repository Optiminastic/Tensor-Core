package auth

// TokenClaims is the verified payload of a Better Auth access token. The wire
// form is camelCase for permissionsVersion and sessionId; the rest are lower.
type TokenClaims struct {
	Sub                string
	Email              string
	Roles              []RoleName
	Permissions        []string
	PermissionsVersion int
	SessionID          *string
}

// AuthenticatedUser is the request-scoped identity built from verified claims.
// Permissions is a set for O(1) checks.
type AuthenticatedUser struct {
	ID                 string
	Email              string
	Roles              []RoleName
	Permissions        map[string]struct{}
	PermissionsVersion int
	SessionID          *string
}

// Has reports whether the user holds a permission key ("resource:action").
func (u AuthenticatedUser) Has(key string) bool {
	_, ok := u.Permissions[key]
	return ok
}

// NewAuthenticatedUser builds the request identity from verified token claims.
func NewAuthenticatedUser(c TokenClaims) AuthenticatedUser {
	perms := make(map[string]struct{}, len(c.Permissions))
	for _, p := range c.Permissions {
		perms[p] = struct{}{}
	}
	return AuthenticatedUser{
		ID:                 c.Sub,
		Email:              c.Email,
		Roles:              c.Roles,
		Permissions:        perms,
		PermissionsVersion: c.PermissionsVersion,
		SessionID:          c.SessionID,
	}
}

// UserAuthzResponse is the body of GET /internal/users/{id}/authz. The frontend
// stamps this into the JWT, so the field names are load-bearing: roles and
// permissions are lower snake, but permissions_version is serialised as the
// camelCase permissionsVersion. Roles and permissions are pre-sorted for stable
// token payloads.
type UserAuthzResponse struct {
	Roles              []RoleName `json:"roles"`
	Permissions        []string   `json:"permissions"`
	PermissionsVersion int        `json:"permissionsVersion"`
}
