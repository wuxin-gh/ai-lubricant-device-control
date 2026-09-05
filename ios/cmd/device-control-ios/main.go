// Command device-control-ios is the host-side iOS device driver: it drives a
// real iPhone over USB/LAN via go-ios + WebDriverAgent, and dials a
// device-control server exactly like the Android app does.
//
// Usage:
//
//	device-control-ios pair --server URL --code CODE [--state PATH]
//	device-control-ios run  [--state PATH] [--transport usb|network] [--udid X]
//	                       [--wda-bundle ID --xctest NAME --wda-port N]
//
// Pair redeems a pairing code (POST /pair) and stores the long-lived credential
// to a 0600 file. Run loads the credential and runs the connection loop,
// driving the iPhone and serving inbound calls.
//
// The Go package under internal/ is the integration surface for an existing Go
// node program: import device-control/ios and call New() rather than the CLI.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"device-control/ios/internal/commands"
	"device-control/ios/internal/creds"
	"device-control/ios/internal/devicelink"
	"device-control/ios/internal/dispatcher"
	"device-control/ios/internal/pairclient"
	"device-control/ios/internal/screen"
	"device-control/ios/internal/wsclient"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "pair":
		cmdPair(os.Args[2:])
	case "run":
		cmdRun(os.Args[2:])
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `device-control-ios — iOS device driver for device-control

  device-control-ios pair --server URL --code CODE [--state PATH]
  device-control-ios run  [--state PATH] [--transport usb|network] [--udid X]
                         [--wda-bundle ID --xctest NAME --wda-port N]

  --state defaults to ./device-control-ios.json (0600).
  run also needs the WDA connection params (bundle id, xctest name, forwarded port);
  these come from your built WebDriverAgent — see ios/README.md.`)
}

func stateFlag(fs *flag.FlagSet) *string {
	p := fs.String("state", "./device-control-ios.json", "path to the credential file (0600)")
	return p
}

func cmdPair(args []string) {
	fs := flag.NewFlagSet("pair", flag.ExitOnError)
	state := stateFlag(fs)
	server := fs.String("server", "", "server base URL, e.g. https://host")
	code := fs.String("code", "", "pairing code")
	_ = fs.Parse(args)

	if *server == "" || *code == "" {
		fmt.Fprintln(os.Stderr, "pair: --server and --code are required")
		os.Exit(2)
	}

	pc := pairclient.New()
	result, err := pc.Pair(*server, *code)
	if err != nil {
		log.Fatalf("pair failed: %v", err)
	}
	store := creds.New(*state)
	if err := store.Save(creds.Credential{
		ServerURL: result.ServerURL,
		DeviceID:  result.DeviceID,
		Token:     result.Token,
	}); err != nil {
		log.Fatalf("save credential: %v", err)
	}
	fmt.Printf("paired: device_id=%s server=%s\n", result.DeviceID, result.ServerURL)
	fmt.Println("now run: device-control-ios run")
}

func cmdRun(args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	state := stateFlag(fs)
	transport := fs.String("transport", "usb", "usb|network")
	udid := fs.String("udid", "", "device UDID (default: first available)")
	wdaBundle := fs.String("wda-bundle", "", "WebDriverAgentRunner bundle id")
	xctest := fs.String("xctest", "WebDriverAgentRunner.xctest", "xctest config name")
	wdaPort := fs.Int("wda-port", 0, "forwarded WDA HTTP port")
	_ = fs.Parse(args)

	store := creds.New(*state)
	cred, err := store.Load()
	if err != nil {
		log.Fatalf("load credential: %v", err)
	}
	if cred == nil {
		log.Fatal("no credential; run 'device-control-ios pair' first")
	}

	// Connect to the iPhone and launch WDA.
	link := devicelink.New(devicelink.Options{
		UDID:         *udid,
		Transport:    *transport,
		WDABundleID:  *wdaBundle,
		XCTestConfig: *xctest,
		WDAPort:      *wdaPort,
	})
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := link.Start(ctx); err != nil {
		log.Fatalf("connect iPhone: %v", err)
	}
	defer link.Close()

	// Build device_info for register (platform, screen).
	info, scale, _ := link.ScreenInfo(ctx)
	deviceInfo, _ := json.Marshal(map[string]any{
		"platform":    "ios",
		"os_version":  "",
		"model":       *udid,
		"app_version": "0.1.0",
		"screen": map[string]any{
			"w":       info.Width,
			"h":       info.Height,
			"density": info.DensityDpi,
			"scale":   scale,
		},
	})

	d := dispatcher.New(commands.Build(link, screen.New()))
	client := &wsclient.Client{
		Dispatcher: d,
		DeviceInfo: deviceInfo,
		OnWipe: func() {
			if err := store.Clear(); err != nil {
				log.Printf("wipe credential: %v", err)
			}
			log.Print("credential wiped (auth failed); re-pair")
		},
	}

	// Run the connection loop until fatal or interrupted.
	err = client.ConnectLoop(ctx, cred)
	if err != nil {
		log.Printf("connection ended: %v", err)
	}
	// Give in-flight goroutines a beat to flush before exit.
	time.Sleep(100 * time.Millisecond)
}
