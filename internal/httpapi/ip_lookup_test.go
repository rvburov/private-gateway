package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

const ipLookupTestKey = "ip-lookup-private-test-key"
const ipLookupTestIP = "162.35.172.6"

type ipLookupTransport func(*http.Request) (*http.Response, error)

func (transport ipLookupTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return transport(r)
}

type ipLookupBrokenBody struct{ closed bool }

func (body *ipLookupBrokenBody) Read([]byte) (int, error) {
	return 0, errors.New("provider response body failed: " + ipLookupTestKey)
}
func (body *ipLookupBrokenBody) Close() error { body.closed = true; return nil }

func ipLookupTestServer(transport ipLookupTransport) *Server {
	service := newIPLookupService(ipLookupTestKey)
	service.client.Transport = transport
	return &Server{ipLookups: service}
}

func ipLookupTestResponse(body string, code int) *http.Response {
	return &http.Response{StatusCode: code, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func ipLookupTestRequest(t *testing.T, server *Server, body string) (*httptest.ResponseRecorder, ipLookupResult) {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/ip-lookup", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	securityHeaders(http.HandlerFunc(server.ipLookup)).ServeHTTP(response, request)
	var result ipLookupResult
	if response.Code == http.StatusOK {
		decoder := json.NewDecoder(response.Body)
		decoder.UseNumber()
		if err := decoder.Decode(&result); err != nil {
			t.Fatal(err)
		}
	}
	return response, result
}

func TestIPLookupRouteRequiresAuthenticationAndWorksWithoutKey(t *testing.T) {
	const token = "ip-lookup-local-cli-token-with-32-characters"
	handler := New(nil, nil, nil, Options{Token: token})
	for _, auth := range []string{"", "Bearer wrong", "Bearer " + token} {
		body := &unreadPerformanceBody{}
		request := httptest.NewRequest(http.MethodPost, "/api/v1/ip-lookup", body)
		request.Header.Set("Authorization", auth)
		request.RemoteAddr = "198.51.100.3:40000"
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized || body.reads != 0 {
			t.Fatalf("lookup read input before authentication: code=%d reads=%d", response.Code, body.reads)
		}
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/ip-lookup", strings.NewReader(`{"ip":"`+ipLookupTestIP+`"}`))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	request.RemoteAddr = "127.0.0.1:40000"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	var result ipLookupResult
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &result) != nil || result.Status != "not_configured" || result.Data != nil || result.CheckedAt != nil || result.IP != ipLookupTestIP || result.Source != ipLookupSource || result.Detail == "" {
		t.Fatalf("optional provider disabled the panel: code=%d body=%s", response.Code, response.Body)
	}
	if response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatal("IP lookup response may be cached or has no UTF-8 declaration")
	}
}

func TestIPLookupAcceptsOnlyPublicLiteralAddresses(t *testing.T) {
	for _, address := range []string{ipLookupTestIP, "8.8.8.8", "2606:4700:4700::1111", "2001:4860:4860::8888", "::ffff:8.8.8.8"} {
		if _, ok := ipLookupAddress(address); !ok {
			t.Errorf("public IP rejected: %s", address)
		}
	}
	for _, address := range []string{"", "example.com", "https://8.8.8.8", "8.8.8.8:443", " 8.8.8.8", "8.8.8.8\n", "127.0.0.1", "0.0.0.0", "10.0.0.1", "172.16.1.2", "192.168.1.1", "100.64.1.1", "169.254.169.254", "192.0.0.1", "192.0.2.1", "192.88.99.1", "198.18.0.1", "198.51.100.1", "203.0.113.1", "224.0.0.1", "240.0.0.1", "255.255.255.255", "::", "::1", "::ffff:127.0.0.1", "fc00::1", "fe80::1", "fe80::1%eth0", "2001:db8::1", "3fff::1", "2001:2::1", "2002:7f00:1::", "64:ff9b::808:808", "ff02::1"} {
		if _, ok := ipLookupAddress(address); ok {
			t.Errorf("nonpublic or nonliteral IP accepted: %q", address)
		}
	}
	address, _ := ipLookupAddress("::ffff:8.8.8.8")
	if address.String() != "8.8.8.8" {
		t.Fatal("mapped IPv4 was not canonicalized")
	}
}

func TestIPLookupRejectsInvalidJSONBeforeProviderOrQuota(t *testing.T) {
	calls := 0
	server := ipLookupTestServer(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("must not run")
	})
	for _, body := range []string{`{}`, `null`, `[]`, `{"ip":8}`, `{"ip":"example.com"}`, `{"ip":"127.0.0.1"}`, `{"ip":"8.8.8.8","url":"https://evil.example"}`, `{"ip":"8.8.8.8"} {}`, strings.Repeat(" ", 4097) + `{}`} {
		response, _ := ipLookupTestRequest(t, server, body)
		if response.Code != http.StatusBadRequest || calls != 0 || server.ipLookups.minuteUsed != 0 {
			t.Fatalf("invalid input consumed quota or reached provider: code=%d calls=%d", response.Code, calls)
		}
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/ip-lookup", strings.NewReader(`{"ip":"8.8.8.8"}`))
	response := httptest.NewRecorder()
	server.ipLookup(response, request)
	if response.Code != http.StatusUnsupportedMediaType || calls != 0 {
		t.Fatal("unsupported request Content-Type reached provider")
	}
}

// Полный синтетический ответ проверяет все согласованные группы, а не текущий тариф API.
const ipLookupCompleteResponse = `{
 "ip":"162.35.172.6","country_code":"US","country_name":"United States","region_name":"New Jersey","district":"Hudson County","city_name":"Secaucus",
 "latitude":40.7895,"longitude":-74.0565,"zip_code":"07094","time_zone":"America/New_York","asn":"19318","as":"Interserver, Inc",
 "as_info":{"as_number":"19318","as_name":"Interserver, Inc","as_domain":"interserver.net","as_usage_type":"DCH","as_cidr":"162.35.172.0/24"},
 "isp":"Interserver, Inc","domain":"interserver.net","net_speed":"T1","idd_code":"1","area_code":"201","weather_station_code":"USNJ0469","weather_station_name":"Secaucus",
 "mcc":"310","mnc":"001","mobile_brand":"Example","elevation":"7","usage_type":"DCH","address_type":"Unicast","ads_category":"IAB19","ads_category_name":"Technology & Computing",
 "continent":{"name":"North America","code":"NA","hemisphere":["north","west"],"translation":{"lang":"ru","value":"Северная Америка"}},
 "country":{"name":"United States","alpha3_code":"USA","numeric_code":"840","demonym":"Americans","flag":"US","capital":"Washington, D.C.","total_area":9833520,"population":331449281,"currency":{"code":"USD","name":"US Dollar","symbol":"$"},"language":{"code":"en","name":"English"},"tld":"us","translation":{"lang":"ru","value":"США"}},
 "region":{"name":"New Jersey","code":"US-NJ","translation":{"lang":"ru","value":"Нью-Джерси"}},
 "city":{"name":"Secaucus","translation":{"lang":"ru","value":"Секокус"}},
 "time_zone_info":{"olson":"America/New_York","current_time":"2026-10-23T05:00:00-04:00","gmt_offset":-14400,"is_dst":true,"abbreviation":"EDT","dst_start_date":"2026-03-08","dst_end_date":"2026-11-01","sunrise":"07:16","sunset":"18:05"},
 "geotargeting":{"metro":"501"},"is_proxy":false,"fraud_score":0,
 "proxy":{"last_seen":0,"proxy_type":"DCH","threat":"NONE","provider":"Example","is_vpn":false,"is_tor":false,"is_data_center":true,"is_public_proxy":false,"is_web_proxy":false,"is_web_crawler":false,"is_ai_crawler":false,"is_residential_proxy":false,"is_consumer_privacy_network":false,"is_enterprise_private_network":false,"is_spammer":false,"is_scanner":false,"is_botnet":false,"is_bogon":false}
}`

func TestIPLookupNormalizesCompleteResponseWithoutExposingCredentials(t *testing.T) {
	var raw map[string]any
	if err := json.Unmarshal([]byte(ipLookupCompleteResponse), &raw); err != nil {
		t.Fatal(err)
	}
	raw["api_key"] = ipLookupTestKey
	raw["private_profile"] = "vless://not-public"
	raw["proxy"].(map[string]any)["debug_secret"] = ipLookupTestKey
	body, _ := json.Marshal(raw)
	server := ipLookupTestServer(func(request *http.Request) (*http.Response, error) {
		query := request.URL.Query()
		if request.Method != http.MethodGet || request.URL.Scheme != "https" || request.URL.Host != "api.ip2location.io" || request.URL.Path != "/" || len(query) != 2 || query.Get("key") != ipLookupTestKey || query.Get("ip") != ipLookupTestIP {
			t.Fatal("provider destination or query is not fixed and canonical")
		}
		if request.Header.Get("Authorization") != "" || request.Header.Get("Cookie") != "" || request.Header.Get("Referer") != "" || request.Header.Get("Accept") != "application/json" {
			t.Fatal("provider received panel credentials or unexpected headers")
		}
		return ipLookupTestResponse(string(body), http.StatusOK), nil
	})
	response, result := ipLookupTestRequest(t, server, `{"ip":"`+ipLookupTestIP+`"}`)
	if response.Code != http.StatusOK || result.Status != "available" || result.CheckedAt == nil || result.CheckedAt.IsZero() || result.Source != ipLookupSource || result.Detail == "" {
		t.Fatalf("complete lookup not available: code=%d result=%+v", response.Code, result)
	}
	if strings.Contains(response.Body.String(), ipLookupTestKey) || strings.Contains(response.Body.String(), "private_profile") || strings.Contains(response.Body.String(), "debug_secret") {
		t.Fatal("lookup exposed credentials or unrecognized fields")
	}
	if len(result.Data) != len(ipLookupFields) || result.Data["asn"] != "19318" || result.Data["elevation"] != json.Number("7") || result.Data["is_proxy"] != false || result.Data["fraud_score"] != json.Number("0") {
		t.Fatal("number strings, zero or false were replaced or lost", result.Data)
	}
	proxy := result.Data["proxy"].(map[string]any)
	if proxy["is_data_center"] != true || proxy["is_vpn"] != false || proxy["last_seen"] != json.Number("0") || proxy["is_consumer_privacy_network"] != false || proxy["is_enterprise_private_network"] != false {
		t.Fatal("proxy flags or age were changed", proxy)
	}
	country := result.Data["country"].(map[string]any)
	if country["numeric_code"] != "840" || country["currency"].(map[string]any)["code"] != "USD" || country["translation"].(map[string]any)["value"] != "США" {
		t.Fatal("country details were changed", country)
	}
}

func TestIPLookupMissingAndInvalidValuesRemainUnknown(t *testing.T) {
	body := `{"ip":"162.35.172.6","country_code":"US","country_name":"` + ipLookupTestKey + `","latitude":91,"longitude":"-74.1","elevation":false,"asn":19318,"is_proxy":false,"fraud_score":"0","country":{"numeric_code":"004","population":"12345"},"geotargeting":{"metro":"-"},"continent":{"hemisphere":["north",12]},"proxy":{"last_seen":"3","is_data_center":true,"is_tor":"false","is_vpn":null,"is_web_proxy":0,"provider":"-"},"time_zone_info":{"gmt_offset":"-14400","is_dst":"true"}}`
	server := ipLookupTestServer(func(*http.Request) (*http.Response, error) {
		return ipLookupTestResponse(body, http.StatusOK), nil
	})
	response, result := ipLookupTestRequest(t, server, `{"ip":"`+ipLookupTestIP+`"}`)
	if result.Status != "partial" || result.CheckedAt == nil || result.Data["country_code"] != "US" || result.Data["country_name"] != nil || result.Data["latitude"] != nil || result.Data["longitude"] != json.Number("-74.1") || result.Data["asn"] != json.Number("19318") || result.Data["elevation"] != nil {
		t.Fatal("partial values were not normalized honestly", result)
	}
	proxy := result.Data["proxy"].(map[string]any)
	for _, flag := range []string{"is_vpn", "is_tor", "is_web_proxy", "is_consumer_privacy_network", "is_enterprise_private_network", "is_spammer", "is_scanner", "is_botnet", "is_bogon"} {
		if proxy[flag] != nil {
			t.Errorf("unavailable flag %s became %v", flag, proxy[flag])
		}
	}
	country := result.Data["country"].(map[string]any)
	zone := result.Data["time_zone_info"].(map[string]any)
	if proxy["last_seen"] != json.Number("3") || proxy["provider"] != nil || proxy["is_data_center"] != true || result.Data["fraud_score"] != json.Number("0") || result.Data["region"] != nil || result.Data["is_proxy"] != false || result.Data["geotargeting"].(map[string]any)["metro"] != nil || country["numeric_code"] != "004" || country["population"] != json.Number("12345") || zone["gmt_offset"] != json.Number("-14400") || strings.Contains(response.Body.String(), ipLookupTestKey) {
		t.Fatal("unknown fields were fabricated or private data was retained", result)
	}
}

func TestIPLookupProviderFailuresDoNotBecomePanelFailuresOrLeakSecrets(t *testing.T) {
	for _, test := range []struct {
		name   string
		code   int
		body   string
		status string
	}{
		{"forbidden", 403, ipLookupTestKey, "unavailable"},
		{"unavailable", 503, ipLookupTestKey, "unavailable"},
		{"quota", 429, ipLookupTestKey, "rate_limited"},
		{"structured-error", 200, `{"error":{"error_code":101,"error_message":"` + ipLookupTestKey + `"}}`, "unavailable"},
		{"not-json", 200, "<html>" + ipLookupTestKey, "unavailable"},
		{"empty", 200, `{}`, "unavailable"},
		{"null", 200, `null`, "unavailable"},
		{"array", 200, `[]`, "unavailable"},
		{"ip-only", 200, `{"ip":"162.35.172.6"}`, "unavailable"},
		{"another-ip", 200, `{"ip":"8.8.8.8","country_code":"US"}`, "unavailable"},
		{"invalid-ip", 200, `{"ip":123,"country_code":"US"}`, "unavailable"},
		{"trailing-json", 200, `{"ip":"162.35.172.6","country_code":"US"} {}`, "unavailable"},
		{"oversized", 200, strings.Repeat(" ", ipLookupResponseLimit+1) + `{}`, "unavailable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := ipLookupTestServer(func(*http.Request) (*http.Response, error) {
				return ipLookupTestResponse(test.body, test.code), nil
			})
			response, result := ipLookupTestRequest(t, server, `{"ip":"`+ipLookupTestIP+`"}`)
			if response.Code != http.StatusOK || result.Status != test.status || result.Data != nil || result.CheckedAt != nil || result.Detail == "" || strings.Contains(response.Body.String(), ipLookupTestKey) || strings.Contains(response.Body.String(), "error_message") {
				t.Fatalf("provider failure escaped isolation: code=%d result=%+v", response.Code, result)
			}
		})
	}
	server := ipLookupTestServer(func(request *http.Request) (*http.Response, error) {
		return nil, errors.New("network failure: " + request.URL.String())
	})
	response, result := ipLookupTestRequest(t, server, `{"ip":"`+ipLookupTestIP+`"}`)
	if result.Status != "unavailable" || strings.Contains(response.Body.String(), ipLookupTestKey) || strings.Contains(response.Body.String(), "key=") {
		t.Fatal("raw network error exposed URL credentials", result)
	}
	broken := &ipLookupBrokenBody{}
	server = ipLookupTestServer(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: broken}, nil
	})
	response, result = ipLookupTestRequest(t, server, `{"ip":"`+ipLookupTestIP+`"}`)
	if result.Status != "unavailable" || !broken.closed || result.CheckedAt != nil || result.Data != nil || strings.Contains(response.Body.String(), ipLookupTestKey) {
		t.Fatal("failed body was not closed or its raw error escaped", result)
	}
}

func TestIPLookupEnforcesSharedMinuteDailyAndParallelBudgets(t *testing.T) {
	calls := 0
	server := ipLookupTestServer(func(*http.Request) (*http.Response, error) {
		calls++
		return ipLookupTestResponse("", http.StatusServiceUnavailable), nil
	})
	for index := 0; index < ipLookupMinuteLimit; index++ {
		_, result := ipLookupTestRequest(t, server, `{"ip":"`+ipLookupTestIP+`"}`)
		if result.Status != "unavailable" {
			t.Fatal("available budget was not used", result)
		}
	}
	response, result := ipLookupTestRequest(t, server, `{"ip":"8.8.8.8"}`)
	retry, _ := strconv.Atoi(response.Header().Get("Retry-After"))
	if response.Code != http.StatusOK || result.Status != "rate_limited" || calls != ipLookupMinuteLimit || retry < 1 || retry > 61 || result.CheckedAt != nil {
		t.Fatal("minute limit was bypassed by another IP or provider error", result, calls, retry)
	}
	server.ipLookups.minuteStart = time.Now().Add(-time.Minute)
	_, result = ipLookupTestRequest(t, server, `{"ip":"8.8.8.8"}`)
	if result.Status != "unavailable" || calls != ipLookupMinuteLimit+1 {
		t.Fatal("minute budget did not reset")
	}
	server.ipLookups.dayUsed = ipLookupDayLimit
	response, result = ipLookupTestRequest(t, server, `{"ip":"8.8.8.8"}`)
	if result.Status != "rate_limited" || calls != ipLookupMinuteLimit+1 || response.Header().Get("Retry-After") == "" {
		t.Fatal("daily budget was bypassed")
	}
	server.ipLookups.dayStart = time.Now().Add(-24 * time.Hour)
	_, result = ipLookupTestRequest(t, server, `{"ip":"8.8.8.8"}`)
	if result.Status != "unavailable" || server.ipLookups.dayUsed != 1 {
		t.Fatal("daily budget did not reset")
	}
	service := newIPLookupService(ipLookupTestKey)
	first, _ := service.reserve(time.Now())
	second, _ := service.reserve(time.Now())
	if third, retry := service.reserve(time.Now()); first == nil || second == nil || third != nil || retry < 1 || service.minuteUsed != 2 {
		t.Fatal("parallel limit was not enforced")
	}
	first()
	third, _ := service.reserve(time.Now())
	if third == nil {
		t.Fatal("completed request did not release parallel slot")
	}
	second()
	third()
}

func TestIPLookupNeverFollowsRedirectsAndHonorsCancellation(t *testing.T) {
	calls := 0
	server := ipLookupTestServer(func(*http.Request) (*http.Response, error) {
		calls++
		response := ipLookupTestResponse(ipLookupTestKey, http.StatusFound)
		response.Header.Set("Location", "https://untrusted.example/?key="+ipLookupTestKey)
		return response, nil
	})
	_, result := ipLookupTestRequest(t, server, `{"ip":"`+ipLookupTestIP+`"}`)
	if calls != 1 || result.Status != "unavailable" {
		t.Fatal("provider redirect was followed", calls, result)
	}
	server = ipLookupTestServer(func(request *http.Request) (*http.Response, error) {
		deadline, bounded := request.Context().Deadline()
		if !bounded || deadline.After(time.Now().Add(ipLookupTimeout)) || server.ipLookups.client.Timeout != ipLookupTimeout {
			t.Fatal("provider request has no bounded deadline")
		}
		<-request.Context().Done()
		return nil, request.Context().Err()
	})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/ip-lookup", strings.NewReader(`{"ip":"`+ipLookupTestIP+`"}`))
	request.Header.Set("Content-Type", "application/json")
	ctx, cancel := context.WithTimeout(request.Context(), 20*time.Millisecond)
	defer cancel()
	request = request.WithContext(ctx)
	response := httptest.NewRecorder()
	started := time.Now()
	server.ipLookup(response, request)
	if response.Code != http.StatusOK || time.Since(started) > time.Second || !strings.Contains(response.Body.String(), `"status":"unavailable"`) || strings.Contains(response.Body.String(), ipLookupTestKey) || server.ipLookups.active != 0 {
		t.Fatal("canceled lookup stalled, exposed secrets or leaked a parallel slot")
	}
}
