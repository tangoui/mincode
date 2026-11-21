package terminal

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"mincode/app/backend/core"
)

const (
	sshAdminTimeout        = 15 * time.Second
	sshAdminLogTimeout     = 30 * time.Second
	sshAdminMaxOutputBytes = 4 * 1024 * 1024
)

var (
	adminUnitPattern      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.@:-]{0,247}\.service$`)
	adminContainerPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
)

type sshAdminSnapshot struct {
	Hostname          string           `json:"hostname"`
	OperatingSystem   string           `json:"operatingSystem"`
	Kernel            string           `json:"kernel"`
	Architecture      string           `json:"architecture"`
	UptimeSeconds     int64            `json:"uptimeSeconds"`
	Load1             float64          `json:"load1"`
	Load5             float64          `json:"load5"`
	Load15            float64          `json:"load15"`
	MemoryTotalBytes  int64            `json:"memoryTotalBytes"`
	MemoryUsedBytes   int64            `json:"memoryUsedBytes"`
	DiskTotalBytes    int64            `json:"diskTotalBytes"`
	DiskUsedBytes     int64            `json:"diskUsedBytes"`
	Disks             []sshAdminDisk   `json:"disks"`
	DiskIO            []sshAdminDiskIO `json:"diskIO"`
	DiskIOTimestampMs int64            `json:"diskIOTimestampMs"`
	CpuCores          int64            `json:"cpuCores"`
	CpuTotal          int64            `json:"cpuTotal"`
	CpuIdle           int64            `json:"cpuIdle"`
	NetIO             []sshAdminNetIO  `json:"netIO"`
}

type sshAdminNetIO struct {
	Interface string `json:"interface"`
	RxBytes   int64  `json:"rxBytes"`
	TxBytes   int64  `json:"txBytes"`
}

type sshAdminDisk struct {
	Mount      string `json:"mount"`
	TotalBytes int64  `json:"totalBytes"`
	UsedBytes  int64  `json:"usedBytes"`
	AvailBytes int64  `json:"availBytes"`
}

type sshAdminDiskIO struct {
	Device     string `json:"device"`
	ReadBytes  int64  `json:"readBytes"`
	WriteBytes int64  `json:"writeBytes"`
}

type sshAdminProcess struct {
	PID     int     `json:"pid"`
	PPID    int     `json:"ppid"`
	User    string  `json:"user"`
	CPU     float64 `json:"cpu"`
	Memory  float64 `json:"memory"`
	State   string  `json:"state"`
	Command string  `json:"command"`
}

type sshAdminService struct {
	Unit        string `json:"unit"`
	Load        string `json:"load"`
	Active      string `json:"active"`
	Sub         string `json:"sub"`
	Description string `json:"description"`
}

type sshAdminDockerInfo struct {
	Available     bool   `json:"available"`
	ClientVersion string `json:"clientVersion,omitempty"`
	ServerVersion string `json:"serverVersion,omitempty"`
	Error         string `json:"error,omitempty"`
}

type sshAdminDockerContainer struct {
	ID      string `json:"id"`
	Names   string `json:"names"`
	Image   string `json:"image"`
	State   string `json:"state"`
	Status  string `json:"status"`
	Ports   string `json:"ports"`
	Created string `json:"created"`
}

func init() {
	core.Register("ssh_admin_snapshot", cmdSSHAdminSnapshot)
	core.Register("ssh_admin_logs", cmdSSHAdminLogs)
	core.Register("ssh_admin_processes", cmdSSHAdminProcesses)
	core.Register("ssh_admin_process_signal", cmdSSHAdminProcessSignal)
	core.Register("ssh_admin_systemd_list", cmdSSHAdminSystemdList)
	core.Register("ssh_admin_systemd_action", cmdSSHAdminSystemdAction)
	core.Register("ssh_admin_docker_info", cmdSSHAdminDockerInfo)
	core.Register("ssh_admin_docker_containers", cmdSSHAdminDockerContainers)
	core.Register("ssh_admin_docker_action", cmdSSHAdminDockerAction)
	core.Register("ssh_admin_docker_logs", cmdSSHAdminDockerLogs)
}

func sshAdminSessionID(args string) (string, error) {
	var payload struct {
		SessionID string `json:"sessionId"`
	}
	if err := core.DecodeArgs(args, &payload); err != nil {
		return "", err
	}
	if strings.TrimSpace(payload.SessionID) == "" {
		return "", fmt.Errorf("SSH 会话 ID 为空")
	}
	return payload.SessionID, nil
}

func sshAdminClient(sessionID string) (*ssh.Client, error) {
	process, ok := sessions.get(sessionID)
	if !ok {
		return nil, fmt.Errorf("SSH 会话不存在或已断开")
	}
	process.mu.Lock()
	defer process.mu.Unlock()
	if process.sshLinkState != sshLinkActive || process.sshClient == nil || process.sshClient.client == nil {
		return nil, fmt.Errorf("SSH 会话当前不可用")
	}
	return process.sshClient.client, nil
}

func runSSHAdminCommand(sessionID, command string, timeout time.Duration) (string, string, int, error) {
	client, err := sshAdminClient(sessionID)
	if err != nil {
		return "", "", -1, err
	}
	session, err := client.NewSession()
	if err != nil {
		return "", "", -1, fmt.Errorf("创建 SSH 管理命令会话失败: %w", err)
	}
	defer session.Close()
	stdout := core.NewCappedWriter(sshAdminMaxOutputBytes)
	stderr := core.NewCappedWriter(sshAdminMaxOutputBytes)
	session.Stdout = stdout
	session.Stderr = stderr
	done := make(chan error, 1)
	go func() { done <- session.Run(command) }()
	select {
	case err = <-done:
	case <-time.After(timeout):
		_ = session.Close()
		return "", "", -1, fmt.Errorf("SSH 管理命令执行超时(%s)", timeout)
	}
	outBytes, outTruncated := stdout.Bytes()
	errBytes, errTruncated := stderr.Bytes()
	outText := string(outBytes)
	errText := string(errBytes)
	if outTruncated {
		outText += "\n... (output truncated)"
	}
	if errTruncated {
		errText += "\n... (output truncated)"
	}
	if err == nil {
		return outText, errText, 0, nil
	}
	if exit, ok := err.(*ssh.ExitError); ok {
		return outText, errText, exit.ExitStatus(), nil
	}
	return outText, errText, -1, fmt.Errorf("SSH 管理命令失败: %w", err)
}

func requireLinux(sessionID string) error {
	stdout, stderr, code, err := runSSHAdminCommand(sessionID, "uname -s", sshAdminTimeout)
	if err != nil {
		return err
	}
	if code != 0 || strings.TrimSpace(stdout) != "Linux" {
		detail := strings.TrimSpace(stderr)
		if detail == "" {
			detail = strings.TrimSpace(stdout)
		}
		return fmt.Errorf("服务器管理当前仅支持 Linux: %s", detail)
	}
	return nil
}

func cmdSSHAdminSnapshot(_, args string) (string, error) {
	sessionID, err := sshAdminSessionID(args)
	if err != nil {
		return "", err
	}
	if err := requireLinux(sessionID); err != nil {
		return "", err
	}
	command := `LC_ALL=C; printf 'hostname=%s\n' "$(hostname 2>/dev/null)"; printf 'os=%s\n' "$(. /etc/os-release 2>/dev/null; printf '%s' "${PRETTY_NAME:-Linux}")"; printf 'kernel=%s\n' "$(uname -r)"; printf 'arch=%s\n' "$(uname -m)"; awk '{printf "uptime=%d\n", $1}' /proc/uptime; awk '{printf "load1=%s\nload5=%s\nload15=%s\n", $1,$2,$3}' /proc/loadavg; awk '/MemTotal:/{t=$2*1024}/MemAvailable:/{a=$2*1024}END{printf "mem_total=%.0f\nmem_used=%.0f\n",t,t-a}' /proc/meminfo; df -Pk / | awk 'NR==2{printf "disk_total=%.0f\ndisk_used=%.0f\n",$2*1024,$3*1024}'; df -Pk 2>/dev/null | awk 'NR>1&&$1~/^\//&&$6~/^\/{i++;printf "dm%d=%s|%d|%d|%d\n",i,$6,$2*1024,$3*1024,$4*1024}'; awk 'NF>=11{dev=$3;if(dev~/^(sd[a-z]$|vd[a-z]$|hd[a-z]$|nvme[0-9]+n[0-9]+$|xvd[a-z]$|mmcblk[0-9]+$)/){i++;printf "dkio%d=%s|%d|%d\n",i,dev,$6*512,$10*512}}' /proc/diskstats 2>/dev/null; awk '{printf "dkiots=%d\n",int($1*1000)}' /proc/uptime; printf 'cpu_cores=%s\n' "$(nproc 2>/dev/null || awk '/^processor/{n++}END{print n+0}' /proc/cpuinfo)"; awk 'NR==1{printf "cpu_total=%d\ncpu_idle=%d\n",$2+$3+$4+$5+$6+$7+$8+$9,$5+$6}' /proc/stat 2>/dev/null; awk 'NR>2{split($0,a,":");iface=a[1];gsub(/[ \t]/,"",iface);if(iface!=""&&iface!="lo"){n=split(a[2],b);if(n>=9){i++;printf "net%d=%s|%s|%s\n",i,iface,b[1],b[9]}}}' /proc/net/dev 2>/dev/null`
	stdout, stderr, code, err := runSSHAdminCommand(sessionID, command, sshAdminTimeout)
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", fmt.Errorf("读取服务器状态失败: %s", strings.TrimSpace(stderr))
	}
	return core.EncodeResult(parseSSHAdminSnapshot(stdout))
}

func parseSSHAdminSnapshot(output string) sshAdminSnapshot {
	values := map[string]string{}
	for _, line := range strings.Split(output, "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok {
			values[key] = value
		}
	}
	parseInt := func(key string) int64 { value, _ := strconv.ParseInt(values[key], 10, 64); return value }
	parseFloat := func(key string) float64 { value, _ := strconv.ParseFloat(values[key], 64); return value }

	// Parse per-mount disk entries (dm1=mount|total|used|avail, dm2=...).
	disks := []sshAdminDisk{}
	for i := 1; i <= 64; i++ {
		raw, ok := values[fmt.Sprintf("dm%d", i)]
		if !ok {
			break
		}
		parts := strings.SplitN(raw, "|", 4)
		if len(parts) == 4 {
			total, _ := strconv.ParseInt(parts[1], 10, 64)
			used, _ := strconv.ParseInt(parts[2], 10, 64)
			avail, _ := strconv.ParseInt(parts[3], 10, 64)
			disks = append(disks, sshAdminDisk{Mount: parts[0], TotalBytes: total, UsedBytes: used, AvailBytes: avail})
		}
	}

	// Parse disk I/O counters (dkio1=device|readBytes|writeBytes, ...).
	diskIO := []sshAdminDiskIO{}
	for i := 1; i <= 32; i++ {
		raw, ok := values[fmt.Sprintf("dkio%d", i)]
		if !ok {
			break
		}
		parts := strings.SplitN(raw, "|", 3)
		if len(parts) == 3 {
			rb, _ := strconv.ParseInt(parts[1], 10, 64)
			wb, _ := strconv.ParseInt(parts[2], 10, 64)
			diskIO = append(diskIO, sshAdminDiskIO{Device: parts[0], ReadBytes: rb, WriteBytes: wb})
		}
	}

	// Parse network I/O counters (net1=iface|rxBytes|txBytes, ...).
	netIO := []sshAdminNetIO{}
	for i := 1; i <= 32; i++ {
		raw, ok := values[fmt.Sprintf("net%d", i)]
		if !ok {
			break
		}
		parts := strings.SplitN(raw, "|", 3)
		if len(parts) == 3 {
			rx, _ := strconv.ParseInt(parts[1], 10, 64)
			tx, _ := strconv.ParseInt(parts[2], 10, 64)
			netIO = append(netIO, sshAdminNetIO{Interface: parts[0], RxBytes: rx, TxBytes: tx})
		}
	}

	return sshAdminSnapshot{
		Hostname: values["hostname"], OperatingSystem: values["os"], Kernel: values["kernel"], Architecture: values["arch"],
		UptimeSeconds: parseInt("uptime"), Load1: parseFloat("load1"), Load5: parseFloat("load5"), Load15: parseFloat("load15"),
		MemoryTotalBytes: parseInt("mem_total"), MemoryUsedBytes: parseInt("mem_used"),
		DiskTotalBytes: parseInt("disk_total"), DiskUsedBytes: parseInt("disk_used"),
		Disks: disks, DiskIO: diskIO, DiskIOTimestampMs: parseInt("dkiots"),
		CpuCores: parseInt("cpu_cores"), CpuTotal: parseInt("cpu_total"), CpuIdle: parseInt("cpu_idle"),
		NetIO: netIO,
	}
}

func cmdSSHAdminLogs(_, args string) (string, error) {
	var payload struct {
		SessionID string `json:"sessionId"`
		Unit      string `json:"unit"`
		Lines     int    `json:"lines"`
	}
	if err := core.DecodeArgs(args, &payload); err != nil {
		return "", err
	}
	if err := requireLinux(payload.SessionID); err != nil {
		return "", err
	}
	if payload.Lines <= 0 {
		payload.Lines = 200
	}
	if payload.Lines > 2000 {
		payload.Lines = 2000
	}
	command := fmt.Sprintf("journalctl --no-pager -o short-iso -n %d", payload.Lines)
	if strings.TrimSpace(payload.Unit) != "" {
		if !adminUnitPattern.MatchString(payload.Unit) {
			return "", fmt.Errorf("systemd 服务名称无效")
		}
		command += " -u " + quotePOSIXShellWord(payload.Unit)
	}
	stdout, stderr, code, err := runSSHAdminCommand(payload.SessionID, command, sshAdminLogTimeout)
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", fmt.Errorf("读取日志失败: %s", strings.TrimSpace(stderr))
	}
	return core.EncodeResult(stdout)
}

func cmdSSHAdminProcesses(_, args string) (string, error) {
	var payload struct {
		SessionID string `json:"sessionId"`
		Limit     int    `json:"limit"`
	}
	if err := core.DecodeArgs(args, &payload); err != nil {
		return "", err
	}
	if err := requireLinux(payload.SessionID); err != nil {
		return "", err
	}
	if payload.Limit <= 0 {
		payload.Limit = 100
	}
	if payload.Limit > 500 {
		payload.Limit = 500
	}
	command := fmt.Sprintf("LC_ALL=C ps -eo pid=,ppid=,user=,%%cpu=,%%mem=,stat=,comm= --sort=-%%cpu | head -n %d", payload.Limit)
	stdout, stderr, code, err := runSSHAdminCommand(payload.SessionID, command, sshAdminTimeout)
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", fmt.Errorf("读取进程失败: %s", strings.TrimSpace(stderr))
	}
	return core.EncodeResult(parseSSHAdminProcesses(stdout))
}

func parseSSHAdminProcesses(output string) []sshAdminProcess {
	result := make([]sshAdminProcess, 0)
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 7 {
			continue
		}
		pid, pidErr := strconv.Atoi(fields[0])
		ppid, ppidErr := strconv.Atoi(fields[1])
		cpu, cpuErr := strconv.ParseFloat(fields[3], 64)
		memory, memoryErr := strconv.ParseFloat(fields[4], 64)
		if pidErr != nil || ppidErr != nil || cpuErr != nil || memoryErr != nil {
			continue
		}
		result = append(result, sshAdminProcess{PID: pid, PPID: ppid, User: fields[2], CPU: cpu, Memory: memory, State: fields[5], Command: strings.Join(fields[6:], " ")})
	}
	return result
}

func cmdSSHAdminProcessSignal(_, args string) (string, error) {
	var payload struct {
		SessionID string `json:"sessionId"`
		PID       int    `json:"pid"`
		Signal    string `json:"signal"`
	}
	if err := core.DecodeArgs(args, &payload); err != nil {
		return "", err
	}
	if payload.PID <= 1 {
		return "", fmt.Errorf("拒绝操作系统关键进程")
	}
	allowed := map[string]bool{"TERM": true, "KILL": true, "HUP": true, "INT": true}
	payload.Signal = strings.ToUpper(payload.Signal)
	if !allowed[payload.Signal] {
		return "", fmt.Errorf("进程信号不受支持")
	}
	_, stderr, code, err := runSSHAdminCommand(payload.SessionID, fmt.Sprintf("kill -%s -- %d", payload.Signal, payload.PID), sshAdminTimeout)
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", fmt.Errorf("发送进程信号失败: %s", strings.TrimSpace(stderr))
	}
	return "", nil
}

func cmdSSHAdminSystemdList(_, args string) (string, error) {
	sessionID, err := sshAdminSessionID(args)
	if err != nil {
		return "", err
	}
	if err := requireLinux(sessionID); err != nil {
		return "", err
	}
	stdout, stderr, code, err := runSSHAdminCommand(sessionID, "LC_ALL=C systemctl list-units --type=service --all --no-legend --no-pager --plain", sshAdminTimeout)
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", fmt.Errorf("读取 systemd 服务失败: %s", strings.TrimSpace(stderr))
	}
	return core.EncodeResult(parseSSHAdminServices(stdout))
}

func parseSSHAdminServices(output string) []sshAdminService {
	result := make([]sshAdminService, 0)
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && (fields[0] == "●" || fields[0] == "*") {
			fields = fields[1:]
		}
		if len(fields) < 5 || !adminUnitPattern.MatchString(fields[0]) {
			continue
		}
		result = append(result, sshAdminService{Unit: fields[0], Load: fields[1], Active: fields[2], Sub: fields[3], Description: strings.Join(fields[4:], " ")})
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].Unit < result[j].Unit })
	return result
}

func cmdSSHAdminSystemdAction(_, args string) (string, error) {
	var payload struct {
		SessionID string `json:"sessionId"`
		Unit      string `json:"unit"`
		Action    string `json:"action"`
	}
	if err := core.DecodeArgs(args, &payload); err != nil {
		return "", err
	}
	if !adminUnitPattern.MatchString(payload.Unit) {
		return "", fmt.Errorf("systemd 服务名称无效")
	}
	allowed := map[string]bool{"start": true, "stop": true, "restart": true, "reload": true}
	if !allowed[payload.Action] {
		return "", fmt.Errorf("systemd 操作不受支持")
	}
	if err := requireLinux(payload.SessionID); err != nil {
		return "", err
	}
	command := "systemctl " + payload.Action + " " + quotePOSIXShellWord(payload.Unit)
	_, stderr, code, err := runSSHAdminCommand(payload.SessionID, command, sshAdminLogTimeout)
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", fmt.Errorf("systemd 操作失败: %s", strings.TrimSpace(stderr))
	}
	return "", nil
}

func cmdSSHAdminDockerInfo(_, args string) (string, error) {
	sessionID, err := sshAdminSessionID(args)
	if err != nil {
		return "", err
	}
	if err := requireLinux(sessionID); err != nil {
		return "", err
	}
	stdout, stderr, code, err := runSSHAdminCommand(sessionID, `docker version --format '{{json .}}'`, sshAdminTimeout)
	if err != nil {
		return "", err
	}
	if code != 0 {
		return core.EncodeResult(sshAdminDockerInfo{Available: false, Error: strings.TrimSpace(stderr)})
	}
	var raw struct {
		Client struct {
			Version string `json:"Version"`
		} `json:"Client"`
		Server struct {
			Version string `json:"Version"`
		} `json:"Server"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &raw); err != nil {
		return "", fmt.Errorf("解析 Docker 版本失败: %w", err)
	}
	return core.EncodeResult(sshAdminDockerInfo{Available: true, ClientVersion: raw.Client.Version, ServerVersion: raw.Server.Version})
}

func cmdSSHAdminDockerContainers(_, args string) (string, error) {
	sessionID, err := sshAdminSessionID(args)
	if err != nil {
		return "", err
	}
	if err := requireLinux(sessionID); err != nil {
		return "", err
	}
	stdout, stderr, code, err := runSSHAdminCommand(sessionID, `docker ps -a --no-trunc --format '{{json .}}'`, sshAdminTimeout)
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", fmt.Errorf("读取 Docker 容器失败: %s", strings.TrimSpace(stderr))
	}
	containers := make([]sshAdminDockerContainer, 0)
	for _, line := range strings.Split(stdout, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var raw map[string]string
		if json.Unmarshal([]byte(line), &raw) != nil {
			continue
		}
		containers = append(containers, sshAdminDockerContainer{ID: raw["ID"], Names: raw["Names"], Image: raw["Image"], State: raw["State"], Status: raw["Status"], Ports: raw["Ports"], Created: raw["CreatedAt"]})
	}
	return core.EncodeResult(containers)
}

func cmdSSHAdminDockerAction(_, args string) (string, error) {
	var payload struct {
		SessionID   string `json:"sessionId"`
		ContainerID string `json:"containerId"`
		Action      string `json:"action"`
	}
	if err := core.DecodeArgs(args, &payload); err != nil {
		return "", err
	}
	if !adminContainerPattern.MatchString(payload.ContainerID) {
		return "", fmt.Errorf("Docker 容器 ID 无效")
	}
	allowed := map[string]bool{"start": true, "stop": true, "restart": true, "pause": true, "unpause": true, "remove": true}
	if !allowed[payload.Action] {
		return "", fmt.Errorf("Docker 操作不受支持")
	}
	if err := requireLinux(payload.SessionID); err != nil {
		return "", err
	}
	verb := payload.Action
	if verb == "remove" {
		verb = "rm"
	}
	_, stderr, code, err := runSSHAdminCommand(payload.SessionID, "docker "+verb+" "+quotePOSIXShellWord(payload.ContainerID), sshAdminLogTimeout)
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", fmt.Errorf("Docker 操作失败: %s", strings.TrimSpace(stderr))
	}
	return "", nil
}

func cmdSSHAdminDockerLogs(_, args string) (string, error) {
	var payload struct {
		SessionID   string `json:"sessionId"`
		ContainerID string `json:"containerId"`
		Lines       int    `json:"lines"`
	}
	if err := core.DecodeArgs(args, &payload); err != nil {
		return "", err
	}
	if !adminContainerPattern.MatchString(payload.ContainerID) {
		return "", fmt.Errorf("Docker 容器 ID 无效")
	}
	if err := requireLinux(payload.SessionID); err != nil {
		return "", err
	}
	if payload.Lines <= 0 {
		payload.Lines = 200
	}
	if payload.Lines > 2000 {
		payload.Lines = 2000
	}
	command := fmt.Sprintf("docker logs --tail %d %s 2>&1", payload.Lines, quotePOSIXShellWord(payload.ContainerID))
	stdout, stderr, code, err := runSSHAdminCommand(payload.SessionID, command, sshAdminLogTimeout)
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", fmt.Errorf("读取 Docker 日志失败: %s", strings.TrimSpace(stderr))
	}
	return core.EncodeResult(stdout)
}
