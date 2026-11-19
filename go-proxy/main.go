package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"

	"pi-proxy/internal/arena"
	"pi-proxy/internal/aws"
	"pi-proxy/internal/binlog"
	"pi-proxy/internal/config"
	"pi-proxy/internal/health"
	"pi-proxy/internal/jobs"
	"pi-proxy/internal/proxy"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "export-logs" {
		runExportCLI(os.Args[2:])
		return
	}
	runServe()
}

func runExportCLI(args []string) {
	fs := flag.NewFlagSet("export-logs", flag.ExitOnError)
	file := fs.String("file", "", "Path to .bin log file")
	tail := fs.Int("tail", 0, "Number of trailing records (0 = all)")
	_ = fs.Parse(args)
	if *file == "" {
		log.Fatal("--file is required")
	}
	if _, err := binlog.ExportJSONL(*file, os.Stdout, *tail); err != nil {
		log.Fatalf("export failed: %v", err)
	}
}

func runServe() {
	exeDir, _ := os.Executable()
	baseDir := filepath.Dir(exeDir)
	logDir := envStr("PI_LOG_DIR", filepath.Join(baseDir, "logs"))
	allowlist := envStr("PI_ALLOWLIST", filepath.Join(baseDir, "allowlist.json"))

	maxBytes := int64(envInt("PI_MAX_LOG_BYTES", 10*1024*1024))
	maxFiles := envInt("PI_MAX_LOG_FILES", 5)

	apiLog, err := binlog.NewLogger(filepath.Join(logDir, "api-audit.bin"), maxBytes, maxFiles)
	if err != nil {
		log.Fatalf("apiLog init: %v", err)
	}
	egressLog, err := binlog.NewLogger(filepath.Join(logDir, "egress.bin"), maxBytes, maxFiles)
	if err != nil {
		log.Fatalf("egressLog init: %v", err)
	}
	ctrlLog, err := binlog.NewLogger(filepath.Join(logDir, "control.bin"), maxBytes, maxFiles)
	if err != nil {
		log.Fatalf("ctrlLog init: %v", err)
	}

	cfg := config.NewStore(config.RuntimeConfig{
		BindHost:          envStr("PI_PROXY_BIND", "0.0.0.0"),
		APIPort:           envInt("PI_API_PROXY_PORT", 18080),
		EgressPort:        envInt("PI_EGRESS_PROXY_PORT", 18888),
		StrataHost:        envStr("STRATA_HOST", "127.0.0.1"),
		StrataPort:        envInt("STRATA_PORT", 8080),
		StrataConcurrency: envInt("PI_STRATA_CONCURRENCY", 1),
		ProxyToken:        os.Getenv("PI_PROXY_TOKEN"),
		Workspace:         os.Getenv("PI_WORKSPACE"),
		LogDir:            logDir,
		MaxLogBytes:       maxBytes,
		MaxLogFiles:       maxFiles,
		AllowlistPath:     allowlist,
		AutoHealEnabled:   envStr("PI_AUTO_HEAL", "true") == "true",
		HealthIntervalSec: envInt("PI_HEALTH_INTERVAL_SEC", 10),
		MaxCommandWorkers: envInt("PI_MAX_CMD_WORKERS", 4),
		GHExe:             envStr("GH_EXE", "gh"),
		ESPProfile:        envStr("ESP_IDF_PROFILE", findDefaultESPProfile()),
		AWSEmuPort:        envInt("PI_AWS_EMU_PORT", 0),
		AWSEmuTargetPort:  envInt("PI_AWS_EMU_TARGET_PORT", 9297),
		AWSEmuCAPath:      os.Getenv("PI_AWS_EMU_CA"),
		AWSEmuContainer:   os.Getenv("PI_AWS_EMU_CONTAINER"),
		SaaSBaseURL:       os.Getenv("PI_SAAS_BASE_URL"),
		SaaSModel:         os.Getenv("PI_SAAS_MODEL"),
		SaaSAPIKey:        os.Getenv("PI_SAAS_API_KEY"),
	})

	c := cfg.Get()
	strataSem := make(chan struct{}, c.StrataConcurrency)
	runner := jobs.NewRunner(c.MaxCommandWorkers)
	monitor := health.NewMonitor(cfg, ctrlLog)
	arenaSvc := arena.NewService(cfg, ctrlLog, strataSem)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if c.AWSEmuPort > 0 {
		codeRoot := filepath.Join(filepath.Dir(logDir), ".aws-emulator", "lambda-code")
		bridge := aws.NewBridge(cfg, apiLog, codeRoot)
		addr := fmt.Sprintf("%s:%d", c.BindHost, c.AWSEmuPort)
		go func() {
			log.Printf("[pi-proxy] aws-emulator+lambda bridge listening on %s -> https://127.0.0.1:%d", addr, c.AWSEmuTargetPort)
			if err := http.ListenAndServe(addr, bridge); err != nil {
				log.Printf("aws bridge stopped: %v", err)
			}
		}()
		if c.AWSEmuContainer != "" {
			emuDir := filepath.Dir(filepath.Dir(c.AWSEmuCAPath))
			monitor.Register(health.Target{
				Name:          "aws-emulator",
				ContainerName: c.AWSEmuContainer,
				HealthURL:     fmt.Sprintf("https://127.0.0.1:%d/__admin/health", c.AWSEmuTargetPort),
				CAPath:        c.AWSEmuCAPath,
				RestartArgs: []string{
					"run", "-d", "--rm", "--name", c.AWSEmuContainer, "-m", "512M",
					"-p", fmt.Sprintf("127.0.0.1:%d:%d", c.AWSEmuTargetPort, c.AWSEmuTargetPort),
					"-v", fmt.Sprintf("%s:/home/emulator/.aws/emulator:rw", emuDir),
					"aws-emulator:latest", "-b", "0.0.0.0", "-p", strconv.Itoa(c.AWSEmuTargetPort), "-t", "off", "-a", "off",
				},
			})
		}
	}

	monitor.Start(ctx)

	apiHandler := proxy.NewAPIHandler(proxy.ServerDeps{
		Cfg: cfg, APILog: apiLog, EgressLog: egressLog, CtrlLog: ctrlLog,
		Runner: runner, Monitor: monitor, Arena: arenaSvc, StrataSem: strataSem,
	})
	egressHandler := proxy.NewEgressHandler(cfg, egressLog)

	go func() {
		addr := fmt.Sprintf("%s:%d", c.BindHost, c.APIPort)
		log.Printf("[pi-proxy] API/Control proxy listening on %s -> http://%s:%d (UI: /admin/ui)", addr, c.StrataHost, c.StrataPort)
		if err := http.ListenAndServe(addr, apiHandler); err != nil {
			log.Fatalf("api server: %v", err)
		}
	}()
	go func() {
		addr := fmt.Sprintf("%s:%d", c.BindHost, c.EgressPort)
		log.Printf("[pi-proxy] Allowlist egress proxy listening on %s", addr)
		if err := http.ListenAndServe(addr, egressHandler); err != nil {
			log.Fatalf("egress server: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("[pi-proxy] shutting down")
}

func envStr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func findDefaultESPProfile() string {
	sysDrive := envStr("SystemDrive", "C:")
	matches, err := filepath.Glob(filepath.Join(sysDrive+string(os.PathSeparator), "Espressif", "tools", "Microsoft.*.PowerShell_profile.ps1"))
	if err == nil && len(matches) > 0 {
		return matches[0]
	}
	return ""
}
