package xray

import "testing"

func TestParseTrafficUsesOnlyKnownUserCounters(t *testing.T) {
	data := []byte(`{"stat":[
{"name":"user>>>b9ee76b7-2d56-4f5f-8a95-bb098715bbad@private-gateway>>>traffic>>>uplink","value":"1200"},
{"name":"user>>>b9ee76b7-2d56-4f5f-8a95-bb098715bbad@private-gateway>>>traffic>>>downlink","value":"3400"},
{"name":"inbound>>>vless-reality>>>traffic>>>downlink","value":"9999"}]}`)
	traffic, err := parseTraffic(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(traffic) != 1 || traffic[testID].UplinkBytes != 1200 || traffic[testID].DownlinkBytes != 3400 {
		t.Fatalf("unexpected user counters: %#v", traffic)
	}
	if _, err := parseTraffic([]byte(`{"stat":[{"name":"user>>>` + testID + `@private-gateway>>>traffic>>>uplink","value":"-1"}]}`)); err == nil {
		t.Fatal("accepted invalid counter")
	}
	zero, err := parseTraffic([]byte(`{"stat":[{"name":"user>>>` + testID + `@private-gateway>>>traffic>>>uplink"}]}`))
	if err != nil || zero[testID].UplinkBytes != 0 {
		t.Fatal("omitted zero counter should be accepted")
	}
}
