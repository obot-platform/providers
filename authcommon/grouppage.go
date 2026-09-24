package authcommon

const (
	DefaultGroupPageSize = 50

	// MaxGroupPageSize caps the page size. It is 100 because that is Auth0's ceiling on per_page,
	// and holding every provider to the same ceiling keeps the "one page costs one upstream
	// request" property rather than making some providers loop internally.
	MaxGroupPageSize = 100
)
