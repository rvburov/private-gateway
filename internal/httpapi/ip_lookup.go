package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	ipLookupSource        = "IP2Location.io"
	ipLookupEndpoint      = "https://api.ip2location.io/"
	ipLookupTimeout       = 6 * time.Second
	ipLookupResponseLimit = 128 << 10
	ipLookupMinuteLimit   = 12
	ipLookupDayLimit      = 200
	ipLookupParallelLimit = 2
)

type ipLookupResult struct {
	IP        string         `json:"ip"`
	Source    string         `json:"source"`
	Status    string         `json:"status"`
	CheckedAt *time.Time     `json:"checkedAt"`
	Detail    string         `json:"detail"`
	Data      map[string]any `json:"data"`
}

// Лимиты общие для всех сеансов: смена устройства или токена не расходует отдельную квоту.
// В памяти остаются только счётчики, без адресов и истории ответов провайдера.
type ipLookupService struct {
	key         string
	client      *http.Client
	mu          sync.Mutex
	active      int
	minuteStart time.Time
	minuteUsed  int
	dayStart    time.Time
	dayUsed     int
}

func newIPLookupService(key string) *ipLookupService {
	transport := http.DefaultTransport
	if base, ok := transport.(*http.Transport); ok {
		copy := base.Clone()
		copy.ResponseHeaderTimeout = ipLookupTimeout
		transport = copy
	}
	return &ipLookupService{key: key, client: &http.Client{
		Transport: transport, Timeout: ipLookupTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (s *ipLookupService) reserve(now time.Time) (func(), int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.minuteStart.IsZero() || now.Sub(s.minuteStart) >= time.Minute {
		s.minuteStart, s.minuteUsed = now, 0
	}
	if s.dayStart.IsZero() || now.Sub(s.dayStart) >= 24*time.Hour {
		s.dayStart, s.dayUsed = now, 0
	}
	if s.dayUsed >= ipLookupDayLimit {
		return nil, max(1, int(s.dayStart.Add(24*time.Hour).Sub(now).Seconds())+1)
	}
	if s.minuteUsed >= ipLookupMinuteLimit {
		return nil, max(1, int(s.minuteStart.Add(time.Minute).Sub(now).Seconds())+1)
	}
	if s.active >= ipLookupParallelLimit {
		return nil, 1
	}
	s.active++
	s.minuteUsed++
	s.dayUsed++
	return func() {
		s.mu.Lock()
		s.active--
		s.mu.Unlock()
	}, 0
}

// Частные, служебные и документационные сети не обозначают внешний выход устройства.
var ipLookupExcluded = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"), netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"), netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("3fff::/20"),
}

func ipLookupAddress(raw string) (netip.Addr, bool) {
	ip, err := netip.ParseAddr(raw)
	if err != nil || ip.Zone() != "" {
		return netip.Addr{}, false
	}
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return netip.Addr{}, false
	}
	if ip.Is6() && !netip.MustParsePrefix("2000::/3").Contains(ip) {
		return netip.Addr{}, false
	}
	for _, prefix := range ipLookupExcluded {
		if prefix.Contains(ip) {
			return netip.Addr{}, false
		}
	}
	return ip, true
}

func (s *Server) ipLookup(w http.ResponseWriter, r *http.Request) {
	var input struct {
		IP string `json:"ip"`
	}
	if !decode(w, r, &input) {
		return
	}
	ip, valid := ipLookupAddress(input.IP)
	if !valid {
		fail(w, http.StatusBadRequest, "ip must be a public IPv4 or IPv6 address")
		return
	}
	result := ipLookupResult{IP: ip.String(), Source: ipLookupSource, Status: "not_configured", Detail: "Расширенные сведения IP не настроены: на сервере не задан ключ IP2Location.io."}
	lookup := s.ipLookups
	if lookup == nil || lookup.key == "" {
		respond(w, http.StatusOK, result)
		return
	}
	finish, retry := lookup.reserve(time.Now())
	if finish == nil {
		w.Header().Set("Retry-After", strconv.Itoa(retry))
		result.Status, result.Detail = "rate_limited", "Достигнут лимит расширенных проверок IP. Повторите позже."
		respond(w, http.StatusOK, result)
		return
	}
	defer finish()
	result = lookup.lookup(r.Context(), ip)
	respond(w, http.StatusOK, result)
}

func (s *ipLookupService) lookup(ctx context.Context, ip netip.Addr) ipLookupResult {
	result := ipLookupResult{IP: ip.String(), Source: ipLookupSource, Status: "unavailable", Detail: "IP2Location.io не предоставил сведения об этом IP. Проверьте доступность сервиса, ключ и условия доступа."}
	ctx, cancel := context.WithTimeout(ctx, ipLookupTimeout)
	defer cancel()
	// Адрес назначения фиксирован. Ключ требует query-параметра по контракту провайдера;
	// URL, тело ошибки и исходная сетевая ошибка не попадают в ответ или журнал.
	query := url.Values{"key": {s.key}, "ip": {ip.String()}}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, ipLookupEndpoint+"?"+query.Encode(), nil)
	if err != nil {
		return result
	}
	request.Header.Set("Accept", "application/json")
	response, err := s.client.Do(request)
	if err != nil {
		return result
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusTooManyRequests {
		result.Status, result.Detail = "rate_limited", "IP2Location.io ограничил число запросов. Повторите проверку позже."
		return result
	}
	if response.StatusCode != http.StatusOK {
		return result
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, ipLookupResponseLimit+1))
	if err != nil || len(body) > ipLookupResponseLimit || ctx.Err() != nil {
		return result
	}
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.UseNumber()
	var raw map[string]any
	if decoder.Decode(&raw) != nil || decoder.Decode(&struct{}{}) != io.EOF || raw == nil || raw["error"] != nil {
		return result
	}
	if address, present := raw["ip"]; present && address != nil {
		text, ok := address.(string)
		seen, public := ipLookupAddress(text)
		if !ok || !public || seen != ip {
			return result
		}
	}
	data, values, complete := normalizeIPLookupObject(raw, ipLookupFields, s.key)
	// Один повторённый IP без других сведений не считается успешным обогащением.
	if values == 0 || values == 1 && data["ip"] != nil {
		return result
	}
	now := time.Now().UTC()
	result.Data, result.CheckedAt = data, &now
	result.Status, result.Detail = "available", "Сведения получены из базы IP2Location.io. Это данные провайдера, а не проверка маршрута или гарантия доступа к сайтам."
	if !complete {
		result.Status, result.Detail = "partial", "IP2Location.io сообщил часть сведений. Отсутствующие поля неизвестны; их доступность зависит от данных и условий доступа."
	}
	return result
}

type ipLookupField struct {
	kind   string
	fields map[string]ipLookupField
	min    float64
	max    float64
}

func ipLookupText() ipLookupField { return ipLookupField{kind: "text"} }
func ipLookupBool() ipLookupField { return ipLookupField{kind: "bool"} }
func ipLookupNumber(minimum, maximum float64, integer bool) ipLookupField {
	kind := "number"
	if integer {
		kind = "integer"
	}
	return ipLookupField{kind: kind, min: minimum, max: maximum}
}
func ipLookupCode(maximum float64) ipLookupField {
	return ipLookupField{kind: "code", min: 0, max: maximum}
}
func ipLookupObject(fields map[string]ipLookupField) ipLookupField {
	return ipLookupField{kind: "object", fields: fields}
}
func ipLookupTranslation() ipLookupField {
	return ipLookupObject(map[string]ipLookupField{"lang": ipLookupText(), "value": ipLookupText()})
}

// Закрытая схема сохраняет исходные имена JSON, но не произвольные новые поля ответа.
// null и отсутствующие значения остаются неизвестными; SDK с нулевыми bool/int здесь не подходит.
var ipLookupFields = map[string]ipLookupField{
	"ip": ipLookupText(), "country_code": ipLookupText(), "country_name": ipLookupText(),
	"region_name": ipLookupText(), "district": ipLookupText(), "city_name": ipLookupText(),
	"latitude": ipLookupNumber(-90, 90, false), "longitude": ipLookupNumber(-180, 180, false),
	"zip_code": ipLookupText(), "time_zone": ipLookupText(), "asn": ipLookupCode(4294967295),
	"as": ipLookupText(), "isp": ipLookupText(), "domain": ipLookupText(), "net_speed": ipLookupText(),
	"idd_code": ipLookupText(), "area_code": ipLookupText(), "weather_station_code": ipLookupText(),
	"weather_station_name": ipLookupText(), "mcc": ipLookupText(), "mnc": ipLookupText(),
	"mobile_brand": ipLookupText(), "elevation": ipLookupNumber(-1000, 100000, false),
	"usage_type": ipLookupText(), "address_type": ipLookupText(), "ads_category": ipLookupText(), "ads_category_name": ipLookupText(),
	"as_info": ipLookupObject(map[string]ipLookupField{
		"as_number": ipLookupCode(4294967295), "as_name": ipLookupText(), "as_domain": ipLookupText(), "as_usage_type": ipLookupText(), "as_cidr": ipLookupText(),
	}),
	"continent": ipLookupObject(map[string]ipLookupField{
		"name": ipLookupText(), "code": ipLookupText(), "hemisphere": {kind: "texts"}, "translation": ipLookupTranslation(),
	}),
	"country": ipLookupObject(map[string]ipLookupField{
		"name": ipLookupText(), "alpha3_code": ipLookupText(), "numeric_code": ipLookupCode(999),
		"demonym": ipLookupText(), "flag": ipLookupText(), "capital": ipLookupText(),
		"total_area": ipLookupNumber(0, 1e12, false), "population": ipLookupNumber(0, 1e12, true),
		"currency": ipLookupObject(map[string]ipLookupField{"code": ipLookupText(), "name": ipLookupText(), "symbol": ipLookupText()}),
		"language": ipLookupObject(map[string]ipLookupField{"code": ipLookupText(), "name": ipLookupText()}),
		"tld":      ipLookupText(), "translation": ipLookupTranslation(),
	}),
	"region": ipLookupObject(map[string]ipLookupField{"name": ipLookupText(), "code": ipLookupText(), "translation": ipLookupTranslation()}),
	"city":   ipLookupObject(map[string]ipLookupField{"name": ipLookupText(), "translation": ipLookupTranslation()}),
	"time_zone_info": ipLookupObject(map[string]ipLookupField{
		"olson": ipLookupText(), "current_time": ipLookupText(), "gmt_offset": ipLookupNumber(-86400, 86400, true),
		"is_dst": ipLookupBool(), "abbreviation": ipLookupText(), "dst_start_date": ipLookupText(), "dst_end_date": ipLookupText(), "sunrise": ipLookupText(), "sunset": ipLookupText(),
	}),
	"geotargeting": ipLookupObject(map[string]ipLookupField{"metro": ipLookupText()}),
	"is_proxy":     ipLookupBool(), "fraud_score": ipLookupNumber(0, 99, true),
	"proxy": ipLookupObject(map[string]ipLookupField{
		"last_seen": ipLookupNumber(0, 1e9, true), "proxy_type": ipLookupText(), "threat": ipLookupText(), "provider": ipLookupText(),
		"is_vpn": ipLookupBool(), "is_tor": ipLookupBool(), "is_data_center": ipLookupBool(),
		"is_public_proxy": ipLookupBool(), "is_web_proxy": ipLookupBool(), "is_web_crawler": ipLookupBool(),
		"is_ai_crawler": ipLookupBool(), "is_residential_proxy": ipLookupBool(), "is_consumer_privacy_network": ipLookupBool(),
		"is_enterprise_private_network": ipLookupBool(), "is_spammer": ipLookupBool(), "is_scanner": ipLookupBool(), "is_botnet": ipLookupBool(), "is_bogon": ipLookupBool(),
	}),
}

func normalizeIPLookupObject(raw map[string]any, fields map[string]ipLookupField, secret string) (map[string]any, int, bool) {
	result := make(map[string]any, len(fields))
	values, complete := 0, true
	for name, field := range fields {
		value, count, valid := normalizeIPLookupValue(raw[name], field, secret)
		result[name] = value
		values += count
		complete = complete && valid
	}
	return result, values, complete
}

func normalizeIPLookupValue(raw any, field ipLookupField, secret string) (any, int, bool) {
	if raw == nil {
		return nil, 0, false
	}
	switch field.kind {
	case "object":
		if object, ok := raw.(map[string]any); ok {
			return normalizeIPLookupObject(object, field.fields, secret)
		}
	case "bool":
		if value, ok := raw.(bool); ok {
			return value, 1, true
		}
	case "text":
		if text, ok := raw.(string); ok && validIPLookupText(text, secret) {
			return text, 1, true
		}
	case "texts":
		if list, ok := raw.([]any); ok && len(list) > 0 && len(list) <= 8 {
			texts := make([]string, 0, len(list))
			for _, item := range list {
				text, ok := item.(string)
				if !ok || !validIPLookupText(text, secret) {
					return nil, 0, false
				}
				texts = append(texts, text)
			}
			return texts, 1, true
		}
	case "number", "integer", "code":
		var text string
		switch number := raw.(type) {
		case json.Number:
			text = string(number)
		case string:
			if !validIPLookupText(number, secret) {
				return nil, 0, false
			}
			text = number
		default:
			return nil, 0, false
		}
		value, err := strconv.ParseFloat(text, 64)
		if err == nil && !math.IsNaN(value) && !math.IsInf(value, 0) && value >= field.min && value <= field.max && (field.kind == "number" || math.Trunc(value) == value) {
			if field.kind == "code" {
				// ISO-коды могут содержать значимые ведущие нули; ASN тоже является кодом.
				return raw, 1, true
			}
			return value, 1, true
		}
	}
	return nil, 0, false
}

func validIPLookupText(text, secret string) bool {
	if len(text) > 2048 || strings.TrimSpace(text) == "" || strings.TrimSpace(text) == "-" || secret != "" && strings.Contains(text, secret) {
		return false
	}
	for _, character := range text {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}
