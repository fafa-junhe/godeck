#!/bin/sh
# ==============================================================================
# GoDeck Hub & Coturn Safe Remote Deployment Script (Artix Linux / OpenRC)
# Target Server: YOUR_SERVER_IP
# Domain: godeck.junher.top
#
# 安全原则：
# 1. 严格预检：端口冲突、关键二进制缺失、既有未授权文件存在时立即以非零状态码退出。
# 2. 绝不盲目覆盖既有配置；升级必须显式指定 --force。
# 3. 取消对 /etc/nginx/nginx.conf 的自动化正则编辑，采用明确的手动一行 include 方案。
# 4. 绝不在证书尚未安装时执行不可能通过的 nginx -t 校验。
# 5. 严格隔离：Hub (godeck 用户)、TURN (turnserver 用户)、续期 (root 专属日志目录)。
# ==============================================================================

set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
FORCE_INSTALL=0

for arg in "$@"; do
    if [ "$arg" = "--force" ]; then
        FORCE_INSTALL=1
    fi
done

echo "=== 1. 执行严格安装前环境与冲突预检 ==="

# 1.1 权限预检
if [ "$(id -u)" -ne 0 ]; then
    echo "错误: 必须以 root 权限运行此安装脚本" >&2
    exit 1
fi

# 1.2 OpenRC 环境预检
if ! command -v openrc >/dev/null 2>&1 && [ ! -d /etc/init.d ]; then
    echo "错误: 当前系统未检测到 OpenRC 环境 (/etc/init.d 不存在)" >&2
    exit 1
fi

# 1.3 必要二进制文件预检
HUB_SRC_BIN=""
if [ -f "$SCRIPT_DIR/../dist/hub-linux-amd64/godeck-hub" ] && [ -s "$SCRIPT_DIR/../dist/hub-linux-amd64/godeck-hub" ]; then
    HUB_SRC_BIN="$SCRIPT_DIR/../dist/hub-linux-amd64/godeck-hub"
elif [ -f "$SCRIPT_DIR/godeck-hub" ] && [ -s "$SCRIPT_DIR/godeck-hub" ]; then
    HUB_SRC_BIN="$SCRIPT_DIR/godeck-hub"
else
    echo "错误: 未找到有效的 godeck-hub 二进制文件 (期望路径: $SCRIPT_DIR/../dist/hub-linux-amd64/godeck-hub)" >&2
    exit 1
fi

if [ ! -x "/usr/bin/turnserver" ]; then
    echo "错误: 未检测到 /usr/bin/turnserver，请先安装 coturn 软件包 (pacman -S coturn)" >&2
    exit 1
fi

# 1.4 端口占用预检 (8787 TCP, 3478 UDP/TCP)
if command -v ss >/dev/null 2>&1; then
    # 检查 8787 本地端口
    if ss -tlnp | grep -q ":8787 "; then
        port_8787_proc=$(ss -tlnp | grep ":8787 " | head -n 1)
        if ! echo "$port_8787_proc" | grep -q "godeck-hub"; then
            echo "错误: 本地 8787 端口已被非 godeck-hub 进程占用: $port_8787_proc" >&2
            exit 1
        fi
    fi

    # 检查 3478 UDP/TCP
    if ss -tlunp | grep -E -q ":3478 "; then
        port_3478_proc=$(ss -tlunp | grep -E ":3478 " | head -n 1)
        if ! echo "$port_3478_proc" | grep -q "turnserver"; then
            echo "错误: 3478 端口已被非 turnserver 进程占用: $port_3478_proc" >&2
            exit 1
        fi
    fi
fi

# 1.5 既有配置文件覆盖检查
EXISTING_CONFLICTS=""
if [ $FORCE_INSTALL -eq 0 ]; then
    for check_file in "/etc/init.d/godeck-hub" "/etc/init.d/godeck-turn" "/etc/godeck-turn/turnserver.conf" "/etc/nginx/conf.d/godeck.conf" "/etc/godeck-maintenance/logrotate.conf"; do
        if [ -f "$check_file" ]; then
            EXISTING_CONFLICTS="$EXISTING_CONFLICTS $check_file"
        fi
    done
    if [ -n "$EXISTING_CONFLICTS" ]; then
        echo "安全拦截: 检测到以下服务或配置文件已存在:$EXISTING_CONFLICTS" >&2
        echo "为防止意外覆盖，脚本已停止。若确认升级覆盖，请携带 --force 参数重新执行: $0 --force" >&2
        exit 1
    fi
fi

echo "预检全部通过。"

echo "=== 2. 创建 GoDeck 与 Turnserver 专属隔离用户与目录 ==="
# 创建 godeck 用户
if ! id -u godeck >/dev/null 2>&1; then
    echo "创建系统服务用户: godeck"
    useradd -r -s /sbin/nologin -d /var/lib/godeck-hub godeck 2>/dev/null || adduser -S -s /sbin/nologin -h /var/lib/godeck-hub godeck 2>/dev/null
fi

# 确保 turnserver 用户存在
if ! id -u turnserver >/dev/null 2>&1; then
    echo "创建系统服务用户: turnserver"
    useradd -r -s /sbin/nologin -d /run/godeck-turn turnserver 2>/dev/null || adduser -S -s /sbin/nologin -h /run/godeck-turn turnserver 2>/dev/null
fi

# 证书隔离目录 (root:0700)
mkdir -p /etc/godeck/tls
chmod 0700 /etc/godeck/tls
chown root:root /etc/godeck/tls

# 维护专用 root 独占隔离目录 (杜绝普通服务用户通过父目录提权)
for maint_dir in "/etc/godeck-maintenance" "/var/lib/godeck-maintenance"; do
    if [ -L "$maint_dir" ]; then
        echo "安全拦截: 路径 $maint_dir 为符号链接，禁止安装" >&2
        exit 1
    fi
done
mkdir -p /etc/godeck-maintenance
chmod 0750 /etc/godeck-maintenance
chown root:root /etc/godeck-maintenance

mkdir -p /var/lib/godeck-maintenance
chmod 0700 /var/lib/godeck-maintenance
chown root:root /var/lib/godeck-maintenance

# Hub 专用目录
mkdir -p /var/lib/godeck-hub /run/godeck-hub /etc/godeck-hub /var/log/godeck-hub
chmod 0700 /var/lib/godeck-hub /run/godeck-hub /etc/godeck-hub
chmod 0750 /var/log/godeck-hub
chown -R godeck:godeck /var/lib/godeck-hub /run/godeck-hub /etc/godeck-hub /var/log/godeck-hub

# Turn 专用目录
mkdir -p /etc/godeck-turn /run/godeck-turn /var/log/godeck-turn
chmod 0750 /etc/godeck-turn /run/godeck-turn /var/log/godeck-turn
chown -R turnserver:turnserver /etc/godeck-turn /run/godeck-turn /var/log/godeck-turn

# 续期与维护专用 root-owned 隔离日志目录 (防软链接提权)
mkdir -p /var/log/godeck-cert-renew
chmod 0750 /var/log/godeck-cert-renew
chown root:root /var/log/godeck-cert-renew

echo "=== 3. 部署共享 TURN 认证密钥 ==="
TURN_SECRET_FILE="/etc/godeck-hub/turn-secret.key"
if [ ! -f "$TURN_SECRET_FILE" ]; then
    echo "生成专属高熵 32 字节共享密钥..."
    tr -dc 'A-Za-z0-9-_' < /dev/urandom | head -c 32 > "$TURN_SECRET_FILE"
    echo "" >> "$TURN_SECRET_FILE"
    chmod 0600 "$TURN_SECRET_FILE"
    chown godeck:godeck "$TURN_SECRET_FILE"
else
    echo "保留既有共享密钥: $TURN_SECRET_FILE"
fi

TURN_SECRET="$(cat "$TURN_SECRET_FILE" | tr -d '\r\n')"

echo "=== 4. 部署 OpenRC 服务脚本与配置 ==="
# Hub 服务
cp "$SCRIPT_DIR/openrc/godeck-hub.initd" /etc/init.d/godeck-hub
chmod 0755 /etc/init.d/godeck-hub
if [ ! -f /etc/conf.d/godeck-hub ] || [ $FORCE_INSTALL -eq 1 ]; then
    cp "$SCRIPT_DIR/openrc/godeck-hub.confd" /etc/conf.d/godeck-hub
    chmod 0644 /etc/conf.d/godeck-hub
fi

# Turn 服务
cp "$SCRIPT_DIR/openrc/godeck-turn.initd" /etc/init.d/godeck-turn
chmod 0755 /etc/init.d/godeck-turn
if [ ! -f /etc/conf.d/godeck-turn ] || [ $FORCE_INSTALL -eq 1 ]; then
    cp "$SCRIPT_DIR/openrc/godeck-turn.confd" /etc/conf.d/godeck-turn
    chmod 0644 /etc/conf.d/godeck-turn
fi

# 证书续期脚本与服务
cp "$SCRIPT_DIR/openrc/godeck-cert-renew.sh" /usr/local/bin/godeck-cert-renew.sh
chmod 0755 /usr/local/bin/godeck-cert-renew.sh
cp "$SCRIPT_DIR/openrc/godeck-cert-renew.initd" /etc/init.d/godeck-cert-renew
chmod 0755 /etc/init.d/godeck-cert-renew

# 专属 Logrotate 配置 (安装至 root 独占目录 /etc/godeck-maintenance/)
if [ -f "$SCRIPT_DIR/logrotate/godeck-logrotate.conf" ]; then
    if [ -L "/etc/godeck-maintenance/logrotate.conf" ]; then
        rm -f "/etc/godeck-maintenance/logrotate.conf"
    fi
    cp "$SCRIPT_DIR/logrotate/godeck-logrotate.conf" /etc/godeck-maintenance/logrotate.conf
    chmod 0644 /etc/godeck-maintenance/logrotate.conf
    chown root:root /etc/godeck-maintenance/logrotate.conf
    # 清理旧位置文件（若有）
    rm -f /etc/godeck-hub/logrotate.conf /var/lib/godeck-hub/logrotate.state 2>/dev/null || true
fi

echo "=== 5. 部署 Coturn 独立配置 (/etc/godeck-turn/turnserver.conf) ==="
sed "s/REPLACE_WITH_SHARED_SECRET_AT_DEPLOYMENT/$TURN_SECRET/g" "$SCRIPT_DIR/coturn/turnserver.conf" > /etc/godeck-turn/turnserver.conf
chmod 0600 /etc/godeck-turn/turnserver.conf
chown turnserver:turnserver /etc/godeck-turn/turnserver.conf

echo "=== 6. 部署 Nginx 专属配置片段 (/etc/nginx/conf.d/godeck.conf) ==="
mkdir -p /etc/nginx/conf.d
cp "$SCRIPT_DIR/nginx/godeck.conf" /etc/nginx/conf.d/godeck.conf
chmod 0644 /etc/nginx/conf.d/godeck.conf

echo "=== 7. 安装 godeck-hub 二进制程序 ==="
cp "$HUB_SRC_BIN" /usr/local/bin/godeck-hub
chmod 0755 /usr/local/bin/godeck-hub
chown root:root /usr/local/bin/godeck-hub

if [ ! -x "/usr/local/bin/godeck-hub" ]; then
    echo "错误: /usr/local/bin/godeck-hub 安装失败或不可执行" >&2
    exit 1
fi

echo ""
echo "================================================================================"
echo "GoDeck 本地文件部署完成！"
echo "================================================================================"
echo "协调者后续执行事项："
echo "1. Nginx 引入配置（若 /etc/nginx/nginx.conf 尚未包含 conf.d/*.conf）："
echo "   请在 /etc/nginx/nginx.conf 的 http { ... } 块内手动追加一行："
echo "       include /etc/nginx/conf.d/godeck.conf;"
echo ""
echo "2. 确认证书文件已就绪："
echo "   /etc/godeck/tls/fullchain.pem"
echo "   /etc/godeck/tls/privkey.pem"
echo "   确认无误后执行语法测试并平滑重载: nginx -t && rc-service nginx reload"
echo ""
echo "3. 启动并启用全部 GoDeck 专属服务："
echo "   rc-service godeck-turn restart && rc-update add godeck-turn default"
echo "   rc-service godeck-hub restart && rc-update add godeck-hub default"
echo "   rc-service godeck-cert-renew restart && rc-update add godeck-cert-renew default"
echo ""
echo "4. 获取初始化 Setup Code："
echo "   cat /run/godeck-hub/setup-code"
echo "================================================================================"
