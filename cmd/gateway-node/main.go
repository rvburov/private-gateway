package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/rvburov/private-gateway/internal/model"
	"github.com/rvburov/private-gateway/internal/nodeagent"
	"github.com/rvburov/private-gateway/internal/xray"
)

func value(name, fallback string) string {
	if raw := os.Getenv(name); raw != "" {
		return raw
	}
	return fallback
}

func port(name string, fallback int) (int, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > 65535 {
		return 0, fmt.Errorf("invalid %s", name)
	}
	return n, nil
}

func restartCommand(name string) ([]string, error) {
	var argv []string
	if err := json.Unmarshal([]byte(os.Getenv(name)), &argv); err != nil || len(argv) == 0 || argv[0] == "" {
		return nil, fmt.Errorf("%s must be a nonempty JSON argv array", name)
	}
	return argv, nil
}

func options() (nodeagent.Options, error) {
	var out nodeagent.Options
	out.ControlURL = os.Getenv("GATEWAY_CONTROL_URL")
	out.NodeID = os.Getenv("GATEWAY_NODE_ID")
	out.TokenFile = value("GATEWAY_NODE_TOKEN_FILE", "/state/agent.token")
	private := os.Getenv("GATEWAY_REALITY_PRIVATE_KEY")
	public, err := xray.PublicKey(private)
	if err != nil {
		return out, errors.New("invalid local REALITY private key")
	}
	tcpPort, err := port("GATEWAY_XRAY_PORT", 443)
	if err != nil {
		return out, err
	}
	publicPort, err := port("GATEWAY_PORT", 8443)
	if err != nil {
		return out, err
	}
	settings := xray.Settings{PrivateKey: private, Listen: value("GATEWAY_XRAY_LISTEN", "0.0.0.0"),
		Port: tcpPort, ServerName: os.Getenv("GATEWAY_SERVER_NAME"),
		Target: os.Getenv("GATEWAY_REALITY_TARGET"), ShortID: os.Getenv("GATEWAY_SHORT_ID")}
	out.Expected = model.Node{ID: out.NodeID, Host: os.Getenv("GATEWAY_HOST"), Port: publicPort,
		PublicKey: public, ServerName: settings.ServerName, ShortID: settings.ShortID}
	if os.Getenv("GATEWAY_XHTTP_ENABLED") == "1" {
		xPort, err := port("GATEWAY_XHTTP_PORT", 8444)
		if err != nil {
			return out, err
		}
		xPrivate := os.Getenv("GATEWAY_XHTTP_PRIVATE_KEY")
		xPublic, err := xray.PublicKey(xPrivate)
		if err != nil {
			return out, errors.New("invalid local XHTTP private key")
		}
		settings.XHTTP = &xray.XHTTPSettings{Port: xPort, PrivateKey: xPrivate,
			ShortID: os.Getenv("GATEWAY_XHTTP_SHORT_ID"), Path: os.Getenv("GATEWAY_XHTTP_PATH")}
		out.Expected.XHTTP = &model.XHTTPNode{Port: xPort, PublicKey: xPublic,
			ShortID: settings.XHTTP.ShortID, Path: settings.XHTTP.Path, Mode: "auto"}
	}
	if os.Getenv("GATEWAY_XHTTP_H3_ENABLED") == "1" {
		publicH3Port, err := port("GATEWAY_XHTTP_H3_PORT", 8444)
		if err != nil {
			return out, err
		}
		internalPort, err := port("GATEWAY_XHTTP_H3_INTERNAL_PORT", 8081)
		if err != nil {
			return out, err
		}
		settings.H3 = &xray.H3Settings{InternalPort: internalPort, Path: os.Getenv("GATEWAY_XHTTP_H3_PATH")}
		out.Expected.H3 = &model.H3Node{Port: publicH3Port,
			ServerName: os.Getenv("GATEWAY_XHTTP_H3_DOMAIN"), Path: settings.H3.Path, Mode: "auto"}
	}
	if _, err := xray.RenderTCP(settings, nil); err != nil {
		return out, errors.New("invalid local TCP Xray configuration")
	}
	if settings.XHTTP != nil || settings.H3 != nil {
		if _, err := xray.RenderXHTTP(settings, nil); err != nil {
			return out, errors.New("invalid local XHTTP Xray configuration")
		}
	}
	const testID = "00000000-0000-4000-8000-000000000000"
	if _, err := xray.Profile(out.Expected, testID, "validation"); err != nil {
		return out, errors.New("invalid local public node metadata")
	}
	if out.Expected.XHTTP != nil {
		if _, err := xray.XHTTPProfile(out.Expected, testID, "validation"); err != nil {
			return out, errors.New("invalid local public XHTTP metadata")
		}
	}
	if out.Expected.H3 != nil {
		if _, err := xray.H3Profile(out.Expected, testID, "validation"); err != nil {
			return out, errors.New("invalid local public H3 metadata")
		}
	}
	tcpRestart, err := restartCommand("GATEWAY_XRAY_RESTART")
	if err != nil {
		return out, err
	}
	tcp := xray.Options{Settings: settings, ConfigPath: value("GATEWAY_XRAY_CONFIG", "/state/xray.json"),
		Binary: value("GATEWAY_XRAY_BINARY", "xray"), RestartCommand: tcpRestart, Renderer: xray.RenderTCP}
	var xhttp *xray.Options
	if settings.XHTTP != nil || settings.H3 != nil {
		xRestart, err := restartCommand("GATEWAY_XHTTP_RESTART")
		if err != nil {
			return out, err
		}
		xhttp = &xray.Options{Settings: settings, ConfigPath: value("GATEWAY_XHTTP_CONFIG", "/state/xhttp.json"),
			Binary: value("GATEWAY_XRAY_BINARY", "xray"), RestartCommand: xRestart, Renderer: xray.RenderXHTTP}
	}
	out.Applier = xray.NewTransportManager(tcp, xhttp)
	return out, nil
}

func run() error {
	if len(os.Args) != 2 || (os.Args[1] != "enroll" && os.Args[1] != "serve") {
		return errors.New("usage: gateway-node enroll|serve")
	}
	configured, err := options()
	if err != nil {
		return err
	}
	agent, err := nodeagent.New(configured)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if os.Args[1] == "enroll" {
		data, err := io.ReadAll(io.LimitReader(os.Stdin, 128))
		if err != nil {
			return errors.New("cannot read join token from stdin")
		}
		join := strings.TrimSpace(string(data))
		if err := agent.Enroll(ctx, join); err != nil {
			return err
		}
		fmt.Println("agent enrolled; token saved")
		return nil
	}
	return agent.Serve(ctx, func(err error) {
		// Ошибки HTTP и Xray намеренно обезличены; токены и UUID не выводятся.
		slog.Warn("node poll failed", "reason", err.Error())
	})
}

func main() {
	if err := run(); err != nil {
		slog.Error("node agent failed", "reason", err.Error())
		os.Exit(1)
	}
}
