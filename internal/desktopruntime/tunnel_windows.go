//go:build windows

package desktopruntime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

func platformLaunchTunnel(context.Context, string) error {
	return errors.New("Windows Tunnel 使用后台进程启动，不支持 launch 内部入口")
}

var quickTunnelURLPattern = regexp.MustCompile(`https://[A-Za-z0-9-]+\.trycloudflare\.com`)

func platformTunnelStatus(ctx context.Context, runtimeRoot string) (TunnelStatus, error) {
	runtime, err := loadTunnelRuntime(runtimeRoot)
	if err != nil {
		return TunnelStatus{}, err
	}
	running, err := processRunningAtPath(runtime.manifest.CloudflaredBinary)
	if err != nil {
		return TunnelStatus{}, err
	}
	startupEnabled, err := tunnelAutostartEnabled(runtime.manifest)
	if err != nil {
		return TunnelStatus{}, fmt.Errorf("读取 Tunnel 开机启动状态失败: %w", err)
	}
	publicURL, err := readTunnelPublicURL(runtime)
	if err != nil {
		return TunnelStatus{}, err
	}
	ready := runtime.mode == "none"
	if runtime.mode == "quick" {
		ready = running && publicURL != ""
	}
	if runtime.mode == "named" {
		ready = running && publicURL != ""
	}
	return TunnelStatus{
		Mode:           runtime.mode,
		Running:        running,
		Ready:          ready,
		StartupEnabled: startupEnabled,
		PublicURL:      publicURL,
	}, nil
}

func platformTunnelAction(ctx context.Context, runtimeRoot, action string) error {
	runtime, err := loadTunnelRuntime(runtimeRoot)
	if err != nil {
		return err
	}
	switch action {
	case "start":
		return startTunnel(ctx, runtime)
	case "stop":
		return stopTunnel(ctx, runtime)
	case "restart":
		if runtime.mode == "quick" {
			return regenerateQuickTunnel(ctx, runtime)
		}
		if err := stopTunnel(ctx, runtime); err != nil {
			return err
		}
		return startTunnel(ctx, runtime)
	case "regenerate":
		if runtime.mode != "quick" {
			return errors.New("只有临时地址模式可以重新生成 Quick Tunnel")
		}
		return regenerateQuickTunnel(ctx, runtime)
	default:
		return fmt.Errorf("不支持的 Tunnel 操作：%s", action)
	}
}

func startTunnel(ctx context.Context, runtime tunnelRuntime) error {
	if runtime.mode == "none" {
		return nil
	}
	if info, err := os.Stat(runtime.manifest.CloudflaredBinary); err != nil || info.IsDir() {
		return fmt.Errorf("找不到 cloudflared.exe，请运行 Setup.exe 修复安装: %s", runtime.manifest.CloudflaredBinary)
	}

	running, err := processRunningAtPath(runtime.manifest.CloudflaredBinary)
	if err != nil {
		return err
	}
	if running {
		if runtime.mode == "quick" {
			readyURL, readyErr := readTrimmedText(runtime.files.quickURL)
			if readyErr != nil {
				return readyErr
			}
			if readyURL == "" {
				return finalizeQuickTunnel(ctx, runtime)
			}
		}
		return nil
	}

	if runtime.mode == "quick" {
		// 旧临时地址在新进程真正拿到 URL 前不能继续暴露为 ready；
		// 只摘除 ready 标记，server-url.txt 中的旧 OAuth Origin 保留到新地址原子写入。
		if err := clearQuickReadyMarker(runtime.files); err != nil {
			return err
		}
		if err := runtime.updateManifest("none", ""); err != nil {
			return err
		}
	}
	if err := launchCloudflared(runtime); err != nil {
		return err
	}
	if err := waitCloudflaredRunning(ctx, runtime.manifest.CloudflaredBinary, 20*time.Second); err != nil {
		return err
	}
	if runtime.mode == "quick" {
		return finalizeQuickTunnel(ctx, runtime)
	}
	return nil
}

func stopTunnel(ctx context.Context, runtime tunnelRuntime) error {
	if err := StopBinaryProcesses(ctx, runtime.manifest.CloudflaredBinary, 15*time.Second); err != nil {
		return fmt.Errorf("停止 cloudflared 失败: %w", err)
	}
	return nil
}

func regenerateQuickTunnel(ctx context.Context, runtime tunnelRuntime) error {
	if err := stopTunnel(ctx, runtime); err != nil {
		return err
	}
	if err := clearQuickReadyMarker(runtime.files); err != nil {
		return err
	}
	if err := runtime.updateManifest("none", ""); err != nil {
		return err
	}
	// 新地址就绪前核心继续以 server-url.txt 中的旧 OAuth Origin 运行；
	// finalizeQuickTunnel 拿到新地址后会原子覆盖并再次重启核心，全程不再出现无 OAuth 窗口。
	if err := platformServiceAction(ctx, runtime.root, "restart"); err != nil {
		return err
	}
	return startTunnel(ctx, runtime)
}

func launchCloudflared(runtime tunnelRuntime) error {
	arguments := []string{"tunnel", "--no-autoupdate"}
	environment := environmentWithout(os.Environ(), "TUNNEL_TOKEN")
	if runtime.mode == "quick" {
		arguments = append(arguments, "--url", fmt.Sprintf("http://127.0.0.1:%d", runtime.settings.Port))
	} else {
		token, err := readProtectedText(runtime.files.token, tunnelTokenEntropy)
		if err != nil {
			return fmt.Errorf("读取 Cloudflare Tunnel Token 失败: %w", err)
		}
		if strings.TrimSpace(token) == "" {
			return errors.New("固定域名模式没有保存 Cloudflare Tunnel Token")
		}
		environment = append(environment, "TUNNEL_TOKEN="+token)
		arguments = append(arguments, "run")
	}

	stdout, err := os.OpenFile(runtime.files.stdoutLog, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("打开 cloudflared 输出日志失败: %w", err)
	}
	stderr, err := os.OpenFile(runtime.files.stderrLog, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		_ = stdout.Close()
		return fmt.Errorf("打开 cloudflared 错误日志失败: %w", err)
	}
	command := exec.Command(runtime.manifest.CloudflaredBinary, arguments...)
	command.Env = environment
	command.Dir = runtime.root
	command.Stdout = stdout
	command.Stderr = stderr
	command.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS,
	}
	startErr := command.Start()
	_ = stdout.Close()
	_ = stderr.Close()
	if startErr != nil {
		return fmt.Errorf("启动 cloudflared 失败: %w", startErr)
	}
	if err := command.Process.Release(); err != nil {
		return fmt.Errorf("释放 cloudflared 后台进程句柄失败: %w", err)
	}
	return nil
}

func finalizeQuickTunnel(ctx context.Context, runtime tunnelRuntime) error {
	publicURL, err := waitQuickTunnelURL(ctx, runtime, 35*time.Second)
	if err != nil {
		_ = StopBinaryProcesses(context.Background(), runtime.manifest.CloudflaredBinary, 5*time.Second)
		return err
	}
	if err := writeRuntimeText(runtime.files.serverURL, publicURL); err != nil {
		return err
	}
	if err := platformServiceAction(ctx, runtime.root, "restart"); err != nil {
		return err
	}
	if err := runtime.updateManifest("quick", publicURL); err != nil {
		return err
	}
	// ready 文件最后写入，保证桌面端读到地址时核心已经采用新 OAuth Origin。
	return writeRuntimeText(runtime.files.quickURL, publicURL)
}

func waitQuickTunnelURL(ctx context.Context, runtime tunnelRuntime, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, path := range []string{runtime.files.stdoutLog, runtime.files.stderrLog} {
			data, err := os.ReadFile(path)
			if err == nil {
				if match := quickTunnelURLPattern.Find(data); len(match) > 0 {
					return string(match), nil
				}
			}
		}
		running, err := processRunningAtPath(runtime.manifest.CloudflaredBinary)
		if err != nil {
			return "", err
		}
		if !running {
			return "", fmt.Errorf("cloudflared 在生成临时地址前退出: %s", tunnelLogSummary(runtime.files))
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return "", fmt.Errorf("cloudflared 未在 %s 内生成 trycloudflare.com 临时地址: %s", timeout, tunnelLogSummary(runtime.files))
}

func waitCloudflaredRunning(ctx context.Context, binaryPath string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		running, err := processRunningAtPath(binaryPath)
		if err != nil {
			return err
		}
		if running {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	return fmt.Errorf("cloudflared 未保持运行: %s", binaryPath)
}

func readTunnelPublicURL(runtime tunnelRuntime) (string, error) {
	if runtime.mode == "none" {
		return "", nil
	}
	if runtime.mode == "quick" {
		return readTrimmedText(runtime.files.quickURL)
	}
	return readTrimmedText(runtime.files.serverURL)
}

func environmentWithout(environment []string, name string) []string {
	prefix := strings.ToUpper(name) + "="
	filtered := make([]string, 0, len(environment))
	for _, entry := range environment {
		if strings.HasPrefix(strings.ToUpper(entry), prefix) {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

func tunnelLogSummary(files tunnelFiles) string {
	for _, path := range []string{files.stderrLog, files.stdoutLog} {
		data, err := os.ReadFile(path)
		if err != nil || len(data) == 0 {
			continue
		}
		if len(data) > 2048 {
			data = data[len(data)-2048:]
		}
		return strings.TrimSpace(string(data))
	}
	return "日志为空"
}
