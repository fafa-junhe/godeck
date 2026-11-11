#!/bin/sh
# ==============================================================================
# GoDeck Dedicated Maintenance & Certificate Renewal Daemon (Artix Linux / OpenRC)
# Domain: godeck.junher.top
#
# 安全与防护原则：
# 1. 严格禁止 set -x，防止任何敏感参数泄漏。
# 2. 维护配置与状态路径严格归属于 root:
#    - 配置路径：/etc/godeck-maintenance/logrotate.conf (root-owned 0750 目录)
#    - 状态路径：/var/lib/godeck-maintenance/logrotate.state (root-owned 0700 目录)
#    - 日志路径：/var/log/godeck-cert-renew/renew.log (root-owned 0750 目录)
#    杜绝普通服务用户通过父目录写权限实施软链接替换或提权。
# 3. 严格拒绝符号链接：若遇软链接立即拦截清理。
# 4. 临时文件在 root 私有目录内通过 mktemp 安全创建。
# 5. 动态检测 logrotate 二进制文件 (command -v logrotate)，不硬编码 /usr/sbin。
# 6. logrotate 每 60 秒轮询调用一次专属配置以检查文件大小并按 maxsize 轮转。
# 7. acme.sh 证书续签每 24 小时检查一次；退出码 2 为标准跳过（未到期，正常现象）。
# ==============================================================================

LOG_DIR="/var/log/godeck-cert-renew"
LOG_FILE="${LOG_DIR}/renew.log"
MAINT_CONF_DIR="/etc/godeck-maintenance"
MAINT_STATE_DIR="/var/lib/godeck-maintenance"
LOGROTATE_CONF="${MAINT_CONF_DIR}/logrotate.conf"
LOGROTATE_STATE="${MAINT_STATE_DIR}/logrotate.state"
DOMAIN="godeck.junher.top"
ACME_BIN="/root/.acme.sh/acme.sh"

# 1. 严格防范目录与配置软链接
for check_dir in "$LOG_DIR" "$MAINT_CONF_DIR" "$MAINT_STATE_DIR"; do
    if [ -L "$check_dir" ]; then
        echo "安全拦截: ${check_dir} 为符号链接，禁止运行" >&2
        exit 1
    fi
done

# 2. 安全初始化 root 独占的目录与权限
mkdir -p "$LOG_DIR" && chmod 0750 "$LOG_DIR" && chown root:root "$LOG_DIR"
mkdir -p "$MAINT_CONF_DIR" && chmod 0750 "$MAINT_CONF_DIR" && chown root:root "$MAINT_CONF_DIR"
mkdir -p "$MAINT_STATE_DIR" && chmod 0700 "$MAINT_STATE_DIR" && chown root:root "$MAINT_STATE_DIR"

if [ -L "$LOG_FILE" ]; then
    echo "安全拦截: ${LOG_FILE} 为符号链接，正在移除..." >&2
    rm -f "$LOG_FILE"
fi
touch "$LOG_FILE"
chmod 0600 "$LOG_FILE"
chown root:root "$LOG_FILE"

log_msg() {
    if [ -L "$LOG_FILE" ]; then
        return 1
    fi
    echo "[$(date '+%Y-%m-%d %H:%M:%S')] $*" >> "$LOG_FILE"
    
    # 保持日志文件在 512KB 以内
    file_size=$(wc -c < "$LOG_FILE" 2>/dev/null || echo 0)
    if [ "$file_size" -gt 524288 ]; then
        tmp_log=$(mktemp -p "$LOG_DIR" .renew-tmp.XXXXXX)
        chmod 0600 "$tmp_log"
        tail -n 500 "$LOG_FILE" > "$tmp_log" 2>/dev/null && mv "$tmp_log" "$LOG_FILE"
    fi
}

# 定时执行 logrotate（由循环周期每 60 秒触发一次）
run_logrotate() {
    rotate_bin="$(command -v logrotate 2>/dev/null || true)"
    if [ -n "$rotate_bin" ] && [ -x "$rotate_bin" ]; then
        if [ ! -L "$LOGROTATE_CONF" ] && [ -f "$LOGROTATE_CONF" ]; then
            if [ -L "$LOGROTATE_STATE" ]; then
                rm -f "$LOGROTATE_STATE"
            fi
            "$rotate_bin" -s "$LOGROTATE_STATE" "$LOGROTATE_CONF" >/dev/null 2>&1 || true
        fi
    fi
}

run_cert_renew() {
    log_msg "Executing scheduled renewal check for ${DOMAIN}..."
    if [ -x "$ACME_BIN" ]; then
        renew_output=$("$ACME_BIN" --renew -d "$DOMAIN" --ecc --server letsencrypt 2>&1)
        exit_code=$?
        
        summary=$(echo "$renew_output" | grep -Ei "Skip|Success|Renew|reload" | head -n 3 | tr '\n' '; ')
        case $exit_code in
            0)
                log_msg "Renewal successful (exit 0): ${summary:-Cert renewed and reloaded}"
                ;;
            2)
                # acme.sh 退出码 2 为标准正常跳过（未到期）
                log_msg "Cert check passed (exit 2, not due for renewal): ${summary:-Skipped as cert is valid}"
                ;;
            *)
                log_msg "Renewal check reported code ${exit_code}: $(echo "$renew_output" | tail -n 2 | tr '\n' '; ')"
                ;;
        esac
    else
        log_msg "Warning: acme.sh not found or not executable at ${ACME_BIN}"
    fi
}

log_msg "Starting GoDeck maintenance daemon (cert-renew & logrotate) for domain: ${DOMAIN}"

# 响应 SIGTERM/SIGINT 优雅退出
trap 'log_msg "GoDeck maintenance daemon stopped"; exit 0' TERM INT HUP

while true; do
    # 每天执行一次证书续签检测
    run_cert_renew
    # 立即执行一次日志轮转检查
    run_logrotate

    # 内层循环每 60 秒休眠一次，并在每个 60 秒周期检查并触发 logrotate maxsize 轮转
    elapsed=0
    while [ $elapsed -lt 86400 ]; do
        sleep 60 &
        wait $!
        elapsed=$((elapsed + 60))
        run_logrotate
    done
done
