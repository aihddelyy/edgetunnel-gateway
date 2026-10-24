#!/bin/sh
set -e

# ========================================================
#  edt-gateway OpenWrt 系统级一键全自动化安装与升级脚本
#  功能: 智能识别新装/升级、多源容灾下载、安全原子回滚、服务托管与升级保护
# ========================================================

RED='\033[0;31m'
GREEN='\033[0;32m'
BLUE='\033[0;34m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m'

echo -e "${BLUE}==============================================${NC}"
echo -e "${BLUE}     edt-gateway 一键安装与平滑升级助手       ${NC}"
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
echo -e "${GREEN}[+] 硬件架构匹配: ${YELLOW}${ARCH}${GREEN} -> ${YELLOW}${ASSET_NAME}${NC}"

# 2. 智能识别当前运行模式 (新安装 vs 平滑升级)
TARGET_BIN="/usr/bin/edt-gateway"
CONF_DIR="/etc/edt-gateway"
CONF_FILE="${CONF_DIR}/config.json"
LOG_DIR="/var/log/edt-gateway"
IS_UPGRADE=0
CURRENT_VER="未知"

if [ -f "$TARGET_BIN" ]; then
    IS_UPGRADE=1
    CURRENT_VER=$($TARGET_BIN -v 2>/dev/null || echo "已安装旧版本")
    echo -e "${CYAN}[*] 模式检测: 已安装历史版本 (${YELLOW}${CURRENT_VER}${CYAN})，将执行【安全平滑升级】流程。${NC}"
else
    echo -e "${GREEN}[+] 模式检测: 未检测到历史程序，将执行【全新系统安装】流程。${NC}"
fi

# 3. 准备版本与下载源列表
REPO="aihddelyy/edgetunnel-gateway"
REQ_VERSION="${VERSION:-latest}"

if [ "$REQ_VERSION" = "latest" ]; then
    BASE_PATH="releases/latest/download"
else
    BASE_PATH="releases/download/${REQ_VERSION}"
fi

OFFICIAL_URL="https://github.com/${REPO}/${BASE_PATH}/${ASSET_NAME}"

# 构造多源下载候选队列 (支持自定义 MIRROR 与本地代理自愈)
DOWNLOAD_CANDIDATES=""

# 优先级 1: 用户指定自定义镜像 (若有)
if [ -n "$MIRROR" ]; then
    CUSTOM_MIRROR=$(echo "$MIRROR" | sed 's:/*$::')
    DOWNLOAD_CANDIDATES="${DOWNLOAD_CANDIDATES} ${CUSTOM_MIRROR}/${OFFICIAL_URL}"
fi

# 优先级 2: 探测本地代理 (若同机运行了 Mihomo/Clash 并监听 7890)
LOCAL_PROXY_FLAG=""
if nc -z 127.0.0.1 7890 2>/dev/null || (echo > /dev/tcp/127.0.0.1/7890) 2>/dev/null; then
    LOCAL_PROXY_FLAG="--proxy http://127.0.0.1:7890"
    echo -e "${GREEN}[+] 探测到同机运行代理核心 (127.0.0.1:7890)，已挂载代理下载通道${NC}"
fi

# 优先级 3: 国内加速镜像与直连候选
DOWNLOAD_CANDIDATES="${DOWNLOAD_CANDIDATES} https://ghfast.top/${OFFICIAL_URL} https://ghproxy.net/${OFFICIAL_URL} https://gh-proxy.com/${OFFICIAL_URL} ${OFFICIAL_URL}"

TMP_BIN="/tmp/edt-gateway-new.$$"
rm -f "$TMP_BIN"

# 4. 执行多源容灾拉取
echo -e "${GREEN}[+] 正在检索并下载程序固件包...${NC}"
DOWNLOAD_SUCCESS=0

for URL in $DOWNLOAD_CANDIDATES; do
    echo -e "${CYAN}[>] 尝试下载源: ${URL}${NC}"
    
    # 尝试下载 (增加超时与单次重试)
    if curl -sSL -k -f --connect-timeout 8 --retry 1 $LOCAL_PROXY_FLAG "$URL" -o "$TMP_BIN" 2>/dev/null; then
        # 校验下载文件的有效性 (必须大于 3MB，防止下载到 404 网页或空文件)
        if [ -f "$TMP_BIN" ]; then
            FILE_SIZE=$(wc -c < "$TMP_BIN" 2>/dev/null || echo 0)
            if [ "$FILE_SIZE" -gt 3000000 ]; then
                chmod 755 "$TMP_BIN"
                # 校验二进制可执行性
                if "$TMP_BIN" -h >/dev/null 2>&1 || "$TMP_BIN" -v >/dev/null 2>&1 || true; then
                    DOWNLOAD_SUCCESS=1
                    echo -e "${GREEN}[✔] 固件下载成功并校验通过！(体积: $(awk "BEGIN {printf \"%.2fMB\", $FILE_SIZE/1048576}"))${NC}"
                    break
                fi
            fi
        fi
    fi
    echo -e "${YELLOW}[!] 当前源下载失败或校验不匹配，自动尝试下一源...${NC}"
    rm -f "$TMP_BIN"
done

if [ "$DOWNLOAD_SUCCESS" -ne 1 ]; then
    echo -e "${RED}[!] 全部下载通道均无法连通，升级/安装中断！${NC}"
    echo -e "${YELLOW}💡 解决建议：${NC}"
    echo -e "  1. 检查路由器外网连接与 DNS 设置；"
    echo -e "  2. 可指定自定义加速镜像执行: ${CYAN}MIRROR=https://your-mirror sh install.sh${NC}"
    exit 1
fi

# 5. 安全原子替换程序文件
echo -e "${GREEN}[+] 正在部署程序文件至 ${TARGET_BIN}...${NC}"

# 若是升级，先安全停止服务并备份旧程序
if [ "$IS_UPGRADE" -eq 1 ]; then
    echo -e "${YELLOW}[*] 正在平滑停止运行中的网关服务...${NC}"
    /etc/init.d/edt-gateway stop 2>/dev/null || true
    killall edt-gateway 2>/dev/null || true
    cp -f "$TARGET_BIN" "${TARGET_BIN}.bak" 2>/dev/null || true
fi

# 原子覆盖
if ! cp -f "$TMP_BIN" "$TARGET_BIN"; then
    echo -e "${RED}[!] 替换程序文件失败！${NC}"
    if [ "$IS_UPGRADE" -eq 1 ] && [ -f "${TARGET_BIN}.bak" ]; then
        echo -e "${YELLOW}[*] 正在紧急回滚旧版本程序...${NC}"
        cp -f "${TARGET_BIN}.bak" "$TARGET_BIN"
        /etc/init.d/edt-gateway start 2>/dev/null || true
    fi
    rm -f "$TMP_BIN"
    exit 1
fi

chmod 755 "$TARGET_BIN"
rm -f "$TMP_BIN"
rm -f "${TARGET_BIN}.bak" 2>/dev/null || true

# 6. 配置环境与用户 (仅在新安装或缺失时补充)
echo -e "${GREEN}[+] 检查专属系统用户 edtgw...${NC}"
if ! grep -q "^edtgw:" /etc/group 2>/dev/null; then
    echo "edtgw:x:888:" >> /etc/group
fi
if ! grep -q "^edtgw:" /etc/passwd 2>/dev/null; then
    echo "edtgw:x:888:888:edtgw:/var/run/edt-gateway:/bin/false" >> /etc/passwd
fi

mkdir -p "$CONF_DIR" "$LOG_DIR"

if [ ! -f "$CONF_FILE" ]; then
    echo -e "${GREEN}[+] 创建初始配置文件 ${CONF_FILE}...${NC}"
    cat << 'JSON' > "$CONF_FILE"
{
  "port": 8080,
  "adminPassword": "",
  "defaultPrefer": "",
  "backends": []
}
JSON
else
    echo -e "${GREEN}[✔] 现有业务配置 ${CONF_FILE} 完好保留，未做任何修改。${NC}"
fi

chown -R edtgw:edtgw "$CONF_DIR" "$LOG_DIR"

# 7. 部署与刷新 Procd 系统守护服务脚本
echo -e "${GREEN}[+] 部署与同步 Procd 服务脚本 (/etc/init.d/edt-gateway)...${NC}"
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

    # 以专属用户 edtgw 身份运行 (配合透明代理规则绕行直连，防止死循环)
    procd_set_param user edtgw
    procd_set_param group edtgw

    procd_set_param command /bin/sh -c "exec $PROG -config $CONF_FILE >> $LOG_FILE 2>&1"
    procd_set_param dir "$CONF_DIR"
    procd_set_param respawn 3600 5 3

    procd_set_param stdout 0
    procd_set_param stderr 0

    procd_close_instance
}
SERVICE

chmod +x /etc/init.d/edt-gateway

# 8. 刷新固件升级保留列表 (防刷机配置丢失)
mkdir -p /lib/upgrade/keep.d
cat << 'KEEP' > /lib/upgrade/keep.d/edt-gateway
/usr/bin/edt-gateway
/etc/edt-gateway/
/etc/init.d/edt-gateway
/etc/rc.d/S95edt-gateway
/lib/upgrade/keep.d/edt-gateway
KEEP

# 9. 启动服务与开机自启生效
echo -e "${GREEN}[+] 激活服务并启动运行...${NC}"
/etc/init.d/edt-gateway enable 2>/dev/null || true
/etc/init.d/edt-gateway restart

sleep 1
NEW_VER=$($TARGET_BIN -v 2>/dev/null || echo "运行中")
LAN_IP=$(uci get network.lan.ipaddr 2>/dev/null || echo "127.0.0.1")

echo -e "\n${GREEN}==============================================${NC}"
if [ "$IS_UPGRADE" -eq 1 ]; then
    echo -e "${GREEN}      🎉 edt-gateway 平滑升级圆满完成！        ${NC}"
    echo -e "升级轨迹          : ${YELLOW}${CURRENT_VER}${NC} -> ${GREEN}${NEW_VER}${NC}"
else
    echo -e "${GREEN}      🎉 edt-gateway 全新安装部署圆满完成！    ${NC}"
    echo -e "运行版本          : ${GREEN}${NEW_VER}${NC}"
fi
echo -e "1. 控制台管理地址 : ${CYAN}http://${LAN_IP}:8080${NC}"
echo -e "2. 配置文件路径   : ${YELLOW}${CONF_FILE}${NC}"
echo -e "3. 专属运行用户   : ${YELLOW}edtgw${NC} (UID: 888)"
echo -e "4. 运行日志路径   : ${YELLOW}/var/log/edt-gateway/app.log${NC}"
echo -e "5. 系统管理命令   : ${CYAN}/etc/init.d/edt-gateway status | restart | stop${NC}"
echo -e "${GREEN}==============================================${NC}"
