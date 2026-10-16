package xray

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Traffic contains byte counters since the running Xray process started.
type Traffic struct {
	UplinkBytes   int64 `json:"uplink_bytes"`
	DownlinkBytes int64 `json:"downlink_bytes"`
}

func QueryTraffic(ctx context.Context, binary, address string) (map[string]Traffic, error) {
	if binary == "" || address == "" {
		return nil, errors.New("Xray statistics are not configured")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, binary, "api", "statsquery", "--server="+address, "--pattern=user>>>").Output()
	if err != nil {
		return nil, errors.New("Xray statistics are unavailable")
	}
	return parseTraffic(output)
}

func parseTraffic(output []byte) (map[string]Traffic, error) {
	if len(output) > 2<<20 {
		return nil, errors.New("Xray statistics response is too large")
	}
	var response struct {
		Stats []struct {
			Name  string          `json:"name"`
			Value json.RawMessage `json:"value"`
		} `json:"stat"`
	}
	if err := json.Unmarshal(output, &response); err != nil {
		return nil, errors.New("invalid Xray statistics response")
	}
	result := make(map[string]Traffic)
	for _, stat := range response.Stats {
		parts := strings.Split(stat.Name, ">>>")
		if len(parts) != 4 || parts[0] != "user" || parts[2] != "traffic" ||
			!strings.HasSuffix(parts[1], "@private-gateway") ||
			(parts[3] != "uplink" && parts[3] != "downlink") {
			continue
		}
		id := strings.TrimSuffix(parts[1], "@private-gateway")
		if !uuidPattern.MatchString(id) {
			continue
		}
		var value int64
		if len(stat.Value) > 0 {
			var err error
			value, err = strconv.ParseInt(strings.Trim(string(stat.Value), "\""), 10, 64)
			if err != nil || value < 0 {
				return nil, errors.New("invalid Xray traffic counter")
			}
		}
		id = strings.ToLower(id)
		traffic := result[id]
		if parts[3] == "uplink" {
			traffic.UplinkBytes = value
		} else {
			traffic.DownlinkBytes = value
		}
		result[id] = traffic
	}
	return result, nil
}
