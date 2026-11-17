package storage

import (
	"github.com/rvburov/private-gateway/internal/model"
	"testing"
)

func TestBridgeRejectsUnusableLiteralAddresses(t *testing.T) {
	for _, host := range []string{"0.0.0.0", "::", "127.0.0.1", "::1", "::ffff:127.0.0.1", "224.0.0.1", "ff02::1", "169.254.169.254", "fe80::1", "256.1.1.1", "123", "1.2.3.999", "bad-.example"} {
		t.Run(host, func(t *testing.T) {
			if err := validateBridge(model.Node{Host: "exit.example"}, &model.BridgeNode{Host: host, TCPPort: 443}); err == nil {
				t.Fatal("unusable bridge address accepted")
			}
			if err := validateBridge(model.Node{Host: host}, &model.BridgeNode{Host: "entry.example", TCPPort: 443}); err == nil {
				t.Fatal("installer-incompatible target address accepted")
			}
		})
	}
	for _, host := range []string{"198.51.100.2", "2001:db8::2", "::ffff:198.51.100.2", "10.0.0.2", "exit.example", "node-1.example", "xn--e1afmkfd.xn--p1ai"} {
		if err := validateBridge(model.Node{Host: "other.example"}, &model.BridgeNode{Host: host, TCPPort: 443}); err != nil {
			t.Fatalf("valid bridge address %s rejected", host)
		}
	}
}
