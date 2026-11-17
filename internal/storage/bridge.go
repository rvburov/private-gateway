package storage

import (
	"context"
	"encoding/json"
	"net"
	"strings"

	"github.com/google/uuid"
	"github.com/rvburov/private-gateway/internal/model"
	"github.com/rvburov/private-gateway/internal/storage/db"
)

func normalizedHost(host string) string {
	if ip := net.ParseIP(host); ip != nil {
		return ip.String()
	}
	return strings.ToLower(strings.TrimSuffix(host, "."))
}

// Адреса моста и его выхода должны совпадать с ограничениями установщика.
// Домен не разрешается здесь: принадлежность адреса самому входному VPS
// проверяется установщиком до запуска listeners.
func validBridgeHost(host string) bool {
	if !validEndpoint(host, 1) {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return !ip.IsUnspecified() && !ip.IsLoopback() && !ip.IsMulticast() && !ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast()
	}
	// Ошибочный IPv4 и чисто числовой хост не считаются DNS-именем.
	return strings.IndexFunc(host, func(r rune) bool { return r != '.' && (r < '0' || r > '9') }) >= 0
}

func validateBridge(node model.Node, bridge *model.BridgeNode) error {
	if bridge == nil || !validBridgeHost(bridge.Host) || !validBridgeHost(node.Host) ||
		!validEndpoint(bridge.Host, bridge.TCPPort) || bridge.TCPPort == 22 {
		return ErrInvalid
	}
	// Входной узел должен отличаться от выходного. DNS-псевдонимы дополнительно
	// проверяются при установке, без внешних DNS-запросов внутри транзакции API.
	if normalizedHost(bridge.Host) == normalizedHost(node.Host) {
		return ErrInvalid
	}
	if node.XHTTP != nil {
		if !validEndpoint(bridge.Host, bridge.XHTTPPort) || bridge.XHTTPPort == bridge.TCPPort || bridge.XHTTPPort == 22 {
			return ErrInvalid
		}
	} else if bridge.XHTTPPort != 0 {
		return ErrInvalid
	}
	return nil
}

// SetNodeConnection меняет только адреса выдаваемых профилей. Установка моста и
// фактическая доступность проверяются отдельно; ревизия выходного Xray не меняется.
func (s *Store) SetNodeConnection(ctx context.Context, nodeID, mode string, bridge *model.BridgeNode) (model.Node, error) {
	if _, err := uuid.Parse(nodeID); err != nil {
		return model.Node{}, ErrNotFound
	}
	if mode != "direct" && mode != "bridge" || mode == "direct" && bridge != nil {
		return model.Node{}, ErrInvalid
	}
	if bridge != nil {
		copy := *bridge
		copy.Host = normalizedHost(copy.Host)
		bridge = &copy
	}
	var out model.Node
	err := s.transaction(ctx, func(q *db.Queries) error {
		current, err := q.GetNode(ctx, uid(nodeID))
		if err != nil {
			return err
		}
		node, err := nodeModel(current)
		if err != nil {
			return err
		}
		var config []byte
		if mode == "bridge" {
			if err := validateBridge(node, bridge); err != nil {
				return err
			}
			nodes, err := q.ListNodes(ctx)
			if err != nil {
				return err
			}
			listeners := 1
			if bridge.XHTTPPort != 0 {
				listeners++
			}
			for _, other := range nodes {
				if other.ID == current.ID || other.ConnectionMode != "bridge" {
					continue
				}
				public, err := nodeModel(other)
				if err != nil {
					return err
				}
				if public.Bridge == nil {
					return ErrConnectionUnavailable
				}
				if normalizedHost(public.Bridge.Host) != bridge.Host {
					continue
				}
				for _, assigned := range []int{public.Bridge.TCPPort, public.Bridge.XHTTPPort} {
					if assigned != 0 {
						listeners++
					}
					if assigned != 0 && (assigned == bridge.TCPPort || assigned == bridge.XHTTPPort) {
						return ErrConflict
					}
				}
			}
			if listeners > 64 {
				return ErrInvalid
			}
			config, err = json.Marshal(bridge)
			if err != nil {
				return err
			}
		}
		row, err := q.SetNodeConnection(ctx, db.SetNodeConnectionParams{ID: current.ID, ConnectionMode: mode, BridgeConfig: config})
		if err != nil {
			return err
		}
		out, err = nodeModel(row)
		if err != nil {
			return err
		}
		return q.InsertAudit(ctx, db.InsertAuditParams{Action: "node.connection"})
	})
	return out, err
}

// profileNode заменяет только адрес подключения, сохраняя UUID, ключи REALITY,
// SNI и параметры транспорта конечного VPS. Через TCP-мост H3 не выдаётся.
func profileNode(node model.Node) (model.Node, error) {
	switch node.ConnectionMode {
	case "direct":
		return node, nil
	case "bridge":
		if err := validateBridge(node, node.Bridge); err != nil {
			return model.Node{}, ErrConnectionUnavailable
		}
		node.Host, node.Port = node.Bridge.Host, node.Bridge.TCPPort
		if node.XHTTP != nil {
			xhttp := *node.XHTTP
			xhttp.Port = node.Bridge.XHTTPPort
			node.XHTTP = &xhttp
		}
		node.H3 = nil
		return node, nil
	default:
		return model.Node{}, ErrConnectionUnavailable
	}
}

type BridgeListener struct {
	Name       string `json:"name"`
	Transport  string `json:"transport"`
	ListenPort int    `json:"listen_port"`
	TargetHost string `json:"target_host"`
	TargetPort int    `json:"target_port"`
}

type BridgeConfig struct {
	Version   int              `json:"version"`
	Listeners []BridgeListener `json:"listeners"`
}

// BridgeConfiguration не содержит секретов клиента или сервера. Она собирает
// все назначения на тот же входной хост, чтобы новый VPS не затёр старые listeners.
func (s *Store) BridgeConfiguration(ctx context.Context, nodeID string) (BridgeConfig, error) {
	var out BridgeConfig
	err := s.transaction(ctx, func(q *db.Queries) error {
		row, err := q.GetNode(ctx, uid(nodeID))
		if err != nil {
			return err
		}
		node, err := nodeModel(row)
		if err != nil {
			return err
		}
		if node.ConnectionMode != "bridge" || validateBridge(node, node.Bridge) != nil {
			return ErrConnectionUnavailable
		}
		rows, err := q.ListNodes(ctx)
		if err != nil {
			return err
		}
		listeners := make([]BridgeListener, 0)
		for _, row := range rows {
			candidate, err := nodeModel(row)
			if err != nil {
				return err
			}
			if candidate.ConnectionMode != "bridge" || candidate.Bridge == nil || normalizedHost(candidate.Bridge.Host) != normalizedHost(node.Bridge.Host) {
				continue
			}
			if err := validateBridge(candidate, candidate.Bridge); err != nil {
				return ErrConnectionUnavailable
			}
			listeners = append(listeners, BridgeListener{Name: candidate.ID + "-tcp", Transport: "tcp", ListenPort: candidate.Bridge.TCPPort, TargetHost: candidate.Host, TargetPort: candidate.Port})
			if candidate.XHTTP != nil {
				listeners = append(listeners, BridgeListener{Name: candidate.ID + "-xhttp", Transport: "xhttp", ListenPort: candidate.Bridge.XHTTPPort, TargetHost: candidate.Host, TargetPort: candidate.XHTTP.Port})
			}
		}
		out = BridgeConfig{Version: 1, Listeners: listeners}
		return nil
	})
	return out, err
}
