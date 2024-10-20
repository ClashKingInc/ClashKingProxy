package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json" // Added import for encoding/json
	"errors"
	"fmt"
	"github.com/golang-jwt/jwt/v5"
	"github.com/joho/godotenv"
	"github.com/valyala/fasthttp"
	"golang.org/x/time/rate"
	"io/ioutil"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// Configuration structure
type Config struct {
	BaseEmail     string
	MinEmailNum   int
	MaxEmailNum   int
	COCPassword   string
	ProxyPort     string
	APICountLimit int
}

// Function to get configuration from environment variables
func getConfig() (Config, error) {
	baseEmail := os.Getenv("BASE_EMAIL")
	if baseEmail == "" {
		return Config{}, errors.New("BASE_EMAIL environment variable is not set")
	}

	minEmailNumStr := os.Getenv("MIN_EMAIL_NUM")
	if minEmailNumStr == "" {
		return Config{}, errors.New("MIN_EMAIL_NUM environment variable is not set")
	}
	minEmailNum, err := strconv.Atoi(minEmailNumStr)
	if err != nil {
		return Config{}, fmt.Errorf("invalid MIN_EMAIL_NUM: %v", err)
	}

	maxEmailNumStr := os.Getenv("MAX_EMAIL_NUM")
	if maxEmailNumStr == "" {
		return Config{}, errors.New("MAX_EMAIL_NUM environment variable is not set")
	}
	maxEmailNum, err := strconv.Atoi(maxEmailNumStr)
	if err != nil {
		return Config{}, fmt.Errorf("invalid MAX_EMAIL_NUM: %v", err)
	}

	cocPassword := os.Getenv("COC_PASSWORD")
	if cocPassword == "" {
		return Config{}, errors.New("COC_PASSWORD environment variable is not set")
	}

	proxyPort := os.Getenv("PROXY_PORT")
	if proxyPort == "" {
		return Config{}, errors.New("PROXY_PORT environment variable is not set")
	}

	apiCountLimitStr := os.Getenv("API_COUNT_LIMIT")
	if apiCountLimitStr == "" {
		return Config{}, errors.New("API_COUNT_LIMIT environment variable is not set")
	}
	apiCountLimit, err := strconv.Atoi(apiCountLimitStr)
	if err != nil {
		return Config{}, fmt.Errorf("invalid API_COUNT_LIMIT: %v", err)
	}

	return Config{
		BaseEmail:     baseEmail,
		MinEmailNum:   minEmailNum,
		MaxEmailNum:   maxEmailNum,
		COCPassword:   cocPassword,
		ProxyPort:     proxyPort,
		APICountLimit: apiCountLimit,
	}, nil
}

// Struct to hold API key
type APIKey struct {
	Key string
}

// Global variables for API keys and rotation
var (
	apiKeys  []APIKey
	keyIndex uint64
	client   *fasthttp.Client
)

// Data structures for JSON responses
type LoginResponse struct {
	TemporaryAPIToken string `json:"temporaryAPIToken"`
}

type KeyResponse struct {
	Key struct {
		Key string `json:"key"`
	} `json:"key"`
	Error       string `json:"error"`
	Description string `json:"description"`
}

type KeysResponse struct {
	Status struct {
		Code    int     `json:"code"`
		Message string  `json:"message"`
		Detail  *string `json:"detail"`
	} `json:"status"`
	SessionExpiresInSeconds int   `json:"sessionExpiresInSeconds"`
	Keys                    []Key `json:"keys"`
}

type Key struct {
	ID          string   `json:"id"`
	DeveloperID string   `json:"developerId"`
	Tier        string   `json:"tier"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Origins     []string `json:"origins"`
	Scopes      []string `json:"scopes"`
	CIDRRanges  []string `json:"cidrRanges"`
	ValidUntil  *string  `json:"validUntil"`
	Key         string   `json:"key"`
}

// Function to extract IP from JWT token
func getIPFromToken(tokenString string) (string, error) {
	// Parse the JWT token without verifying the signature
	token, _, err := new(jwt.Parser).ParseUnverified(tokenString, jwt.MapClaims{})
	if err != nil {
		return "", fmt.Errorf("error parsing token: %v", err)
	}

	// Check the token claims
	if claims, ok := token.Claims.(jwt.MapClaims); ok {
		if limits, ok := claims["limits"].([]interface{}); ok {
			for _, limitEntry := range limits {
				limitMap, ok := limitEntry.(map[string]interface{})
				if !ok {
					continue
				}
				if limitMap["type"] == "client" {
					if cidrs, ok := limitMap["cidrs"].([]interface{}); ok && len(cidrs) > 0 {
						ipCidr, ok := cidrs[0].(string)
						if !ok {
							return "", fmt.Errorf("cidr range is not a string")
						}
						ip := strings.Split(ipCidr, "/")[0] // Extract the IP part
						return ip, nil
					} else {
						return "", fmt.Errorf("CIDR ranges not found in client claim")
					}
				}
			}
			return "", fmt.Errorf("client type not found in limits")
		} else {
			return "", fmt.Errorf("limits claim not found or invalid format")
		}
	} else {
		return "", fmt.Errorf("invalid token claims format")
	}
}

// Function to login and get IP address
func loginAndGetIP(email string, password string) (*http.Client, string, error) {
	loginURL := "https://developer.clashofclans.com/api/login"
	loginBody := map[string]string{
		"email":    email,
		"password": password,
	}
	loginBodyBytes, _ := json.Marshal(loginBody)
	jar, _ := cookiejar.New(nil)
	httpClient := &http.Client{
		Jar: jar,
	}
	loginReq, _ := http.NewRequest("POST", loginURL, bytes.NewBuffer(loginBodyBytes))
	loginReq.Header.Set("Content-Type", "application/json")
	loginResp, err := httpClient.Do(loginReq)
	if err != nil {
		return nil, "", err
	}
	defer loginResp.Body.Close()
	if loginResp.StatusCode == 403 {
		return nil, "", fmt.Errorf("invalid credentials for email: %s", email)
	}
	var loginRespPayload LoginResponse
	err = json.NewDecoder(loginResp.Body).Decode(&loginRespPayload)
	if err != nil {
		return nil, "", fmt.Errorf("error decoding login response: %v", err)
	}

	ip, err := getIPFromToken(loginRespPayload.TemporaryAPIToken)
	if err != nil {
		return nil, "", err
	}
	return httpClient, ip, nil
}

// Function to list existing API keys
func listKeys(client *http.Client) ([]Key, error) {
	listURL := "https://developer.clashofclans.com/api/apikey/list"
	listReq, _ := http.NewRequest("POST", listURL, nil)
	listReq.Header.Set("Content-Type", "application/json")
	listResp, err := client.Do(listReq)
	if err != nil {
		return nil, fmt.Errorf("error listing keys: %v", err)
	}
	defer listResp.Body.Close()

	listRespBody, _ := ioutil.ReadAll(listResp.Body)

	var keysResp KeysResponse
	err = json.Unmarshal(listRespBody, &keysResp)
	if err != nil {
		return nil, fmt.Errorf("error parsing keys response: %v", err)
	}

	return keysResp.Keys, nil
}

// Function to revoke an API key
func revokeKey(client *http.Client, keyID string) error {
	revokeURL := "https://developer.clashofclans.com/api/apikey/revoke"
	revokeBody := map[string]string{"id": keyID}
	revokeBodyBytes, _ := json.Marshal(revokeBody)
	revokeReq, _ := http.NewRequest("POST", revokeURL, bytes.NewBuffer(revokeBodyBytes))
	revokeReq.Header.Set("Content-Type", "application/json")
	revokeResp, err := client.Do(revokeReq)
	if err != nil {
		return fmt.Errorf("error revoking key %s: %v", keyID, err)
	}
	defer revokeResp.Body.Close()

	if revokeResp.StatusCode != 200 {
		body, _ := ioutil.ReadAll(revokeResp.Body)
		return fmt.Errorf("failed to revoke key %s: status %d, response: %s", keyID, revokeResp.StatusCode, string(body))
	}

	return nil
}

// Function to create a new API key
func createKey(client *http.Client, name string, description string, cidrRanges []string, scopes []string) (string, error) {
	createURL := "https://developer.clashofclans.com/api/apikey/create"
	createBody := map[string]interface{}{
		"name":        name,
		"description": description,
		"cidrRanges":  cidrRanges,
		"scopes":      scopes,
	}
	createBodyBytes, _ := json.Marshal(createBody)
	createReq, _ := http.NewRequest("POST", createURL, bytes.NewBuffer(createBodyBytes))
	createReq.Header.Set("Content-Type", "application/json")
	createResp, err := client.Do(createReq)
	if err != nil {
		return "", fmt.Errorf("error creating key: %v", err)
	}
	defer createResp.Body.Close()

	createRespBody, _ := ioutil.ReadAll(createResp.Body)

	var createRespPayload KeyResponse
	err = json.Unmarshal(createRespBody, &createRespPayload)
	if err != nil {
		return "", fmt.Errorf("error parsing create key response: %v", err)
	}

	if createRespPayload.Error == "too-many-keys" {
		return "", fmt.Errorf("too many keys: %v", createRespPayload.Description)
	}

	keyString := createRespPayload.Key.Key
	if keyString == "" {
		return "", fmt.Errorf("invalid or missing key in response")
	}

	return keyString, nil
}

// Function to get or create API keys
func GetKeys(emails []string, passwords []string, keyNames string, keyCount int) ([]APIKey, error) {
	var totalKeys []APIKey
	for i, email := range emails {
		password := passwords[i]
		client, ip, err := loginAndGetIP(email, password)
		if err != nil {
			return nil, err
		}

		existingKeys, err := listKeys(client)
		if err != nil {
			return nil, err
		}

		var matchingKeys []APIKey

		// Collect keys matching keyNames and IP
		for _, key := range existingKeys {
			if key.Name == keyNames && contains(key.CIDRRanges, ip) {
				matchingKeys = append(matchingKeys, APIKey{
					Key: key.Key,
				})
			}
		}

		// Revoke keys that do not match the current IP
		for _, key := range existingKeys {
			if key.Name == keyNames && !contains(key.CIDRRanges, ip) {
				if err := revokeKey(client, key.ID); err != nil {
					return nil, fmt.Errorf("error revoking key: %v", err)
				}
			}
		}

		// Determine how many keys need to be created
		keysToCreate := keyCount - len(matchingKeys)
		if keysToCreate <= 0 {
			// Already have enough keys
			if len(matchingKeys) > keyCount {
				matchingKeys = matchingKeys[:keyCount]
			}
			totalKeys = append(totalKeys, matchingKeys...)
			continue
		}

		for j := 0; j < keysToCreate; j++ {
			key, err := createKey(client, keyNames, fmt.Sprintf("Created on %s", time.Now().Format(time.RFC3339)), []string{ip}, []string{"clash"})
			if err != nil {
				if strings.Contains(err.Error(), "too many keys") {
					break
				}
				return nil, err
			}
			matchingKeys = append(matchingKeys, APIKey{
				Key: key,
			})
			totalKeys = append(totalKeys, APIKey{
				Key: key,
			})
		}

		// Append the matching keys to totalKeys
		totalKeys = append(totalKeys, matchingKeys...)
	}
	return totalKeys, nil
}

// Utility function to check if a slice contains a string
func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

// Function to generate emails and passwords based on configuration
func generateCredentials(config Config) ([]string, []string) {
	var emails []string
	var passwords []string

	for x := config.MinEmailNum; x <= config.MaxEmailNum; x++ {
		email := fmt.Sprintf(config.BaseEmail, x)
		emails = append(emails, email)
		passwords = append(passwords, config.COCPassword)
	}

	return emails, passwords
}

// Global rate limiter
var globalLimiter *rate.Limiter

// Function to initialize the global rate limiter
func initGlobalLimiter(totalRequestsPerSecond int) {
	globalLimiter = rate.NewLimiter(rate.Limit(float64(totalRequestsPerSecond)), totalRequestsPerSecond)
}

// Proxy handler that uses global rate limiting and forwards requests
func proxyHandler(ctx *fasthttp.RequestCtx) {
	// Handle CORS preflight requests if necessary
	if string(ctx.Method()) == "OPTIONS" {
		ctx.Response.Header.Set("Access-Control-Allow-Origin", "*")
		ctx.Response.Header.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		ctx.Response.Header.Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		ctx.SetStatusCode(fasthttp.StatusOK)
		return
	}

	// Create a context with a 5-second timeout for rate limiting
	ctxLimiter, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Attempt to reserve a token with a timeout
	if err := globalLimiter.Wait(ctxLimiter); err != nil {
		ctx.Error("Too Many Requests", fasthttp.StatusTooManyRequests)
		return
	}

	// Get the requested path (after /v1/)
	path := string(ctx.Path())
	if strings.HasPrefix(path, "/v1/") {
		path = strings.TrimPrefix(path, "/v1/")
	} else {
		ctx.Error("Not Found", fasthttp.StatusNotFound)
		return
	}

	// Replace '!' with '#' in the path
	path = strings.ReplaceAll(path, "!", "#")

	// Split the path into segments and URL-encode only the player tag
	segments := strings.Split(path, "/")
	for i, segment := range segments {
		if strings.HasPrefix(segment, "#") {
			segments[i] = url.PathEscape(segment)
		} else {
			segments[i] = segment // Keep other segments as-is
		}
	}
	escapedPath := strings.Join(segments, "/")

	// Construct the full URL
	fullURL := "https://api.clashofclans.com/v1/" + escapedPath

	// Handle query parameters
	queryString := string(ctx.QueryArgs().QueryString())
	if queryString != "" {
		fullURL += "?" + queryString
	}

	// Rotate the API key using atomic operation
	idx := atomic.AddUint64(&keyIndex, 1)
	currentAPIKey := &apiKeys[idx%uint64(len(apiKeys))]

	// Prepare the Clash of Clans API request
	req := fasthttp.AcquireRequest()
	defer fasthttp.ReleaseRequest(req)
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseResponse(resp)

	req.SetRequestURI(fullURL)
	req.Header.SetMethod(string(ctx.Method()))
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", currentAPIKey.Key))
	req.Header.Set("Accept", "application/json")

	// Handle incoming gzip requests
	if strings.Contains(string(ctx.Request.Header.Peek("Content-Encoding")), "gzip") {
		// Decompress the request body
		gzReader, err := gzip.NewReader(bytes.NewReader(ctx.PostBody()))
		if err != nil {
			ctx.Error("Bad Request: unable to decompress gzip body", fasthttp.StatusBadRequest)
			return
		}
		defer gzReader.Close()
		decompressedBody, err := ioutil.ReadAll(gzReader)
		if err != nil {
			ctx.Error("Bad Request: error reading gzip body", fasthttp.StatusBadRequest)
			return
		}
		req.SetBody(decompressedBody)
	} else {
		req.SetBody(ctx.PostBody())
	}

	// Forward the request to the Clash of Clans API
	err := client.Do(req, resp)
	if err != nil {
		ctx.Error("Internal Server Error", fasthttp.StatusInternalServerError)
		return
	}

	// Set the proxy's response status code to match the API's response status code
	ctx.SetStatusCode(resp.StatusCode())

	// Check if the client accepts gzip responses
	acceptEncoding := string(ctx.Request.Header.Peek("Accept-Encoding"))
	shouldGzip := strings.Contains(acceptEncoding, "gzip")

	// Get the response body
	responseBody := resp.Body()

	// Prepare response body based on gzip support
	if shouldGzip {
		var buf bytes.Buffer
		gzWriter := gzip.NewWriter(&buf)
		_, err := gzWriter.Write(responseBody)
		if err != nil {
			ctx.Error("Internal Server Error: gzip write failed", fasthttp.StatusInternalServerError)
			return
		}
		gzWriter.Close()
		ctx.Response.Header.Set("Content-Encoding", "gzip")
		ctx.SetBody(buf.Bytes())
	} else {
		ctx.SetBody(responseBody)
	}

	// Forward response headers (excluding certain headers)
	resp.Header.VisitAll(func(key, value []byte) {
		lowerKey := bytes.ToLower(key)
		if bytes.Equal(lowerKey, []byte("content-length")) || bytes.Equal(lowerKey, []byte("content-encoding")) {
			return
		}
		ctx.Response.Header.SetBytesKV(key, value)
	})

	// Set CORS headers for the response
	ctx.Response.Header.Set("Access-Control-Allow-Origin", "*")
}

func main() {
	err := godotenv.Load()
	if err != nil {
		fmt.Println("Error loading .env file:", err)
	}

	config, err := getConfig()
	if err != nil {
		fmt.Println("Error:", err)
		return
	}

	// Generate emails and passwords based on configuration
	emails, passwords := generateCredentials(config)

	// Validate that emails and passwords slices are of the same length
	if len(emails) != len(passwords) {
		fmt.Println("Error: The number of emails and passwords must be the same.")
		return
	}

	// Get or create API keys
	apiKeys, err = GetKeys(emails, passwords, "test", config.APICountLimit)
	if err != nil {
		fmt.Println("Error:", err)
		return
	}

	if len(apiKeys) == 0 {
		fmt.Println("No API keys available.")
		return
	}

	// Initialize the global rate limiter: 30 requests per second per key
	totalRequestsPerSecond := 30 * len(apiKeys)
	initGlobalLimiter(totalRequestsPerSecond)

	// Initialize fasthttp client
	client = &fasthttp.Client{
		MaxConnsPerHost: 10000,
	}

	// Start HTTP server using fasthttp
	if err := fasthttp.ListenAndServe(":"+config.ProxyPort, proxyHandler); err != nil {
		fmt.Println("Error starting server:", err)
	}
}
