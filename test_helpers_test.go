package main

const (
	testProdPlayerPath           = "/v1/players/%23TAG"
	testDevPlayerPath            = "/dev/players/%23TAG"
	testProdPlayerPathWithFields = "/v1/players/%23TAG?fields=name&limit=10"
	testProdPlayerForwardedPath  = "/players/%23TAG?limit=10"
	testProdPlayerEndpoint       = "/players/{playerTag}"
	testClanEndpoint             = "/clans/{clanTag}"
	testRotatedKey               = "rotated-key"
	testForwardedBearer          = "Bearer forwarded-token"
	testJSONContentType          = "application/json"
	testProxyETag                = "proxy-etag"
	testExampleBaseURL           = "https://example.com/"
	headerContentType            = "Content-Type"
)
