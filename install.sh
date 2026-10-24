#!/bin/sh
set -e

# ========================================================
#  edt-gateway OpenWrt 系统级一键全自动化安装脚本
#  功能: 自动匹配架构、下载安装、创建独立用户、配置系统服务与升级保护
# ========================================================

RED='\033[0;31m'
GREEN='\033[0;32m'
BLUE='\033[0;34m'
YELLOW='\033[1;33m'
NC='\033[0m'

echo -e "${BLUE}==============================================${NC}"
echo -e "${BLUE}     edt-gateway OpenWrt 系统级一键安装脚本      ${NC}"
echo -e "${BLUE}==============================================${NC}"

# 1. 架构检测
ARCH=$(uname -m)
case "$ARCH" in
    x86_64|amd64)
        ASSET_NAME="edt-gateway-linux-amd64"
        ;;
    aarch64|arm64)
        ASSET_NAME="edt-gateway-linux-arm64"
        ;;
    *)
        echo -e "${RED}[!] 暂不支持的架构: $ARCH (当前仅支持 x86_64 与 aarch64)${NC}"
        exit 1
        ;;
esac
echo -e "${GREEN}[+] 检测到硬件架构: ${YELLOW}${ARCH}${GREEN}，匹配目标产物: ${YELLOW}${ASSET_NAME}${NC}"

# 2. 获取并下载 GitHub 最新 Release
REPO="aihddelyy/edgetunnel-gateway"
DOWNLOAD_URL="https://github.com/${REPO}/releases/latest/download/${ASSET_NAME}"
GH_PROXY_URL="https://ghfast.top/https://github.com/${REPO}/releases/latest/download/${ASSET_NAME}"

echo -e "${GREEN}[+] 正在下载最新版程序...${NC}"
TMP_BIN="/tmp/${ASSET_NAME}"
rm -f "$TMP_BIN"

# 优先直连，失败则尝试镜像加速下载
if ! curl -sSL -k -f --connect-timeout 8 "$DOWNLOAD_URL" -o "$TMP_BIN"; then
    echo -e "${YELLOW}[*] 直连下载超时，正在通过加速镜像下载...${NC}"
    curl -sSL -k -f "$GH_PROXY_URL" -o "$TMP_BIN" || {
        echo -e "${RED}[!] 下载失败，请检查网络连接！${NC}"
        exit 1
    }
fi

# 3. 安装到系统全局 PATH
echo -e "${GREEN}[+] 正在安装系统程序至 /usr/bin/edt-gateway...${NC}"
killall edt-gateway 2>/dev/null || true
cp -f "$TMP_BIN" /usr/bin/edt-gateway
chmod 755 /usr/bin/edt-gateway
rm -f "$TMP_BIN"

# 4. 创建专属系统用户 edtgw (若不存在)
echo -e "${GREEN}[+] 正在配置专属系统用户 edtgw...${NC}"
if ! grep -q "^edtgw:" /etc/group 2>/dev/null; then
    echo "edtgw:x:888:" >> /etc/group
fi
if ! grep -q "^edtgw:" /etc/passwd 2>/dev/null; then
    echo "edtgw:x:888:888:edtgw:/var/run/edt-gateway:/bin/false" >> /etc/passwd
fi

# 5. 初始化配置与运行时目录
CONF_DIR="/etc/edt-gateway"
LOG_DIR="/var/log/edt-gateway"
mkdir -p "$CONF_DIR" "$LOG_DIR"

if [ ! -f "${CONF_DIR}/config.json" ]; then
    echo -e "${GREEN}[+] 创建初始配置文件 ${CONF_DIR}/config.json...${NC}"
    cat << 'JSON' > "${CONF_DIR}/config.json"
{
  "port": 8080,
  "adminPassword": "",
  "defaultPrefer": "",
  "backends": []
}
JSON
else
    echo -e "${YELLOW}[*] 检测到已存在配置文件，保持原有配置不变。${NC}"
fi

# 授权目录给 edtgw
chown -R edtgw:edtgw "$CONF_DIR" "$LOG_DIR"

# 6. 创建 procd 系统服务脚本
echo -e "${GREEN}[+] 部署 OpenWrt 原生服务脚本 (/etc/init.d/edt-gateway)...${NC}"
cat << 'SERVICE' > /etc/init.d/edt-gateway
#!/bin/sh /etc/rc.common

START=95
STOP=10
USE_PROCD=1

PROG="/usr/bin/edt-gateway"
CONF_DIR="/etc/edt-gateway"
CONF_FILE="${CONF_DIR}/config.json"
LOG_DIR="/var/log/edt-gateway"
LOG_FILE="${LOG_DIR}/app.log"

start_service() {
    [ -f "$PROG" ] || return 1
    [ -f "$CONF_FILE" ] || return 1

    mkdir -p "$LOG_DIR"
    chown -R edtgw:edtgw "$LOG_DIR"

    procd_open_instance "main"

    # 以专属用户 edtgw 身份运行 (配合 Nikki 等透明代理绕行防环路)
    procd_set_param user edtgw
    procd_set_param group edtgw

    # 输出重定向至独立日志，不污染系统日志
    procd_set_param command /bin/sh -c "exec $PROG -config $CONF_FILE >> $LOG_FILE 2>&1"
    procd_set_param dir "$CONF_DIR"
    procd_set_param respawn 3600 5 3

    # 关闭系统日志捕获
    procd_set_param stdout 0
    procd_set_param stderr 0

    procd_close_instance
}
SERVICE

chmod +x /etc/init.d/edt-gateway

# 7. 配置固件升级保留名单
echo -e "${GREEN}[+] 配置固件升级自动保留名单 (/lib/upgrade/keep.d/edt-gateway)...${NC}"
mkdir -p /lib/upgrade/keep.d
cat << 'KEEP' > /lib/upgrade/keep.d/edt-gateway
/usr/bin/edt-gateway
/etc/edt-gateway/
/etc/init.d/edt-gateway
/etc/rc.d/S95edt-gateway
/lib/upgrade/keep.d/edt-gateway
KEEP

# 8. 启用开机自启并启动服务
echo -e "${GREEN}[+] 注册开机自启并启动服务...${NC}"
/etc/init.d/edt-gateway enable
/etc/init.d/edt-gateway start

LAN_IP=$(uci get network.lan.ipaddr 2>/dev/null || echo "127.0.0.1")

echo -e "\n${GREEN}==============================================${NC}"
echo -e "${GREEN}      ✅ edt-gateway 安装与服务配置圆满完成！   ${NC}"
echo -e "${GREEN}==============================================${NC}"
echo -e "1. 控制台访问地址 : ${YELLOW}http://${LAN_IP}:8080${NC}"
echo -e "2. 配置文件路径   : ${YELLOW}/etc/edt-gateway/config.json${NC}"
echo -e "3. 专属运行用户   : ${YELLOW}edtgw${NC} (UID: 888)"
echo -e "4. 独立日志路径   : ${YELLOW}/var/log/edt-gateway/app.log${NC}"
echo -e "5. 系统管理命令   : ${YELLOW}service edt-gateway status | restart | stop${NC}"
echo -e "----------------------------------------------"
echo -e "${YELLOW}【配合 Nikki (Mihomo) 绕行避坑说明】：${NC}"
echo -e "若同机运行 Nikki，请进入 Nikki 网页后台 -> 【访问控制】："
echo -e "在第一行（不走代理的免劫持列表）中，选择用户 【edtgw】 添加并保存应用即可！"
echo -e "${GREEN}==============================================${NC}"
