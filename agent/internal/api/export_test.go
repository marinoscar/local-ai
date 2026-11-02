package api

// CompareRegistered lets the external test package (openapi_wired_test.go), which can import the
// packages that build routes, run the comparison of openapi_test.go.
var CompareRegistered = compareRegistered
